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
	"github.com/tidwall/sjson"
)

type nativeCodexIdentityContextKey struct{}
type nativeCodexRequestIdentity struct {
	userAgent    string
	originalBody []byte
}

var nativeCodexHTTPHeaders = map[string]bool{"session-id": true, "thread-id": true, "x-client-request-id": true}

// Some intermediate API gateways omit the Lite and session headers. Pinned
// Codex still carries its complete developer context and matching IDs in body.
// Recognize that shape only on the controlled native account; ordinary API
// requests keep their existing compatibility handling.
func isNativeCodexLitePayload(body []byte) bool {
	if gjson.GetBytes(body, "instructions").Exists() {
		return false
	}
	cm := gjson.GetBytes(body, "client_metadata")
	for _, key := range []string{"session_id", "thread_id", "turn_id"} {
		if cm.Get(key).String() == "" {
			return false
		}
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		role := item.Get("role").String()
		content := item.Get("content")
		if (role == "developer" || role == "system") && ((content.IsArray() && len(content.Array()) > 0) || strings.TrimSpace(content.String()) != "") {
			return true
		}
	}
	return false
}

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
	// Rebuild before deriving headers so the embedded JSON uses the same
	// serialization in the header and body after identity scoping.
	rebuilt, err := marshalNativeCodexHTTPBody(identity.originalBody, body)
	if err != nil {
		return err
	}
	cm := gjson.GetBytes(rebuilt, "client_metadata")
	for header, field := range map[string]string{"session-id": "session_id", "thread-id": "thread_id"} {
		if value := cm.Get(field).String(); value != "" {
			req.Header.Set(header, value)
		}
	}
	for header, field := range map[string]string{"x-codex-window-id": "x-codex-window-id", "x-codex-turn-metadata": "x-codex-turn-metadata"} {
		if value := cm.Get(field).String(); value != "" {
			req.Header.Set(header, value)
		}
	}
	if req.Header.Get("x-client-request-id") == "" {
		if value := cm.Get("turn_id").String(); value != "" {
			req.Header.Set("x-client-request-id", value)
		}
	}
	if isNativeCodexLitePayload(identity.originalBody) {
		req.Header.Set(responsesLiteHeader, "true")
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
		} else if key == "client_metadata" {
			rebuilt, err := preserveNativeJSONMemberOrder(prior, value)
			if err != nil {
				return nil, err
			}
			final[key] = rebuilt
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

// Replace only changed identity values while retaining the client's object
// member order and unchanged raw values, including embedded turn metadata.
func preserveNativeJSONMemberOrder(original, final []byte) ([]byte, error) {
	prior := gjson.ParseBytes(original)
	current := gjson.ParseBytes(final)
	if !prior.IsObject() || !current.IsObject() {
		return final, nil
	}
	result := append([]byte(nil), original...)
	var failure error
	prior.ForEach(func(key, old gjson.Result) bool {
		path := gjson.Escape(key.String())
		value := current.Get(path)
		if !value.Exists() {
			result, failure = sjson.DeleteBytes(result, path)
			return failure == nil
		}
		var a, b any
		if json.Unmarshal([]byte(old.Raw), &a) == nil && json.Unmarshal([]byte(value.Raw), &b) == nil && reflect.DeepEqual(a, b) {
			return true
		}
		raw := []byte(value.Raw)
		if old.IsObject() && value.IsObject() {
			raw, failure = preserveNativeJSONMemberOrder([]byte(old.Raw), raw)
		} else if key.String() == "x-codex-turn-metadata" && old.Type == gjson.String && value.Type == gjson.String {
			var embedded []byte
			embedded, failure = preserveNativeJSONMemberOrder([]byte(old.String()), []byte(value.String()))
			if failure == nil {
				raw, failure = json.Marshal(string(embedded))
			}
		}
		if failure == nil {
			result, failure = sjson.SetRawBytes(result, path, raw)
		}
		return failure == nil
	})
	if failure != nil {
		return nil, failure
	}
	current.ForEach(func(key, value gjson.Result) bool {
		path := gjson.Escape(key.String())
		if !prior.Get(path).Exists() {
			result, failure = sjson.SetRawBytes(result, path, []byte(value.Raw))
		}
		return failure == nil
	})
	return result, failure
}
