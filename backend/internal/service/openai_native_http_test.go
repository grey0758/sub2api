//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNativeCodexBrokerHTTPResponsesWithFullToolHistory(t *testing.T) {
	lease := nativeBrokerTestLease()
	nativeBrokerFixture(t, lease)
	account := nativeBrokerTestAccount()
	account.Concurrency = 1
	account.Status = StatusActive
	account.Schedulable = true
	account.Extra["openai_oauth_responses_websockets_v2_mode"] = "passthrough"
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	require.Equal(t, OpenAIUpstreamTransportHTTPSSE, NewOpenAIWSProtocolResolver(cfg).Resolve(account).Transport)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_synthetic_http\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"HTTP_OK\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"))}}
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{}, toolCorrector: NewCodexToolCorrector()}
	body := []byte(`{"model":"gpt-6.1-sol","stream":false,"instructions":"Answer the tool result","previous_response_id":"resp_http_not_reused","input":[{"role":"user","content":"Use trial_echo"},{"type":"function_call","call_id":"call_synthetic_http","name":"trial_echo","arguments":"{}"},{"type":"function_call_output","call_id":"call_synthetic_http","output":"HTTP_OK"}],"tools":[{"type":"function","name":"trial_echo","parameters":{"type":"object","properties":{}}}],"tool_choice":"none"}`)
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 200, rec.Code)
	require.True(t, upstream.lastReq.Header.Get("Authorization") == "Bearer "+lease["access_token"].(string))
	require.Equal(t, "account-synthetic", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", upstream.lastReq.URL.String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "previous_response_id").Exists())
	require.Equal(t, int64(3), gjson.GetBytes(upstream.lastBody, "input.#").Int())
	require.Equal(t, "HTTP_OK", gjson.GetBytes(upstream.lastBody, "input.2.output").String())
	require.NotContains(t, account.Credentials, "access_token")
	require.NotContains(t, account.Credentials, "refresh_token")
}

func TestNativeCodexLitePreservesBodyAndIdentityRelationships(t *testing.T) {
	lease := nativeBrokerTestLease()
	nativeBrokerFixture(t, lease)
	account := nativeBrokerTestAccount()
	up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: up, cache: &stubGatewayCache{}, toolCorrector: NewCodexToolCorrector()}
	body := []byte(`{"model":"gpt-6.1-sol","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"Keep native guidance"}]},{"role":"user","content":[{"type":"input_text","text":"Hello"}]}],"stream":true,"store":false,"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"reasoning":{"effort":"xhigh","context":"all_turns"},"text":{"verbosity":"low"},"prompt_cache_key":"native-session","client_metadata":{"session_id":"native-session","thread_id":"native-session","turn_id":"native-turn","root_turn_id":"native-turn","x-codex-window-id":"native-window","x-codex-turn-metadata":"{\"thread_id\":\"native-session\",\"turn_id\":\"native-turn\"}"}}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set(responsesLiteHeader, "true")
	c.Request.Header.Set("session-id", "native-session")
	c.Request.Header.Set("thread-id", "native-session")
	c.Request.Header.Set("x-client-request-id", "native-turn")
	_, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(up.lastBody, "instructions").Exists())
	require.Equal(t, gjson.GetBytes(body, "input").Value(), gjson.GetBytes(up.lastBody, "input").Value())
	cm := gjson.GetBytes(up.lastBody, "client_metadata")
	require.NotEqual(t, "native-session", cm.Get("session_id").String())
	require.Equal(t, cm.Get("session_id").String(), cm.Get("thread_id").String())
	require.Equal(t, cm.Get("session_id").String(), gjson.GetBytes(up.lastBody, "prompt_cache_key").String())
	require.Equal(t, cm.Get("turn_id").String(), cm.Get("root_turn_id").String())
	require.Equal(t, cm.Get("session_id").String(), up.lastReq.Header.Get("session-id"))
	require.Equal(t, cm.Get("thread_id").String(), up.lastReq.Header.Get("thread-id"))
	require.Equal(t, cm.Get("turn_id").String(), up.lastReq.Header.Get("x-client-request-id"))
	require.Equal(t, lease["user_agent"], up.lastReq.Header.Get("User-Agent"))
	for _, key := range []string{"version", "session_id", "conversation_id", openAICodexRoutingHintHeader} {
		require.Empty(t, up.lastReq.Header.Get(key), key)
	}
	require.Equal(t, HTTPUpstreamProfileNativeCodex, HTTPUpstreamProfileFromContext(up.lastReq.Context()))
	require.NotEqual(t, scopeCodexAccountIdentityValue(account, 1, "session", "native-session"), scopeCodexAccountIdentityValue(account, 2, "thread", "native-session"))
	other := *account
	other.Credentials = map[string]any{"chatgpt_account_id": "other"}
	require.NotEqual(t, scopeCodexAccountIdentityValue(account, 1, "session", "native-session"), scopeCodexAccountIdentityValue(&other, 1, "session", "native-session"))
}
