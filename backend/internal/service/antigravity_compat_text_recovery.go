package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const antigravityCompatTextRecoveryKey = "antigravity_compat_text_recovery"
const antigravityCompatTextRecoveryInstruction = "No callable tools are available for this turn. Respond directly with text. If a task mentions tools or commands, describe them as text; do not emit function calls or attempt to execute tools."

var errAntigravityCompatRecoverText = errors.New("retry empty no-tool malformed generation once")

type antigravityCompatTextRecovery struct {
	allowed, used bool
	started       time.Time
	failedUsage   *ClaudeUsage
}

func antigravityCompatRecoveryFromContext(c *gin.Context) *antigravityCompatTextRecovery {
	if c == nil {
		return nil
	}
	value, _ := c.Get(antigravityCompatTextRecoveryKey)
	r, _ := value.(*antigravityCompatTextRecovery)
	return r
}
func antigravityCompatCanRecoverText(c *gin.Context) bool {
	r := antigravityCompatRecoveryFromContext(c)
	return r != nil && r.allowed && !r.used && c.Request.Context().Err() == nil && time.Since(r.started) < 90*time.Second
}

// Recovery is restricted to native Gemini requests with no tools and no tool
// history. It cannot repeat a client function or a server-side built-in action.
func antigravityCompatTextOnly(body []byte) bool {
	r := gjson.ParseBytes(body)
	if !strings.HasPrefix(strings.ToLower(r.Get("model").String()), "gemini-") || r.Get("request.toolConfig.functionCallingConfig.mode").String() != "NONE" || len(r.Get("request.tools").Array()) != 0 {
		return false
	}
	for _, content := range r.Get("request.contents").Array() {
		for _, part := range content.Get("parts").Array() {
			for _, field := range []string{"functionCall", "functionResponse", "function_call", "function_response", "executableCode", "codeExecutionResult"} {
				if part.Get(field).Exists() {
					return false
				}
			}
		}
	}
	return true
}
func antigravityCompatPartsHaveOutput(parts []map[string]any) bool {
	for _, p := range parts {
		for k, v := range p {
			if k == "thoughtSignature" || k == "thought_signature" || k == "thought" {
				continue
			}
			if k == "text" {
				if text, _ := v.(string); text != "" {
					return true
				}
				continue
			}
			return true // Unknown media/tool content must conservatively prevent a replay.
		}
	}
	return false
}
func antigravityCompatTextRecoveryBody(body []byte) ([]byte, error) {
	if !antigravityCompatTextOnly(body) {
		return nil, errors.New("text recovery requires a no-tool request")
	}
	parts := gjson.GetBytes(body, "request.systemInstruction.parts")
	var system []json.RawMessage
	if parts.Exists() {
		if err := json.Unmarshal([]byte(parts.Raw), &system); err != nil {
			return nil, err
		}
	}
	guard, _ := json.Marshal(map[string]string{"text": antigravityCompatTextRecoveryInstruction})
	system = append(system, guard)
	out, err := sjson.SetBytes(body, "request.systemInstruction.parts", system)
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(out, "requestId", "agent-"+uuid.NewString())
}
func isAntigravityMalformedError(err error) bool {
	return errors.Is(err, errAntigravityMalformedFunctionCall) || errors.Is(err, antigravity.ErrMalformedFunctionCall) || (err != nil && strings.Contains(err.Error(), "MALFORMED_FUNCTION_CALL"))
}
func recordAntigravityCompatGenerationFailure(c *gin.Context, account *Account, recovering bool, usage *ClaudeUsage) {
	reason := "MALFORMED_FUNCTION_CALL"
	if recovering {
		reason += "; one text-only recovery; no output or tool emitted"
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: account.Platform, AccountID: account.ID, ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account), UpstreamStatusCode: http.StatusOK, Kind: "generation_error", Stage: "generation", Scope: "request", Reason: "malformed_function_call", Message: reason})
	// Failed generation tokens are provider telemetry, not a second customer usage record.
	if usage != nil {
		logger.LegacyPrintf("service.antigravity_gateway", "text_recovery account=%d failed_input_tokens=%d failed_output_tokens=%d failed_cache_read_tokens=%d", account.ID, usage.InputTokens, usage.OutputTokens, usage.CacheReadInputTokens)
	}
}
func writeAntigravityCompatRecoveryStreamError(c *gin.Context, protocol antigravityCompatProtocol, reason string) error {
	if protocol == antigravityCompatResponses {
		fmt.Fprintf(c.Writer, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"upstream_error\",\"message\":%q}}\n\n", reason)
	} else {
		fmt.Fprintf(c.Writer, "data: {\"error\":{\"type\":\"upstream_error\",\"message\":%q}}\n\n", reason)
	}
	c.Writer.Flush()
	return errors.New(reason)
}
