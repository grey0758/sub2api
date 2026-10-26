// Package imageinput resolves remote images before a protocol adapter that only
// supports inline images. It never removes an image or rewrites its bytes.
package imageinput

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	maxImageBytes = 20 << 20
	maxTotalBytes = 32 << 20
	maxImages     = 20
)

type Stats struct {
	Images int
	Remote int
	Bytes  int64
}

// Error intentionally contains no URL, response body or transport error text.
type Error struct {
	Code   string
	Status int
	Index  int
}

func (e *Error) Error() string { return fmt.Sprintf("reference image %d: %s", e.Index+1, e.Code) }

type resolver struct {
	client     *http.Client
	imageLimit int64
	totalLimit int64
	timeout    time.Duration
	slots      chan struct{}
}

var defaultResolver = &resolver{
	client: &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			// Untrusted image URLs must never inherit an environment proxy.
			Proxy:                 nil,
			DialContext:           safeDialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			IdleConnTimeout:       60 * time.Second,
			MaxIdleConns:          16,
			MaxConnsPerHost:       4,
		},
		CheckRedirect: checkRedirect,
	},
	imageLimit: maxImageBytes,
	totalLimit: maxTotalBytes,
	timeout:    60 * time.Second,
	slots:      make(chan struct{}, 8),
}

func Resolve(ctx context.Context, input json.RawMessage) (json.RawMessage, Stats, error) {
	return defaultResolver.resolve(ctx, input)
}

type target struct {
	part   map[string]any
	source string
}

