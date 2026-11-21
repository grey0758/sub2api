package handler

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

func TestCodexGoalValidationPreservesNativeBodyAndHistory(t *testing.T) {
	goal := `{"type":"function_call_output","name":"goal_objective","namespace":"agent","output":"Synthetic goal: return GOAL_OK","internal_chat_message_metadata_passthrough":{"turn_id":"synthetic-turn","create_time":1}}`
	for _, history := range []string{"", `,{"type":"function_call","call_id":"call_echo","name":"echo","arguments":"{}"},{"type":"function_call_output","call_id":"call_echo","output":"done"}`} {
		body := []byte(`{ "model":"gpt-6.2-sol", "metadata":{"integer":9007199254740993}, "input":[` + goal + history + `,{"role":"user","content":"continue"}], "store":false }`)
		original := append([]byte(nil), body...)
		projection, changed := codexGoalValidationBody(body)
		require.True(t, changed)
		require.Contains(t, string(projection), ` "metadata":{"integer":9007199254740993}, `)
		before, after := gjson.GetBytes(body, "input").Array(), gjson.GetBytes(projection, "input").Array()
		require.Len(t, after, len(before)-1)
		for i := range after {
			require.Equal(t, before[i+1].Raw, after[i].Raw)
		}
		h := &OpenAIGatewayHandler{}
		require.True(t, h.validateFunctionCallOutputRequest(nil, body, zap.NewNop()))
		require.Equal(t, original, body, "native goal must not be rewritten or removed from forwarded bytes")
	}
}

func TestCodexGoalValidationRejectsAmbiguity(t *testing.T) {
	base := `{"type":"function_call_output","name":"goal_objective","namespace":"agent","output":"synthetic goal"}`
	for name, input := range map[string]string{
		"ordinary output":   `{"type":"function_call_output","output":"result"}`,
		"other namespace":   `{"type":"function_call_output","name":"goal_objective","namespace":"other","output":"synthetic goal"}`,
		"real call id":      `{"type":"function_call_output","name":"goal_objective","namespace":"agent","output":"synthetic goal","call_id":"call_real"}`,
		"nonstring call id": `{"type":"function_call_output","name":"goal_objective","namespace":"agent","output":"synthetic goal","call_id":42}`,
		"nonstring output":  `{"type":"function_call_output","name":"goal_objective","namespace":"agent","output":[]}`,
		"empty goal":        `{"type":"function_call_output","name":"goal_objective","namespace":"agent","output":" "}`,
		"duplicate output":  `{"type":"function_call_output","name":"goal_objective","namespace":"agent","output":"a","output":"b"}`,
		"mixed missing id":  base + `,{"type":"function_call_output","output":"ordinary result"}`,
		"unanchored call":   base + `,{"type":"function_call","name":"echo"}`,
		"blank reference":   base + `,{"type":"item_reference","id":""}`,
		"numeric reference": base + `,{"type":"item_reference","id":42}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-6.2-sol","input":[` + input + `]}`)
			got, changed := codexGoalValidationBody(body)
			require.False(t, changed)
			require.Equal(t, body, got)
		})
	}
	body := []byte(`{"model":"other-provider-model","input":[` + base + `]}`)
	got, changed := codexGoalValidationBody(body)
	require.False(t, changed)
	require.Equal(t, body, got)
}
