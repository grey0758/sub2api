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
