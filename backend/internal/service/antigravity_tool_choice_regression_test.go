package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAntigravityCompatToolChoiceReachesGemini(t *testing.T) {
	for _, protocol := range []string{"chat", "responses"} {
		for _, tc := range []struct {
			name, choice, mode string
			tools              bool
		}{
			{"omitted", "", "NONE", false}, {"empty", "", "NONE", false},
			{"none", "none", "NONE", true}, {"auto", "auto", "VALIDATED", true},
			{"default", "", "VALIDATED", true}, {"required", "required", "ANY", true},
			{"named", "named", "ANY", true},
		} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				body := map[string]any{"model": "gemini-3.1-pro-high", "stream": true}
				if protocol == "chat" {
					body["messages"] = []any{map[string]any{"role": "system", "content": "Agent history may discuss tools; use only declared tools."}, map[string]any{"role": "user", "content": "hi"}, map[string]any{"role": "assistant", "content": "I can calculate the answer."}, map[string]any{"role": "user", "content": "Answer 11+12."}}
				} else {
					body["input"] = "Answer 11+12."
				}
				if tc.name == "empty" {
					body["tools"] = []any{}
				}
				if tc.tools {
					function := map[string]any{"name": "calculate", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}
					tool := map[string]any{"type": "function", "name": function["name"], "parameters": function["parameters"]}
					if protocol == "chat" {
						tool = map[string]any{"type": "function", "function": function}
					}
					body["tools"] = []any{tool}
				}
				if tc.choice == "named" {
					if protocol == "chat" {
						body["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "calculate"}}
					} else {
						body["tool_choice"] = map[string]any{"type": "function", "name": "calculate"}
					}
				} else if tc.choice != "" {
					body["tool_choice"] = tc.choice
				}
				raw, err := json.Marshal(body)
				require.NoError(t, err)
				upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{antigravityCompatSuccessResponse()}}
				svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
				c, rec := newAntigravityCompatContext("POST", "/", raw)
				if protocol == "chat" {
					_, err = svc.ForwardAsChatCompletions(context.Background(), c, newAntigravityCompatAccount(AccountTypeOAuth), raw, nil)
				} else {
					_, err = svc.ForwardAsResponses(context.Background(), c, newAntigravityCompatAccount(AccountTypeOAuth), raw, nil)
				}
				require.NoError(t, err)
				require.Equal(t, 200, rec.Code)
				require.Equal(t, 1, upstream.callCount)
				sent := upstream.requestBodies[0]
				require.Equal(t, tc.mode, gjson.GetBytes(sent, "request.toolConfig.functionCallingConfig.mode").String())
				if tc.name == "named" {
					require.Equal(t, "calculate", gjson.GetBytes(sent, "request.toolConfig.functionCallingConfig.allowedFunctionNames.0").String())
				}
				require.Equal(t, tc.tools && tc.name != "none", gjson.GetBytes(sent, "request.tools").Exists())
			})
		}
	}
}

const malformedFunctionCallSSE = `data: {"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"PRIVATE_SIGNATURE_MUST_NOT_ESCAPE"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":9,"thoughtsTokenCount":13}}}` + "\n\n"

func TestAntigravityCompatMalformedFunctionCallDoesNotCompleteOrRetry(t *testing.T) {
	for _, protocol := range []string{"chat", "responses"} {
		for _, phase := range []string{"before_write", "after_ping", "after_text", "nonstream"} {
			t.Run(protocol+"/"+phase, func(t *testing.T) {
				svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, nil)
				c, rec := newAntigravityCompatContext("POST", "/", nil)
				raw := malformedFunctionCallSSE
				if phase == "after_text" {
					raw = `data: {"response":{"candidates":[{"content":{"parts":[{"text":"partial answer"}]}}]}}` + "\n\n" + raw
				}
				resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw))}
				var err error
				if phase == "nonstream" {
					if protocol == "chat" {
						_, err = svc.handleChatCompletionsNonStreamingFromAntigravity(c, resp, time.Now(), "gemini-4-argon-high")
					} else {
						_, err = svc.handleResponsesNonStreamingFromAntigravity(c, resp, time.Now(), "gemini-4-argon-high")
					}
				} else {
					var adapter antigravityCompatStreamAdapter = newAntigravityChatStreamAdapter("gemini-4-argon-high", true)
					if protocol == "responses" {
						adapter = newAntigravityResponsesStreamAdapter("gemini-4-argon-high")
					}
					if phase == "after_ping" {
						reader, writer := io.Pipe()
						defer reader.Close()
						resp.Body = reader
						go func() { defer writer.Close(); time.Sleep(30 * time.Millisecond); fmt.Fprint(writer, raw) }()
						_, err = svc.handleAntigravityCompatStreamWithKeepaliveInterval(c, resp, time.Now(), "gemini-4-argon-high", adapter, "test", 5*time.Millisecond, time.Second)
					} else {
						_, err = svc.handleAntigravityCompatStream(c, resp, time.Now(), "gemini-4-argon-high", adapter, "test")
					}
				}
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.NotErrorAs(t, err, &failover)
				require.Contains(t, rec.Body.String(), "MALFORMED_FUNCTION_CALL")
				for _, forbidden := range []string{"PRIVATE_SIGNATURE_MUST_NOT_ESCAPE", "empty_stream", "response.completed", "[DONE]"} {
					require.NotContains(t, rec.Body.String(), forbidden)
				}
				if phase == "before_write" || phase == "nonstream" {
					require.Equal(t, 502, rec.Code)
				} else {
					require.Equal(t, 200, rec.Code)
				}
			})
		}
	}
}
