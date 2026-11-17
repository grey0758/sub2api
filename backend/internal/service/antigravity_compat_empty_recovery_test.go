package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise the full forwarder: a graceful empty EOF after comments must not
// bypass the single recovery budget, or turn a second failure into completed.
func TestAntigravityCompatEmptyTextRecovery(t *testing.T) {
	for _, protocol := range []string{"chat", "responses"} {
		for _, scenario := range []string{"empty", "after_ping", "usage_only", "signature_only", "second_empty", "budget_used", "expired", "cancelled", "tools", "tool_history", "thinking", "tool_output", "media", "safety", "max_tokens"} {
			t.Run(protocol+"/"+scenario, func(t *testing.T) {
				body := map[string]any{"model": "gemini-3.8-flash-low", "stream": true, "tools": []any{}}
				items := []any{map[string]any{"role": "user", "content": "Return a short text answer."}}
				field := "input"
				if protocol == "chat" {
					field = "messages"
				}
				if scenario == "tools" {
					function := map[string]any{"name": "fixture", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}
					if protocol == "chat" {
						body["tools"] = []any{map[string]any{"type": "function", "function": function}}
					} else {
						function["type"] = "function"
						body["tools"] = []any{function}
					}
					body["tool_choice"] = "none"
				}
				if scenario == "tool_history" {
					if protocol == "chat" {
						items = append(items, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "old", "type": "function", "function": map[string]any{"name": "fixture", "arguments": "{}"}}}}, map[string]any{"role": "tool", "tool_call_id": "old", "content": "1"})
					} else {
						items = append(items, map[string]any{"type": "function_call", "call_id": "old", "name": "fixture", "arguments": "{}"}, map[string]any{"type": "function_call_output", "call_id": "old", "output": "1"})
					}
				}
				body[field] = items
				raw, err := json.Marshal(body)
				require.NoError(t, err)
				first := ""
				switch scenario {
				case "usage_only":
					first = `data: {"response":{"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2}}}` + "\n\n"
				case "signature_only":
					first = `data: {"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"PRIVATE_EMPTY_SIGNATURE"}]},"finishReason":"STOP"}]}}` + "\n\n"
				case "thinking":
					first = `data: {"response":{"candidates":[{"content":{"parts":[{"thought":true,"text":"partial reasoning"}]}}]}}` + "\n\n"
				case "tool_output":
					first = `data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"fixture","args":{}}}]}}]}}` + "\n\n"
				case "media":
					first = `data: {"response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":""}}]}}]}}` + "\n\n"
				case "safety":
					first = `data: {"response":{"promptFeedback":{"blockReason":"SAFETY"},"candidates":[{"finishReason":"SAFETY"}]}}` + "\n\n"
				case "max_tokens":
					first = `data: {"response":{"candidates":[{"finishReason":"MAX_TOKENS"}]}}` + "\n\n"
				}
				response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(first))}
				next := antigravityCompatSuccessResponse()
				if scenario == "second_empty" {
					next = &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
				}
				upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{response, next}}
				svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
				c, rec := newAntigravityCompatContext("POST", "/", raw)
				if scenario == "after_ping" || scenario == "second_empty" {
					_, err = c.Writer.Write([]byte(": ping\n\n"))
					require.NoError(t, err)
					c.Writer.Flush()
					c.Set(antigravityCompatHeaderHeartbeatBytesKey, 8)
				}
				if scenario == "budget_used" || scenario == "expired" {
					started := time.Now()
					if scenario == "expired" {
						started = started.Add(-181 * time.Second)
					}
					c.Set(antigravityCompatTextRecoveryKey, &antigravityCompatTextRecovery{allowed: true, used: scenario == "budget_used", started: started})
				}
				ctx := context.Background()
				if scenario == "cancelled" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					c.Request = c.Request.WithContext(ctx)
					upstream.onCall = func(_ *http.Request, _ *queuedHTTPUpstreamStub) { cancel() }
				}
				var result *ForwardResult
				if protocol == "chat" {
					result, err = svc.ForwardAsChatCompletions(ctx, c, newAntigravityCompatAccount(AccountTypeOAuth), raw, nil)
				} else {
					result, err = svc.ForwardAsResponses(ctx, c, newAntigravityCompatAccount(AccountTypeOAuth), raw, nil)
				}
				recoverable := scenario == "empty" || scenario == "after_ping" || scenario == "usage_only" || scenario == "signature_only"
				attempts := 1
				if recoverable || scenario == "second_empty" {
					attempts = 2
				}
				require.Equal(t, attempts, upstream.callCount)
				if recoverable {
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, 3, result.Usage.OutputTokens)
					require.Contains(t, rec.Body.String(), "ok")
					require.NotContains(t, rec.Body.String(), "empty_stream")
					if protocol == "chat" {
						require.Equal(t, 1, strings.Count(rec.Body.String(), "[DONE]"))
					} else {
						require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.completed"))
					}
					a, b := upstream.requestBodies[0], upstream.requestBodies[1]
					for _, key := range []string{"model", "request.contents", "request.generationConfig", "request.toolConfig"} {
						require.JSONEq(t, gjson.GetBytes(a, key).Raw, gjson.GetBytes(b, key).Raw)
					}
					require.NotEqual(t, gjson.GetBytes(a, "requestId").String(), gjson.GetBytes(b, "requestId").String())
					events, ok := c.Get(OpsUpstreamErrorsKey)
					require.True(t, ok)
					encoded, _ := json.Marshal(events)
					require.Contains(t, string(encoded), "empty_stream")
					require.NotContains(t, string(encoded), "malformed_function_call")
				} else if scenario == "second_empty" {
					require.Error(t, err)
					require.Nil(t, result)
					require.Contains(t, rec.Body.String(), "error")
					require.NotContains(t, rec.Body.String(), "response.completed")
					require.NotContains(t, rec.Body.String(), "[DONE]")
				}
				require.NotContains(t, rec.Body.String(), "PRIVATE_EMPTY_SIGNATURE")
			})
		}
	}
}
