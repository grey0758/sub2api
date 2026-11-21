package handler

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Make a validation-only projection excluding synthetic goals. Never forward
// this projection: the caller's native context must stay on the actual wire.
func codexGoalValidationBody(body []byte) ([]byte, bool) {
	if !bytes.Contains(body, []byte("goal_objective")) ||
		!strings.HasPrefix(gjson.GetBytes(body, "model").String(), "gpt-") || !hasUniqueJSONMembers(body) {
		return body, false
	}
	previous := gjson.GetBytes(body, "previous_response_id")
	if previous.Exists() && previous.Type != gjson.String {
		return body, false
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false
	}
	items := input.Array()
	var candidates []int
	for i, item := range items {
		typ := item.Get("type").String()
		callID := item.Get("call_id")
		if service.IsCodexGoalContextItem(item) {
			candidates = append(candidates, i)
			continue
		}
		if typ == "item_reference" {
			id := item.Get("id")
			if id.Type != gjson.String || strings.TrimSpace(id.String()) == "" {
				return body, false
			}
		}
		if strings.HasSuffix(typ, "_call") || isResponsesCallOutputType(typ) {
			if callID.Type != gjson.String || strings.TrimSpace(callID.String()) == "" {
				return body, false
			}
		}
	}
	if len(candidates) == 0 {
		return body, false
	}
	result := body
	for j := len(candidates) - 1; j >= 0; j-- {
		var err error
		result, err = sjson.DeleteBytes(result, "input."+strconv.Itoa(candidates[j]))
		if err != nil {
			return body, false
		}
	}
	return result, true
}