func (r *resolver) resolve(ctx context.Context, input json.RawMessage) (json.RawMessage, Stats, error) {
	var stats Stats
	if len(input) == 0 {
		return input, stats, nil
	}
	var root any
	decoder := json.NewDecoder(strings.NewReader(string(input)))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, stats, &Error{Code: "invalid_image_input", Status: 400}
	}
	var targets []target
	var visit func(any) error
	visit = func(value any) error {
		switch node := value.(type) {
		case []any:
			for _, child := range node {
				if err := visit(child); err != nil {
					return err
				}
			}
		case map[string]any:
			if node["type"] == "input_image" {
				source, ok := node["image_url"].(string)
				if !ok || strings.TrimSpace(source) == "" {
					return &Error{Code: "image_url_required", Status: 400, Index: len(targets)}
				}
				targets = append(targets, target{node, source})
				if len(targets) > maxImages {
					return &Error{Code: "too_many_images", Status: 413, Index: len(targets) - 1}
				}
				return nil
			}
			// Only traverse message/media fields, never tool arguments, schemas,
			// text, or JSON-looking strings returned by a tool.
			for _, key := range []string{"content", "output"} {
				if child, ok := node[key]; ok {
					if err := visit(child); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, stats, err
	}
	stats.Images = len(targets)
	if len(targets) == 0 {
		return input, stats, nil
	}
	for i, t := range targets {
		if strings.HasPrefix(t.source, "data:") {
			n, err := validateInline(t.source, r.imageLimit)
			if err != nil {
				return nil, stats, &Error{Code: "invalid_inline_image", Status: 400, Index: i}
			}
			stats.Bytes += n
		} else {
			u, err := url.Parse(t.source)
			if err != nil || validateURL(u) != nil {
				return nil, stats, &Error{Code: "unsafe_or_unsupported_image_url", Status: 400, Index: i}
			}
			stats.Remote++
		}
	}
	if stats.Bytes > r.totalLimit {
		return nil, stats, &Error{Code: "images_too_large", Status: 413}
	}
	if stats.Remote == 0 {
		return input, stats, nil
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	parallel := make(chan struct{}, 2)
	for i, t := range targets {
		if strings.HasPrefix(t.source, "data:") {
			continue
		}
		wg.Add(1)
		go func(i int, t target) {
			defer wg.Done()
			select {
			case parallel <- struct{}{}:
				defer func() { <-parallel }()
			case <-ctx.Done():
				mu.Lock()
				if firstErr == nil {
					firstErr = &Error{Code: "image_download_timeout", Status: 504, Index: i}
				}
				mu.Unlock()
				return
			}
			data, err := r.fetch(ctx, t.source)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					var e *Error
					if errors.As(err, &e) {
						copy := *e
						copy.Index = i
						firstErr = &copy
					} else {
						firstErr = &Error{Code: "image_download_failed", Status: 502, Index: i}
					}
					cancel()
				}
				return
			}
			stats.Bytes += int64(len(data))
			if stats.Bytes > r.totalLimit {
				if firstErr == nil {
					firstErr = &Error{Code: "images_too_large", Status: 413, Index: i}
					cancel()
				}
				return
			}
			t.part["image_url"] = "data:" + http.DetectContentType(data) + ";base64," + base64.StdEncoding.EncodeToString(data)
		}(i, t)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, stats, firstErr
	}
	result, err := json.Marshal(root)
	return result, stats, err
}

func validateInline(source string, limit int64) (int64, error) {
	comma := strings.IndexByte(source, ',')
	if comma < 0 || !strings.HasSuffix(source[:comma], ";base64") || !strings.HasPrefix(source, "data:image/") {
		return 0, errors.New("invalid image")
	}
	encoded := source[comma+1:]
	if int64(len(encoded)) > ((limit+2)/3)*4 {
		return 0, errors.New("image too large")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || int64(len(data)) > limit {
		return 0, errors.New("invalid image")
	}
	return int64(len(data)), nil
}

func (r *resolver) fetch(ctx context.Context, source string) ([]byte, error) {
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		return nil, &Error{Code: "image_download_timeout", Status: 504}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, &Error{Code: "unsafe_or_unsupported_image_url", Status: 400}
	}
	req.Header.Set("Accept", "image/*")
	resp, err := r.client.Do(req)
	if err != nil {
		var netErr net.Error
		if ctx.Err() != nil || errors.As(err, &netErr) && netErr.Timeout() {
			return nil, &Error{Code: "image_download_timeout", Status: 504}
		}
		return nil, &Error{Code: "image_download_failed", Status: 502}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, &Error{Code: "image_source_http_error", Status: 502}
	}
	if resp.ContentLength > r.imageLimit {
		return nil, &Error{Code: "image_too_large", Status: 413}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, r.imageLimit+1))
	if err != nil {
		var netErr net.Error
		if ctx.Err() != nil || errors.As(err, &netErr) && netErr.Timeout() {
			return nil, &Error{Code: "image_download_timeout", Status: 504}
		}
		return nil, &Error{Code: "image_body_incomplete", Status: 502}
	}
	if int64(len(data)) > r.imageLimit {
		return nil, &Error{Code: "image_too_large", Status: 413}
	}
	switch http.DetectContentType(data) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return nil, &Error{Code: "unsupported_image_content", Status: 400}
	}
	return data, nil
}

func validateURL(u *url.URL) error {
	if u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return errors.New("unsupported URL")
	}
	if p := u.Port(); p != "" && p != "443" && p != "80" {
		return errors.New("unsupported port")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "metadata" || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") || host == "instance-data" {
		return errors.New("private host")
	}
	if ip, err := netip.ParseAddr(host); err == nil && blockedIP(ip) {
		return errors.New("private IP")
	}
	return nil
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("2001::/32"),
}

func blockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return errors.New("too many image redirects")
	}
	if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("insecure image redirect")
	}
	return validateURL(req.URL)
}

func safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return dialPublic(ctx, network, address, net.DefaultResolver.LookupIPAddr, dialer.DialContext)
}

func dialPublic(ctx context.Context, network, address string, lookup func(context.Context, string) ([]net.IPAddr, error), dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addrs, err := lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		return nil, errors.New("image DNS lookup failed")
	}
	for _, a := range addrs {
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok || blockedIP(ip) {
			return nil, errors.New("private image address")
		}
	}
	for _, a := range addrs {
		conn, err := dial(ctx, network, net.JoinHostPort(a.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("image connection failed")
}
