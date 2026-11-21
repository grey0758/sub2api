package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// IsCodexGoalContextItem recognizes the synthetic caller-owned context sent by
// Codex 0.171. It is not the result of a function invocation. A fresh native
// HTTP canary accepted this exact namespace/name without a call_id.
func IsCodexGoalContextItem(item gjson.Result) bool {
	if item.Get("type").String() != "function_call_output" ||
		item.Get("namespace").String() != "agent" || item.Get("name").String() != "goal_objective" {
		return false
	}
	output, callID := item.Get("output"), item.Get("call_id")
	return output.Type == gjson.String && strings.TrimSpace(output.String()) != "" &&
		(!callID.Exists() || (callID.Type == gjson.String && strings.TrimSpace(callID.String()) == ""))
}

func isCodexGoalContextMap(item map[string]any) bool {
	if item["type"] != "function_call_output" || item["namespace"] != "agent" || item["name"] != "goal_objective" {
		return false
	}
	output, ok := item["output"].(string)
	if !ok || strings.TrimSpace(output) == "" {
		return false
	}
	callID, exists := item["call_id"]
	if !exists {
		return true
	}
	value, ok := callID.(string)
	return ok && strings.TrimSpace(value) == ""
}
