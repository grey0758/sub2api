package service

import (
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

func TestBuildOpenAILegacyCompactNativeV2Body(t *testing.T) {
	for _, tt := range []struct {
		name       string
		input      string
		wantItems  int
		wantString string
	}{
		{name: "string input", input: `"hello"`, wantItems: 2, wantString: "hello"},
		{name: "array input", input: `[{"type":"message","role":"user","content":"hello"}]`, wantItems: 2, wantString: "hello"},
		{name: "object input", input: `{"type":"message","role":"user","content":"hello"}`, wantItems: 2, wantString: "hello"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := buildOpenAILegacyCompactNativeV2Body([]byte(`{"model":"gpt-5.5","input":` + tt.input + `}`))
			require.NoError(t, err)
			require.True(t, gjson.GetBytes(body, "stream").Bool())
			require.False(t, gjson.GetBytes(body, "store").Bool())
			require.Equal(t, tt.wantItems, len(gjson.GetBytes(body, "input").Array()))
			require.Equal(t, "compaction_trigger", gjson.GetBytes(body, "input.1.type").String())
			require.Equal(t, tt.wantString, gjson.GetBytes(body, "input.0.content").String())
		})
	}
}

func TestOpenAIGatewayServiceForwardLegacyCompactUsesNativeV2ForOAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid-compact"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"summary\"}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-compact\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n")),
	}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
	account := &Account{
		ID: 7, Name: "openai-oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Concurrency: 1, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.5","instructions":"compact","input":"hello"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
	require.Equal(t, "chatgpt.com", upstream.lastReq.Host)
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.Equal(t, "compaction_trigger", gjson.GetBytes(upstream.lastBody, "input.1.type").String())
	require.Contains(t, upstream.lastReq.Header.Get("x-codex-beta-features"), "remote_compaction_v2")
	require.Equal(t, "application/json; charset=utf-8", recorder.Header().Get("Content-Type"))
	require.Equal(t, "resp-compact", gjson.Get(recorder.Body.String(), "id").String())
	require.Equal(t, "compaction", gjson.Get(recorder.Body.String(), "output.0.type").String())
}
