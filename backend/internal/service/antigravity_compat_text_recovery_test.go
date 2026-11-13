package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAntigravityCompatTextRecovery(t *testing.T) {
	for _, protocol := range []string{"chat", "responses"} {
		for _, scenario := range []string{"stream", "nonstream", "after_ping", "slow_first_attempt", "expired_first_attempt", "exhausted", "after_text", "after_thinking", "tool_declared", "tool_declared_none", "tool_history", "tool_in_error", "media_in_error", "budget_used", "cancelled", "cancel_after_attempt", "retry_http_error", "retry_empty"} {
			t.Run(protocol+"/"+scenario, func(t *testing.T) {
				stream := scenario != "nonstream"
				rawBody := map[string]any{"model": "gemini-3.1-pro-high", "stream": stream, "tools": []any{}}
				msgs := []any{map[string]any{"role": "user", "content": "Give the answer directly."}}
				if scenario == "tool_history" {
					if protocol == "chat" {
						msgs = []any{map[string]any{"role": "user", "content": "Use the old result."}, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call_fixture", "type": "function", "function": map[string]any{"name": "sum", "arguments": "{}"}}}}, map[string]any{"role": "tool", "tool_call_id": "call_fixture", "content": "23"}}
					} else {
						msgs = append(msgs, map[string]any{"type": "function_call", "call_id": "call_fixture", "name": "sum", "arguments": "{}"}, map[string]any{"type": "function_call_output", "call_id": "call_fixture", "output": "23"})
					}
				}
				if protocol == "chat" {
					rawBody["messages"] = msgs
				} else {
					rawBody["input"] = msgs
				}
				if scenario == "tool_declared" || scenario == "tool_declared_none" {
					if scenario == "tool_declared_none" {
						rawBody["tool_choice"] = "none"
					}
					tool := map[string]any{"name": "sum", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}
					if protocol == "chat" {
						rawBody["tools"] = []any{map[string]any{"type": "function", "function": tool}}
					} else {
						tool["type"] = "function"
						rawBody["tools"] = []any{tool}
					}
				}
				raw, err := json.Marshal(rawBody)
				require.NoError(t, err)
				first := malformedFunctionCallSSE
				if scenario == "after_text" {
					first = `data: {"response":{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}}` + "\n\n" + first
				}
				if scenario == "after_thinking" {
					first = `data: {"response":{"candidates":[{"content":{"parts":[{"thought":true,"text":"partial reasoning"}]}}]}}` + "\n\n" + first
				}
				if scenario == "tool_in_error" {
					first = `data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"sum","args":{}}}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}` + "\n\n"
				}
				if scenario == "media_in_error" {
					first = `data: {"response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":""}}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}` + "\n\n"
				}
				response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(first))}
				next := antigravityCompatSuccessResponse()
				if scenario == "exhausted" {
					next = &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(malformedFunctionCallSSE))}
				}
				if scenario == "retry_http_error" {
					next = &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"fixture invalid","status":"INVALID_ARGUMENT"}}`))}
				}
				if scenario == "retry_empty" {
					next = &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
				}
				upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{response, next}}
				svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
				c, rec := newAntigravityCompatContext("POST", "/", raw)
				if scenario == "after_ping" || scenario == "retry_http_error" || scenario == "retry_empty" {
					c.Writer.Write([]byte(": ping\n\n"))
					c.Writer.Flush()
				}
				if scenario == "budget_used" {
					c.Set(antigravityCompatTextRecoveryKey, &antigravityCompatTextRecovery{allowed: true, used: true, started: time.Now()})
				}
				ctx := context.Background()
				if scenario == "cancelled" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
					c.Request = c.Request.WithContext(ctx)
				}
				if scenario == "cancel_after_attempt" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					defer cancel()
					c.Request = c.Request.WithContext(ctx)
					upstream.onCall = func(_ *http.Request, _ *queuedHTTPUpstreamStub) { cancel() }
				}
				if scenario == "slow_first_attempt" || scenario == "expired_first_attempt" {
					upstream.onCall = func(_ *http.Request, _ *queuedHTTPUpstreamStub) {
						r := antigravityCompatRecoveryFromContext(c)
						require.NotNil(t, r)
						if scenario == "slow_first_attempt" {
							r.started = time.Now().Add(-96 * time.Second)
						} else {
							r.started = time.Now().Add(-181 * time.Second)
						}
					}
				}
				var result *ForwardResult
				if protocol == "chat" {
					result, err = svc.ForwardAsChatCompletions(ctx, c, newAntigravityCompatAccount(AccountTypeOAuth), raw, nil)
				} else {
					result, err = svc.ForwardAsResponses(ctx, c, newAntigravityCompatAccount(AccountTypeOAuth), raw, nil)
				}
				recoverable := scenario == "stream" || scenario == "nonstream" || scenario == "after_ping" || scenario == "slow_first_attempt"
				attempts := 1
				if recoverable || scenario == "exhausted" || scenario == "retry_http_error" || scenario == "retry_empty" {
					attempts = 2
				}
				if scenario == "cancelled" {
					attempts = 0
				}
				require.Equal(t, attempts, upstream.callCount)
				if recoverable {
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, 3, result.Usage.OutputTokens, "only the successful generation is billed")
					require.NotContains(t, rec.Body.String(), "MALFORMED_FUNCTION_CALL")
					require.Contains(t, rec.Body.String(), "ok")
					if protocol == "chat" && stream {
						require.Equal(t, 1, strings.Count(rec.Body.String(), "[DONE]"))
					}
					if protocol == "responses" && stream {
						require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.completed"))
					}
				} else {
					require.Error(t, err)
					require.Nil(t, result)
					require.NotContains(t, rec.Body.String(), "[DONE]")
					require.NotContains(t, rec.Body.String(), "response.completed")
				}
				require.NotContains(t, rec.Body.String(), "PRIVATE_SIGNATURE_MUST_NOT_ESCAPE")
				if attempts == 2 {
					a, b := upstream.requestBodies[0], upstream.requestBodies[1]
					for _, key := range []string{"model", "project", "request.contents", "request.generationConfig", "request.toolConfig"} {
						require.JSONEq(t, gjson.GetBytes(a, key).Raw, gjson.GetBytes(b, key).Raw)
					}
					require.NotEqual(t, gjson.GetBytes(a, "requestId").String(), gjson.GetBytes(b, "requestId").String())
					require.Contains(t, gjson.GetBytes(b, "request.systemInstruction").Raw, antigravityCompatTextRecoveryInstruction)
					events, ok := c.Get(OpsUpstreamErrorsKey)
					require.True(t, ok)
					encoded, _ := json.Marshal(events)
					require.Contains(t, string(encoded), "malformed_function_call")
					require.NotContains(t, string(encoded), "PRIVATE_SIGNATURE_MUST_NOT_ESCAPE")
				}
				if scenario == "retry_http_error" || scenario == "retry_empty" {
					require.Equal(t, 200, rec.Code)
					if protocol == "responses" {
						require.Contains(t, rec.Body.String(), "event: error\n")
					} else {
						require.Contains(t, rec.Body.String(), "data: {\"error\"")
					}
					require.NotContains(t, rec.Body.String(), "{\"error\":{\"code\"")
				}
			})
		}
	}
}
func TestAntigravityCompatTextRecoveryBounds(t *testing.T) {
	c, _ := newAntigravityCompatContext("POST", "/", nil)
	r := &antigravityCompatTextRecovery{allowed: true, started: time.Now()}
	c.Set(antigravityCompatTextRecoveryKey, r)
	require.True(t, antigravityCompatCanRecoverText(c))
	r.used = true
	require.False(t, antigravityCompatCanRecoverText(c))
	r.used = false
	r.started = time.Now().Add(-96 * time.Second)
	require.True(t, antigravityCompatCanRecoverText(c))
	r.started = time.Now().Add(-179 * time.Second)
	require.True(t, antigravityCompatCanRecoverText(c))
	r.started = time.Now().Add(-181 * time.Second)
	require.False(t, antigravityCompatCanRecoverText(c))
	for _, field := range []string{"functionCall", "inlineData", "fileData", "executableCode", "unknown_future_payload"} {
		require.True(t, antigravityCompatPartsHaveOutput([]map[string]any{{field: map[string]any{}}}))
	}
	require.False(t, antigravityCompatPartsHaveOutput([]map[string]any{{"thoughtSignature": "private", "text": ""}}))
	require.False(t, antigravityCompatTextOnly([]byte(`{"model":"gemini-4-argon","request":{"toolConfig":{"functionCallingConfig":{"mode":"NONE"}},"tools":[{"googleSearch":{}}]}}`)))
	_, err := antigravityCompatTextRecoveryBody([]byte(`{}`))
	require.Error(t, err)
	require.True(t, isAntigravityMalformedError(errors.New("upstream MALFORMED_FUNCTION_CALL")))
}
