package imageinput

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Sufficient format evidence for the resolver; no raster transformations occur.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)

func testResolver(fn transportFunc) *resolver {
	return &resolver{client: &http.Client{Transport: fn}, imageLimit: 1024, totalLimit: 4096, timeout: time.Second, slots: make(chan struct{}, 8)}
}
func input(parts ...map[string]any) json.RawMessage {
	b, _ := json.Marshal([]any{map[string]any{"type": "message", "role": "user", "content": parts}})
	return b
}
func response(b []byte) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), ContentLength: int64(len(b)), Header: make(http.Header)}
}
func TestResolvePreservesBytesOrderAndMetadata(t *testing.T) {
	first := append(append([]byte{}, pngBytes...), []byte("first-reference-EXIF-sentinel")...)
	second := append(append([]byte{}, pngBytes...), []byte("second-reference")...)
	r := testResolver(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/first" {
			time.Sleep(20 * time.Millisecond)
			return response(first), nil
		}
		return response(second), nil
	})
	raw := input(map[string]any{"type": "input_text", "text": "Do not change 9007199254740993"}, map[string]any{"type": "input_image", "image_url": "https://images.example/first", "detail": "high", "extra": json.Number("9007199254740993")}, map[string]any{"type": "input_image", "image_url": "https://images.example/second"})
	got, stats, err := r.resolve(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var root []struct {
		Content []map[string]json.RawMessage `json:"content"`
	}
	if err = json.Unmarshal(got, &root); err != nil {
		t.Fatal(err)
	}
	for i, want := range [][]byte{first, second} {
		var uri string
		json.Unmarshal(root[0].Content[i+1]["image_url"], &uri)
		b, err := base64.StdEncoding.DecodeString(strings.SplitN(uri, ",", 2)[1])
		if err != nil || !bytes.Equal(b, want) {
			t.Fatalf("image %d altered or reordered", i)
		}
	}
	if string(root[0].Content[1]["extra"]) != "9007199254740993" || string(root[0].Content[1]["detail"]) != `"high"` {
		t.Fatal("metadata altered")
	}
	if stats.Images != 2 || stats.Remote != 2 || stats.Bytes != int64(len(first)+len(second)) {
		t.Fatalf("stats %+v", stats)
	}
}

func TestResolveInlineAndTextAreUnchanged(t *testing.T) {
	r := testResolver(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network"); return nil, nil })
	for _, raw := range []json.RawMessage{json.RawMessage(`"literal https://example.com/image.jpg"`), input(map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)})} {
		got, _, err := r.resolve(context.Background(), raw)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("unchanged input: %s %v", got, err)
		}
	}
}

func TestResolveNestedToolMediaOnly(t *testing.T) {
	var calls atomic.Int32
	r := testResolver(func(*http.Request) (*http.Response, error) { calls.Add(1); return response(pngBytes), nil })
	raw := json.RawMessage(`[{"type":"function_call_output","output":[{"type":"input_image","image_url":"https://images.example/reference"}]},{"type":"function_call","arguments":"{\"type\":\"input_image\",\"image_url\":\"https://ignored.example/\"}"}]`)
	_, stats, err := r.resolve(context.Background(), raw)
	if err != nil || calls.Load() != 1 || stats.Images != 1 {
		t.Fatalf("%+v %v calls%d", stats, err, calls.Load())
	}
}

func TestResolveFailsInsteadOfDroppingImage(t *testing.T) {
	tests := []struct {
		name         string
		uri          string
		makeResponse transportFunc
		code         string
		status       int
	}{
		{"source_status", "https://images.example/secret?signature=DO_NOT_LOG", func(*http.Request) (*http.Response, error) { r := response(nil); r.StatusCode = 403; return r, nil }, "image_source_http_error", 502},
		{"not_image", "https://images.example/", func(*http.Request) (*http.Response, error) { return response([]byte("<html>login</html>")), nil }, "unsupported_image_content", 400},
		{"oversize", "https://images.example/", func(*http.Request) (*http.Response, error) { return response(bytes.Repeat([]byte{0}, 1025)), nil }, "image_too_large", 413},
		{"bad_scheme", "file:///etc/passwd", nil, "unsafe_or_unsupported_image_url", 400},
		{"private_ip", "http://169.254.169.254/", nil, "unsafe_or_unsupported_image_url", 400},
		{"bad_inline", "data:image/png;base64,", nil, "invalid_inline_image", 400},
		{"transport", "https://images.example/secret", func(*http.Request) (*http.Response, error) {
			return nil, errors.New("secret credential transport message")
		}, "image_download_failed", 502},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := testResolver(func(req *http.Request) (*http.Response, error) {
				if tc.makeResponse == nil {
					t.Fatal("network for rejected input")
				}
				return tc.makeResponse(req)
			})
			got, _, err := r.resolve(context.Background(), input(map[string]any{"type": "input_text", "text": "keep me"}, map[string]any{"type": "input_image", "image_url": tc.uri}))
			var e *Error
			if got != nil || !errors.As(err, &e) || e.Code != tc.code || e.Status != tc.status {
				t.Fatalf("got%s err%v", got, err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "DO_NOT_LOG") || strings.Contains(err.Error(), "://") {
				t.Fatal("error leaked source")
			}
		})
	}
}

