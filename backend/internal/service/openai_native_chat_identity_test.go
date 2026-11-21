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

func TestNativeCodexChatCompletionsCarriesLeaseIdentity(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, history := range []bool{false, true} {
			t.Run(stringBool(stream)+"/history_"+stringBool(history), func(t *testing.T) {
				lease := nativeBrokerTestLease()
				nativeBrokerFixture(t, lease)
				messages := `[{"role":"user","content":"Return CHAT_OK"}]`
				if history {
					messages = `[{"role":"user","content":"Use synthetic_echo"},{"role":"assistant","tool_calls":[{"id":"fc_synthetic_chat","type":"function","function":{"name":"synthetic_echo","arguments":"{}"}}]},{"role":"tool","tool_call_id":"fc_synthetic_chat","content":"CHAT_OK"}]`
				}
				body := []byte(`{"model":"gpt-6.2-sol","stream":` + stringBool(stream) + `,"messages":` + messages + `}`)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
				up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"CHAT_OK\",\"output_index\":0,\"content_index\":0}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_chat_identity\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"CHAT_OK\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"))}}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: up, cache: &stubGatewayCache{}}
				account := nativeBrokerTestAccount()
				result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "synthetic-chat-cache", "")
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, 200, rec.Code)
				require.Contains(t, rec.Body.String(), "CHAT_OK")
				require.Equal(t, lease["user_agent"], up.lastReq.Header.Get("User-Agent"))
				require.Equal(t, "codex_cli_rs", up.lastReq.Header.Get("originator"))
				require.Empty(t, up.lastReq.Header.Get("session_id"))
				require.Equal(t, HTTPUpstreamProfileNativeCodex, HTTPUpstreamProfileFromContext(up.lastReq.Context()))
				require.Equal(t, "gpt-6.2-sol", gjson.GetBytes(up.lastBody, "model").String())
				require.True(t, gjson.GetBytes(up.lastBody, "stream").Bool())
				if history {
					require.Equal(t, "fc_synthetic_chat", gjson.GetBytes(up.lastBody, `input.#(type=="function_call").call_id`).String())
					require.Equal(t, "fc_synthetic_chat", gjson.GetBytes(up.lastBody, `input.#(type=="function_call_output").call_id`).String())
				}
				require.NotContains(t, account.Credentials, "access_token")
				require.NotContains(t, account.Credentials, "refresh_token")
			})
		}
	}
}

func stringBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
