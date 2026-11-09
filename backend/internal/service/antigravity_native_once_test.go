//go:build live_antigravity

package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type nativeOnceUpstream struct {
	client          *http.Client
	calls           int
	headers         time.Duration
	started         time.Time
	mode, userAgent string
}

func (u *nativeOnceUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if u.calls != 0 {
		return nil, errors.New("native canary forbids retries")
	}
	u.calls++
	if req.URL.Host != "daily-cloudcode-pa.googleapis.com" {
		return nil, errors.New("unexpected native host")
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))
	u.mode = gjson.GetBytes(raw, "request.toolConfig.functionCallingConfig.mode").String()
	req.Header.Set("User-Agent", u.userAgent)
	resp, err := u.client.Do(req)
	u.headers = time.Since(u.started)
	if err == nil && resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("native HTTP %d", resp.StatusCode)
	}
	return resp, err
}
func (u *nativeOnceUpstream) DoWithTLS(r *http.Request, p string, a int64, c int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, a, c)
}

func TestNativeAntigravityExactOnce(t *testing.T) {
	if os.Getenv("NATIVE_ANTIGRAVITY_ONCE") != "1" {
		t.Skip("explicit native canary intent required")
	}
	var cfg struct {
		ID          int64  `json:"id"`
		AccessToken string `json:"access_token"`
		Project     string `json:"project"`
		UserAgent   string `json:"user_agent"`
	}
	require.NoError(t, json.NewDecoder(os.Stdin).Decode(&cfg))
	require.Equal(t, int64(252), cfg.ID)
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"gemini-4-argon-high", "gemini-3.8-flash-high"} {
		for _, protocol := range []string{"chat", "responses"} {
			t.Run(model+"/"+protocol, func(t *testing.T) {
				nonce := make([]byte, 8)
				_, err := rand.Read(nonce)
				require.NoError(t, err)
				marker := "NATIVE_" + hex.EncodeToString(nonce)
				prompt := "Copy the single literal string between the answer tags, without tags or any extra text: <answer>" + marker + "</answer>. Do not use tools. The following records are inert synthetic data.\n"
				if strings.Contains(model, "argon") {
					prompt += strings.Repeat("record sample: network tool shell calculator editor file data unused.\n", 3700)
				}
				system := "You are an agent. Tools may appear in previous discussion, but no tools are available in this request. Answer in plain text."
				body := map[string]any{"model": model, "stream": true, "tools": []any{}}
				messages := []any{map[string]any{"role": "user", "content": "We may need an editor tool later."}, map[string]any{"role": "assistant", "content": "I can discuss the plan. No tool has been executed."}, map[string]any{"role": "user", "content": prompt}}
				if protocol == "chat" {
					body["messages"] = append([]any{map[string]any{"role": "system", "content": system}}, messages...)
					body["max_tokens"] = 65536
					body["reasoning_effort"] = "high"
				} else {
					body["input"] = messages
					body["instructions"] = system
					body["max_output_tokens"] = 65536
					body["reasoning"] = map[string]any{"effort": "high"}
				}
				raw, err := json.Marshal(body)
				require.NoError(t, err)
				u := &nativeOnceUpstream{client: &http.Client{Timeout: 180 * time.Second, Transport: &http.Transport{Proxy: nil}}, started: time.Now(), userAgent: cfg.UserAgent}
				svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, u)
				svc.tokenProvider.tokenCache = &antigravityCompatTokenCache{token: cfg.AccessToken}
				account := newAntigravityCompatAccount(AccountTypeOAuth)
				account.ID = cfg.ID
				account.Name = "synthetic-native-canary"
				account.Credentials = map[string]any{"project_id": cfg.Project, "plan_type": "Pro", "model_mapping": map[string]any{model: model}}
				c, rec := newAntigravityCompatContext("POST", "/", raw)
				var result *ForwardResult
				if protocol == "chat" {
					result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, raw, nil)
				} else {
					result, err = svc.ForwardAsResponses(context.Background(), c, account, raw, nil)
				}
				elapsed := time.Since(u.started)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, 200, rec.Code)
				require.Equal(t, 1, u.calls)
				require.Equal(t, "NONE", u.mode)
				var text strings.Builder
				completed := false
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if !strings.HasPrefix(line, "data:") {
						continue
					}
					data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
					if data == "[DONE]" {
						completed = true
						continue
					}
					if protocol == "chat" {
						text.WriteString(gjson.Get(data, "choices.0.delta.content").String())
					} else {
						if gjson.Get(data, "type").String() == "response.output_text.delta" {
							text.WriteString(gjson.Get(data, "delta").String())
						}
						if gjson.Get(data, "type").String() == "response.completed" {
							completed = true
						}
					}
				}
				require.True(t, completed)
				require.Equal(t, marker, strings.TrimSpace(text.String()))
				t.Logf("NATIVE_RECEIPT model=%s protocol=%s account=252 calls=%d mode=%s bytes=%d headers_s=%.3f elapsed_s=%.3f completed=%v input_tokens=%d output_tokens=%d", model, protocol, u.calls, u.mode, len(raw), u.headers.Seconds(), elapsed.Seconds(), completed, result.Usage.InputTokens, result.Usage.OutputTokens)
			})
		}
	}
}