func TestResolveBoundedConcurrencyCancellationAndAggregate(t *testing.T) {
	var active, peak atomic.Int32
	r := testResolver(func(req *http.Request) (*http.Response, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		select {
		case <-time.After(15 * time.Millisecond):
			return response(pngBytes), nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})
	parts := make([]map[string]any, 8)
	for i := range parts {
		parts[i] = map[string]any{"type": "input_image", "image_url": "https://images.example/ref"}
	}
	if _, _, err := r.resolve(context.Background(), input(parts...)); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 {
		t.Fatalf("peak %d", peak.Load())
	}
	r.totalLimit = int64(len(pngBytes))
	if got, _, err := r.resolve(context.Background(), input(parts[:2]...)); err == nil || got != nil {
		t.Fatal("aggregate limit ignored")
	}
	r.totalLimit = 4096
	r.timeout = time.Millisecond
	if got, _, err := r.resolve(context.Background(), input(parts...)); err == nil || got != nil {
		t.Fatal("timeout ignored")
	} else {
		var e *Error
		if !errors.As(err, &e) || e.Status != 504 {
			t.Fatalf("timeout misclassified: %v", err)
		}
	}
}

func TestValidateURLAndRedirect(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1/", "http://[::ffff:127.0.0.1]/", "https://[64:ff9b::a00:1]/", "https://metadata.google.internal/", "https://localhost./", "https://name:password@example.com/", "https://example.com:22/", "ftp://example.com/x", "https://example.com/x#fragment"} {
		u, _ := url.Parse(raw)
		if validateURL(u) == nil {
			t.Errorf("unsafe URL accepted %s", raw)
		}
	}
	u, _ := url.Parse("https://images.example/x")
	via := []*http.Request{{URL: u}}
	private, _ := http.NewRequest("GET", "http://127.0.0.1/", nil)
	if checkRedirect(private, via) == nil {
		t.Fatal("private redirect allowed")
	}
	down, _ := http.NewRequest("GET", "http://images.example/", nil)
	if checkRedirect(down, via) == nil {
		t.Fatal("downgrade allowed")
	}
}

func TestDialPinsPublicIPAndRejectsMixedDNS(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		calls := 0
		lookup := func(context.Context, string) ([]net.IPAddr, error) {
			a := []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}
			if mixed {
				a = append(a, net.IPAddr{IP: net.ParseIP("10.0.0.1")})
			}
			return a, nil
		}
		dial := func(ctx context.Context, network, address string) (net.Conn, error) {
			calls++
			if address != "8.8.8.8:443" {
				t.Fatal("hostname was not pinned")
			}
			return nil, errors.New("test connection")
		}
		_, err := dialPublic(context.Background(), "tcp", "images.example:443", lookup, dial)
		if err == nil {
			t.Fatal("expected synthetic failure")
		}
		if mixed && calls != 0 || !mixed && calls != 1 {
			t.Fatalf("mixed%v calls%d", mixed, calls)
		}
	}
	for _, ip := range []string{"127.0.0.1", "::1", "::ffff:10.0.0.1", "169.254.169.254", "100.64.0.1", "192.168.1.1", "224.0.0.1", "240.0.0.1", "64:ff9b::7f00:1"} {
		if !blockedIP(netip.MustParseAddr(ip)) {
			t.Errorf("not blocked %s", ip)
		}
	}
}
