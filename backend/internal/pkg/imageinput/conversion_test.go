package imageinput

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

func TestRemoteImageSurvivesChatResponsesAnthropicConversion(t *testing.T) {
	r := testResolver(func(*http.Request) (*http.Response, error) { return response(pngBytes), nil })
	var chat apicompat.ChatCompletionsRequest
	if err := json.Unmarshal([]byte(`{"model":"gemini-3.8-flash-tiered","messages":[{"role":"user","content":[{"type":"text","text":"Describe this reference."},{"type":"image_url","image_url":{"url":"https://images.example/reference.png","detail":"high"}}]}]}`), &chat); err != nil {
		t.Fatal(err)
	}
	resp, err := apicompat.ChatCompletionsToResponses(&chat)
	if err != nil {
		t.Fatal(err)
	}
	// The unchanged converter demonstrates the old failure on this same input.
	before, err := apicompat.ResponsesToAnthropicRequest(resp)
	if err != nil {
		t.Fatal(err)
	}
	var oldContent []apicompat.AnthropicContentBlock
	if err = json.Unmarshal(before.Messages[0].Content, &oldContent); err != nil {
		t.Fatal(err)
	}
	if len(oldContent) != 1 || oldContent[0].Type != "text" {
		t.Fatal("baseline URL-loss evidence changed")
	}
	resp.Input, _, err = r.resolve(context.Background(), resp.Input)
	if err != nil {
		t.Fatal(err)
	}
	anthropic, err := apicompat.ResponsesToAnthropicRequest(resp)
	if err != nil {
		t.Fatal(err)
	}
	if len(anthropic.Messages) != 1 {
		t.Fatalf("messages %+v", anthropic.Messages)
	}
	var content []apicompat.AnthropicContentBlock
	if err = json.Unmarshal(anthropic.Messages[0].Content, &content); err != nil {
		t.Fatal(err)
	}
	if len(content) != 2 || content[1].Type != "image" || content[1].Source == nil || content[1].Source.Type != "base64" || content[1].Source.Data != base64.StdEncoding.EncodeToString(pngBytes) {
		t.Fatalf("image lost or altered: %+v", content)
	}
}
