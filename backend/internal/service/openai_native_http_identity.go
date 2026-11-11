package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

type nativeCodexIdentityContextKey struct{}
type nativeCodexRequestIdentity struct{ userAgent string }

var nativeCodexHTTPHeaders = map[string]bool{"session-id": true, "thread-id": true, "x-client-request-id": true}

func validNativeCodexUserAgent(ua string) bool {
	if len(ua) > 512 || !strings.HasPrefix(ua, "codex_cli_rs/") {
		return false
	}
	for _, c := range ua {
		if c < 32 || c > 126 {
			return false
		}
	}
	version := strings.SplitN(strings.TrimPrefix(ua, "codex_cli_rs/"), " ", 2)[0]
	return NormalizeCodexClientVersion(version) != "" && CompareVersions(version, codexUpstreamMinVersion) >= 0
}

// Only the authenticated native lease supplies the identity. The global latest
// version resolver must not relabel a request from a pinned official client.
func applyNativeCodexHTTPRequest(ctx context.Context, req *http.Request, body []byte) error {
	identity, _ := ctx.Value(nativeCodexIdentityContextKey{}).(*nativeCodexRequestIdentity)
	if identity == nil || !validNativeCodexUserAgent(identity.userAgent) {
		return errors.New("native Codex client identity unavailable")
	}
	req.Header.Set("User-Agent", identity.userAgent)
	req.Header.Set("originator", "codex_cli_rs")
	for _, key := range []string{"version", "conversation_id", "session_id", openAICodexRoutingHintHeader} {
		req.Header.Del(key)
	}
	cm := gjson.GetBytes(body, "client_metadata")
	for header, field := range map[string]string{"session-id": "session_id", "thread-id": "thread_id"} {
		if value := cm.Get(field).String(); value != "" {
			req.Header.Set(header, value)
		}
	}
	// Native HTTP accepts complete history. Lite instructions and tool schemas
	// already live in input; the gateway must not invent a second instruction set.
	*req = *req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileNativeCodex))
	return nil
}
