package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Disabled unless a host-owned, short-lived control file matches a synthetic
// marker. Only the selected native account can enter; no customer-wide capture.
func prepareNativeCodexPacketAudit(req *http.Request, account *Account) (*http.Request, func(*http.Response, error)) {
	noop := func(*http.Response, error) {}
	dir := os.Getenv("OPENAI_NATIVE_PACKET_AUDIT_DIR")
	if dir == "" || !filepath.IsAbs(dir) || !account.UsesNativeCodexTokenBroker() || req.GetBody == nil {
		return req, noop
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return req, noop
	}
	control, err := os.ReadFile(filepath.Join(dir, "control.json"))
	if err != nil || len(control) > 4096 {
		return req, noop
	}
	var scope struct {
		AccountID int64  `json:"account_id"`
		Marker    string `json:"marker"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if json.Unmarshal(control, &scope) != nil || scope.AccountID != account.ID || len(scope.Marker) < 32 || scope.ExpiresAt <= time.Now().Unix() || scope.ExpiresAt > time.Now().Add(30*time.Minute).Unix() {
		return req, noop
	}
	reader, err := req.GetBody()
	if err != nil {
		return req, noop
	}
	body, err := io.ReadAll(io.LimitReader(reader, 1048577))
	reader.Close()
	if err != nil || len(body) > 1048576 || !bytes.Contains(body, []byte(scope.Marker)) {
		return req, noop
	}
	var file *os.File
	var prefix string
	for slot := 0; slot < 16; slot++ {
		prefix = filepath.Join(dir, fmt.Sprintf("attempt-%02d", slot))
		file, err = os.OpenFile(prefix+".intent", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			break
		}
		file = nil
	}
	if file == nil {
		return req, noop
	}
	file.Close()
	if os.WriteFile(prefix+".body", body, 0600) != nil {
		return req, noop
	}
	var mu sync.Mutex
	headers := []map[string]any{}
	written := false
	reused := false
	trace := &httptrace.ClientTrace{
		WroteHeaderField: func(key string, values []string) {
			mu.Lock()
			defer mu.Unlock()
			safe := append([]string(nil), values...)
			switch strings.ToLower(key) {
			case "authorization", "proxy-authorization", "cookie":
				for i, v := range safe {
					safe[i] = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(v)))
				}
			}
			headers = append(headers, map[string]any{"name": strings.ToLower(key), "values": safe})
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) { mu.Lock(); defer mu.Unlock(); written = info.Err == nil },
		GotConn:      func(info httptrace.GotConnInfo) { mu.Lock(); defer mu.Unlock(); reused = info.Reused },
	}
	observed := req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	return observed, func(resp *http.Response, dispatchErr error) {
		mu.Lock()
		defer mu.Unlock()
		metadata := map[string]any{"utc": time.Now().UTC().Format(time.RFC3339Nano), "account_id": account.ID, "method": req.Method, "host": req.URL.Host, "path": req.URL.Path, "body_bytes": len(body), "body_sha256": fmt.Sprintf("%x", sha256.Sum256(body)), "headers_written": headers, "wrote_request": written, "connection_reused": reused, "dispatch_error": dispatchErr != nil}
		if resp != nil {
			metadata["status"] = resp.StatusCode
			metadata["protocol"] = resp.Proto
			if resp.TLS != nil {
				metadata["tls"] = map[string]any{"version": resp.TLS.Version, "cipher_suite": resp.TLS.CipherSuite, "alpn": resp.TLS.NegotiatedProtocol}
			}
		}
		encoded, _ := json.Marshal(metadata)
		_ = os.WriteFile(prefix+".wire.json", encoded, 0600)
	}
}
