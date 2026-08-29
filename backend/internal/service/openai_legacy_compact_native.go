package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// forwardLegacyOpenAICompactViaNativeV2 adapts the legacy unary
// /responses/compact facade to the native Codex compaction protocol. OpenAI's
// ChatGPT backend removed the former /compact resource, while the current
// protocol is a streaming /responses request carrying compaction_trigger.
// Keep this compatibility path limited to OAuth accounts; API-key providers
// may expose a real legacy endpoint and must retain their existing behavior.
func (s *OpenAIGatewayService) forwardLegacyOpenAICompactViaNativeV2(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) (*OpenAIForwardResult, error) {
	nativeBody, err := buildOpenAILegacyCompactNativeV2Body(body)
	if err != nil {
		return nil, err
	}

	// Forward writes the native SSE stream to its gin writer. Capture that
	// response in an isolated context, then return the completed response as
	// JSON to the legacy caller. Copy request-scoped values so auth, API-key,
	// routing and billing metadata remain identical to the outer request.
	capture := httptest.NewRecorder()
	nativeContext, _ := gin.CreateTestContext(capture)
	if c != nil {
		for key, value := range c.Keys {
			nativeContext.Set(key, value)
		}
	}
	if c == nil || c.Request == nil {
		return nil, errors.New("legacy compact context is required")
	}
	nativeRequest := c.Request.Clone(ctx)
	nativeRequest.URL.Path = "/v1/responses"
	nativeRequest.URL.RawPath = ""
	nativeRequest.Body = io.NopCloser(bytes.NewReader(nativeBody))
	nativeRequest.ContentLength = int64(len(nativeBody))
	nativeContext.Request = nativeRequest
	SetOpenAIClientTransport(nativeContext, OpenAIClientTransportHTTP)
	MarkOpenAINativeCompactionV2(nativeContext)

	result, forwardErr := s.Forward(ctx, nativeContext, account, nativeBody)
	if forwardErr != nil {
		return result, forwardErr
	}
	finalResponse, ok := extractCodexFinalResponse(capture.Body.String())
	if !ok {
		return nil, errors.New("native compact response did not contain response.completed")
	}
	// Some upstreams emit the compaction item only in output_item.done and an
	// empty or partial terminal output array. Reuse the existing repair helper
	// against the original compact context before returning JSON.
	finalResponse = supplementCompactionItemFromSSE(c, finalResponse, capture.Body.String())
	if !responsesOutputHasCompactionItem(finalResponse) {
		return nil, errors.New("native compact response did not contain a compaction item")
	}

	responseheaders.WriteFilteredHeaders(c.Writer.Header(), capture.Header(), s.responseHeaderFilter)
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(finalResponse)
	return result, nil
}

func buildOpenAILegacyCompactNativeV2Body(body []byte) ([]byte, error) {
	legacyBody, _, err := normalizeOpenAIResponsesLegacyIngress(body)
	if err != nil {
		return nil, err
	}
	compactBody, _, err := normalizeOpenAICompactRequestBody(legacyBody)
	if err != nil {
		return nil, err
	}
	var request map[string]any
	if err := json.Unmarshal(compactBody, &request); err != nil {
		return nil, fmt.Errorf("decode legacy compact body: %w", err)
	}
	// prompt_cache_key is a native Responses session signal. The legacy body
	// normalizer intentionally omits it for the dead /compact resource, so
	// restore it while translating to native v2.
	if value := gjson.GetBytes(legacyBody, "prompt_cache_key"); value.Exists() {
		request["prompt_cache_key"] = value.Value()
	}

	input := make([]any, 0, 2)
	switch value := request["input"].(type) {
	case []any:
		input = append(input, value...)
	case string:
		input = append(input, map[string]any{"type": "message", "role": "user", "content": value})
	case map[string]any:
		if strings.TrimSpace(fmt.Sprint(value["type"])) == "" {
			input = append(input, map[string]any{"type": "message", "role": "user", "content": value})
		} else {
			input = append(input, value)
		}
	case nil:
		// A trigger-only compaction turn is valid and lets the upstream use the
		// session identified by prompt_cache_key/session headers.
	default:
		return nil, errors.New("legacy compact input must be a string, object, or array")
	}
	triggerPresent := false
	for _, item := range input {
		if object, ok := item.(map[string]any); ok && object["type"] == "compaction_trigger" {
			triggerPresent = true
			break
		}
	}
	if !triggerPresent {
		input = append(input, map[string]any{"type": "compaction_trigger"})
	}
	request["input"] = input
	request["stream"] = true
	request["store"] = false
	return json.Marshal(request)
}
