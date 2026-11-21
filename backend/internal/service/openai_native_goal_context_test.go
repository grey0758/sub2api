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

func TestNativeCodexGoalContextIsForwardedVerbatim(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(stringBool(passthrough), func(t *testing.T) {
			nativeBrokerFixture(t, nativeBrokerTestLease())
			account := nativeBrokerTestAccount()
			account.Extra["openai_passthrough"] = passthrough
			up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_goal\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: up, cache: &stubGatewayCache{}, toolCorrector: NewCodexToolCorrector()}
			body := []byte(`{"model":"gpt-6.2-sol","instructions":"Synthetic goal check","input":[ {"type":"function_call_output", "id":"fco_synthetic_goal", "namespace":"agent", "name":"goal_objective", "output":"Synthetic goal: GOAL_OK", "internal_chat_message_metadata_passthrough":{"turn_id":"synthetic-turn","create_time":1}}, {"role":"user","content":"continue"} ],"stream":true,"store":false}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
			_, err := svc.Forward(context.Background(), c, account, body)
			require.NoError(t, err)
			require.Equal(t, gjson.GetBytes(body, "input").Raw, gjson.GetBytes(up.lastBody, "input").Raw)
			require.Equal(t, "agent", gjson.GetBytes(up.lastBody, "input.0.namespace").String())
			require.False(t, gjson.GetBytes(up.lastBody, "input.0.call_id").Exists())
		})
	}
}
