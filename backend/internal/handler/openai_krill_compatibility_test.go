package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

func TestKrillGoalValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const goal = `{"type":"function_call_output","namespace":"agent","name":"goal_objective","id":"fco_synthetic_goal","output":"Synthetic goal","internal_chat_message_metadata_passthrough":{"turn_id":"synthetic"}}`
	const ordinary = `{"type":"function_call_output","name":"example","output":"Synthetic tool output"}`
	const matchedCall = `{"type":"function_call","name":"example","call_id":"call_synthetic","arguments":"{}"}`
	const matchedOutput = `{"type":"function_call_output","call_id":"call_synthetic","output":"Synthetic tool output"}`
	for _, tc := range []struct {
		name  string
		input string
		group int64
		valid bool
	}{
		{"exact goal", goal, 8, true},
		{"goal and correlated history", goal + "," + matchedCall + "," + matchedOutput, 8, true},
		{"goal and unmatched history", goal + "," + ordinary, 8, false},
		{"goal with an unmatched call ID", goal + "," + matchedOutput, 8, false},
		{"unrelated group", goal, 9, false},
		{"ordinary missing call ID", ordinary, 8, false},
		{"duplicate member", `{"type":"function_call_output","namespace":"agent","name":"goal_objective","name":"other","output":"Synthetic goal"}`, 8, false},
		{"null call ID", `{"type":"function_call_output","namespace":"agent","name":"goal_objective","output":"Synthetic goal","call_id":null}`, 8, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.KrillCompatibilityGroupID = 8
			key := &service.APIKey{GroupID: &tc.group}
			body := []byte(`{"model":"gpt-6.2-sol","input":[` + tc.input + `]}`)
			original := string(body)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			h := &OpenAIGatewayHandler{}
			require.Equal(t, tc.valid, h.validateFunctionCallOutputRequest(c, body, zap.NewNop(), krillResponsesCompatibilityEnabled(cfg, key)))
			if !tc.valid {
				require.Equal(t, http.StatusBadRequest, w.Code)
			}
			require.Equal(t, original, string(body))
		})
	}
}

func TestKrillReasoningEffortCompatibility(t *testing.T) {
	account := &service.Account{
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
		Extra:    map[string]any{"openai_passthrough": true},
	}
	body := []byte(`{ "model":"gpt-6.1-sol", "reasoning":{"effort":"none","summary":"auto"}, "input":[{"role":"user","content":"synthetic"}] }`)
	want, err := sjson.SetBytes(body, "reasoning.effort", "low")
	require.NoError(t, err)
	require.Equal(t, want, normalizeKrillResponsesEffort(body, account))

	for _, candidate := range [][]byte{
		[]byte(`{"model":"gpt-6.1-sol","reasoning":{"effort":"xhigh"}}`),
		[]byte(`{"model":"gpt-6.2-sol","reasoning":{"effort":"none"}}`),
		[]byte(`{"model":"gpt-6.1-sol","reasoning":{}}`),
		[]byte(`{"model":"gpt-6.1-sol","reasoning":{"effort":"none","effort":"high"}}`),
	} {
		require.Equal(t, candidate, normalizeKrillResponsesEffort(candidate, account))
	}
	require.Equal(t, body, normalizeKrillResponsesEffort(body, nil))
	require.Equal(t, "none", gjson.GetBytes(body, "reasoning.effort").String())
}
