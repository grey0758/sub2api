package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func krillResponsesCompatibilityEnabled(cfg *config.Config, apiKey *service.APIKey) bool {
	return cfg != nil && cfg.Gateway.KrillCompatibilityGroupID > 0 && apiKey != nil &&
		apiKey.GroupID != nil && *apiKey.GroupID == cfg.Gateway.KrillCompatibilityGroupID
}

// Only the validation input is projected. The original synthetic goal and its
// metadata are part of the native Codex request sent to Krill.
func krillGoalValidationBody(body []byte) []byte {
	if !bytes.Contains(body, []byte("goal_objective")) ||
		!strings.HasPrefix(gjson.GetBytes(body, "model").String(), "gpt-") {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() || !hasUniqueJSONMembers(body) {
		return body
	}

	var candidates []int
	for index, item := range input.Array() {
		kind := item.Get("type").String()
		callID := item.Get("call_id")
		if isNativeCodexGoalContext(item) {
			candidates = append(candidates, index)
			continue
		}
		if kind == "item_reference" {
			id := item.Get("id")
			if id.Type != gjson.String || strings.TrimSpace(id.String()) == "" {
				return body
			}
		}
		if strings.HasSuffix(kind, "_call") || strings.HasSuffix(kind, "_call_output") ||
			kind == "tool_call" || kind == "tool_search_output" {
			if callID.Type != gjson.String || strings.TrimSpace(callID.String()) == "" {
				return body
			}
		}
	}
	if len(candidates) == 0 {
		return body
	}
	projection := body
	for i := len(candidates) - 1; i >= 0; i-- {
		next, err := sjson.DeleteBytes(projection, "input."+strconv.Itoa(candidates[i]))
		if err != nil {
			return body
		}
		projection = next
	}
	coverage := service.AnalyzeToolCallOutputContextCoverageBytes(projection)
	if coverage.HasFunctionCallOutput && !coverage.ContextCoversAllCallIDs {
		return body
	}
	return projection
}

func isNativeCodexGoalContext(item gjson.Result) bool {
	if item.Get("type").String() != "function_call_output" || item.Get("namespace").String() != "agent" ||
		item.Get("name").String() != "goal_objective" {
		return false
	}
	output, callID := item.Get("output"), item.Get("call_id")
	return output.Type == gjson.String && strings.TrimSpace(output.String()) != "" &&
		(!callID.Exists() || (callID.Type == gjson.String && strings.TrimSpace(callID.String()) == ""))
}

// Krill's gpt-6.1-sol advertises low as the least reasoning effort it accepts.
func normalizeKrillResponsesEffort(body []byte, account *service.Account) []byte {
	effort := gjson.GetBytes(body, "reasoning.effort")
	if effort.Type != gjson.String || effort.String() != "none" {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	if account == nil || account.Type != service.AccountTypeAPIKey || !account.IsOpenAIPassthroughEnabled() ||
		account.GetMappedModel(model) != "gpt-6.1-sol" || !hasUniqueJSONMembers(body) {
		return body
	}
	updated, err := sjson.SetBytes(body, "reasoning.effort", "low")
	if err != nil {
		return body
	}
	return updated
}

func hasUniqueJSONMembers(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if !consumeUniqueJSONValue(decoder) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func consumeUniqueJSONValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}

	switch delim {
	case '{':
		members := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return false
			}
			key, ok := keyToken.(string)
			if !ok {
				return false
			}
			if _, duplicate := members[key]; duplicate {
				return false
			}
			members[key] = struct{}{}
			if !consumeUniqueJSONValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !consumeUniqueJSONValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}
