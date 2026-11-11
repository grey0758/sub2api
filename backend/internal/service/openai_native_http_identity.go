package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

type nativeCodexIdentityContextKey struct{}
type nativeCodexRequestIdentity struct {
	userAgent    string
	originalBody []byte
}

var nativeCodexHTTPHeaders = map[string]bool{"session-id": true, "thread-id": true, "x-client-request-id": true}

func validNativeCodexUserAgent(ua string) bool {
	if len(ua) > 512 || !(strings.HasPrefix(ua, "codex_cli_rs/") || strings.HasPrefix(ua, "us003_outer_tool_relay/")) {
		return false
	}
	for _, c := range ua {
		if c < 32 || c > 126 {
			return false
		}
	}
	version := strings.SplitN(strings.SplitN(ua, "/", 2)[1], " ", 2)[0]
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
	req.Header.Set("originator", strings.SplitN(identity.userAgent, "/", 2)[0])
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
	rebuilt, err := marshalNativeCodexHTTPBody(identity.originalBody, body)
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(rebuilt))
	req.ContentLength = int64(len(rebuilt))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(rebuilt)), nil }
	*req = *req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileNativeCodex))
	return nil
}

// Keep unchanged raw field values (including large native input/tool histories),
// and emit the official HTTP top-level order observed on pinned0.163. Identity
// metadata remains scoped and changed gateway fields keep their final value.
func marshalNativeCodexHTTPBody(original, body []byte) ([]byte, error) {
	var final map[string]json.RawMessage
	var input map[string]json.RawMessage
	if err := json.Unmarshal(body, &final); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(original, &input)
	for key, value := range final {
		prior, ok := input[key]
		if !ok {
			continue
		}
		var a, b any
		if json.Unmarshal(prior, &a) == nil && json.Unmarshal(value, &b) == nil && reflect.DeepEqual(a, b) {
			final[key] = prior
		}
	}
	order := []string{"model", "stream", "input", "instructions", "tools", "tool_choice", "parallel_tool_calls", "reasoning", "store", "include", "prompt_cache_key", "text", "client_metadata"}
	known := map[string]bool{}
	for _, key := range order {
		known[key] = true
	}
	extra := []string{}
	for key := range final {
		if !known[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	order = append(order, extra...)
	var result bytes.Buffer
	result.WriteByte('{')
	first := true
	for _, key := range order {
		value, ok := final[key]
		if !ok {
			continue
		}
		if !first {
			result.WriteByte(',')
		}
		first = false
		name, _ := json.Marshal(key)
		result.Write(name)
		result.WriteByte(':')
		result.Write(value)
	}
	result.WriteByte('}')
	return result.Bytes(), nil
}
