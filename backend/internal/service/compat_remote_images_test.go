package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCompatImagesReachGeminiUnchanged(t *testing.T) {
	image := base64.StdEncoding.EncodeToString([]byte("unchanged-image-bytes-and-EXIF"))
	for _, responses := range []bool{false, true} {
		t.Run(fmt.Sprint(responses), func(t *testing.T) {
			stub := &geminiCompatHTTPUpstreamStub{response: antigravityCompatSuccessResponse()}
			svc := newAntigravityCompatService(config.GatewayConfig{}, stub)
			account := newAntigravityCompatAccount(AccountTypeOAuth)
			var body []byte
			if responses {
				body = []byte(fmt.Sprintf(`{"model":"gemini-3.1-pro-high","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"data:image/png;base64,%s"}]}]}`, image))
			} else {
				body = []byte(fmt.Sprintf(`{"model":"gemini-3.1-pro-high","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,%s"}}]}]}`, image))
			}
			c, _ := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)
			if responses {
				_, err := svc.ForwardAsResponses(context.Background(), c, account, body, nil)
				require.NoError(t, err)
			} else {
				_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
				require.NoError(t, err)
			}
			require.Equal(t, 1, stub.calls)
			sent, err := io.ReadAll(stub.lastReq.Body)
			require.NoError(t, err)
			require.Equal(t, image, gjson.GetBytes(sent, "request.contents.0.parts.1.inlineData.data").String())
		})
	}
}

func TestCompatRemoteImageErrorsStopBeforeUpstream(t *testing.T) {
	for _, kind := range []string{"antigravity-chat", "antigravity-responses", "gemini-chat"} {
		t.Run(kind, func(t *testing.T) {
			stub := &geminiCompatHTTPUpstreamStub{response: antigravityCompatSuccessResponse()}
			body := []byte(`{"model":"gemini-3.1-pro-high","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"http://169.254.169.254/latest/meta-data/"}}]}]}`)
			c, rec := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)
			if kind == "gemini-chat" {
				svc := &GeminiMessagesCompatService{httpUpstream: stub, cfg: &config.Config{}}
				_, err := svc.ForwardAsChatCompletions(context.Background(), c, nil, body)
				require.Error(t, err)
			} else {
				svc := newAntigravityCompatService(config.GatewayConfig{}, stub)
				account := newAntigravityCompatAccount(AccountTypeOAuth)
				if strings.HasSuffix(kind, "responses") {
					body = []byte(`{"model":"gemini-3.1-pro-high","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"http://169.254.169.254/"}]}]}`)
					_, err := svc.ForwardAsResponses(context.Background(), c, account, body, nil)
					require.Error(t, err)
				} else {
					_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
					require.Error(t, err)
				}
			}
			require.Equal(t, 0, stub.calls)
			require.Equal(t, 400, rec.Code)
			require.Contains(t, rec.Body.String(), "reference_image_error")
			require.NotContains(t, rec.Body.String(), "169.254")
		})
	}
}

func TestCompatResolverPreservesOriginalInputOnFailure(t *testing.T) {
	raw := json.RawMessage(`[{"role":"user","content":[{"type":"input_image","image_url":"file:///private"}]}]`)
	req := &apicompat.ResponsesRequest{Input: raw}
	require.Error(t, resolveGeminiCompatImages(context.Background(), req))
	require.Equal(t, raw, req.Input)
}
