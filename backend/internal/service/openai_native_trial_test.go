//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// This harness runs the actual Sub2API token provider and WS ingress/relay.
// It bypasses the product's DB/scheduler/billing and is never a production route.
type nativeTrialCapture struct {
	mu            sync.Mutex
	dials         int
	requests      [][]byte
	completed     []string
	parentMatches int
	headers       map[string]any
	delegate      openAIWSClientDialer
	offlineURL    string
	raw           *os.File
}

type nativeTrialConn struct {
	openAIWSClientConn
	frames interface {
		ReadFrame(context.Context) (coderws.MessageType, []byte, error)
		WriteFrame(context.Context, coderws.MessageType, []byte) error
	}
	capture *nativeTrialCapture
}

func (d *nativeTrialCapture) Dial(ctx context.Context, target string, headers http.Header, proxy string) (openAIWSClientConn, int, http.Header, error) {
	d.mu.Lock()
	d.dials++
	d.headers = map[string]any{"user_agent": headers.Get("User-Agent"), "originator": headers.Get("originator"),
		"beta": headers.Get("OpenAI-Beta"), "version": headers.Get("version"),
		"has_authorization": headers.Get("Authorization") != "", "has_account_id": headers.Get("chatgpt-account-id") != ""}
	parts := strings.Split(strings.TrimPrefix(headers.Get("Authorization"), "Bearer "), ".")
	if len(parts) == 3 {
		claims, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err == nil {
			var value map[string]any
			if json.Unmarshal(claims, &value) == nil {
				auth, _ := value["https://api.openai.com/auth"].(map[string]any)
				d.headers["account_header_matches_token"] = auth["chatgpt_account_id"] == headers.Get("chatgpt-account-id")
			}
		}
	}
	d.mu.Unlock()
	if d.offlineURL != "" {
		target = d.offlineURL
	}
	conn, status, resultHeaders, err := d.delegate.Dial(ctx, target, headers, proxy)
	if err != nil {
		return nil, status, resultHeaders, err
	}
	frames := conn.(interface {
		ReadFrame(context.Context) (coderws.MessageType, []byte, error)
		WriteFrame(context.Context, coderws.MessageType, []byte) error
	})
	return &nativeTrialConn{conn, frames, d}, status, resultHeaders, nil
}

func (c *nativeTrialConn) WriteFrame(ctx context.Context, typ coderws.MessageType, body []byte) error {
	c.capture.mu.Lock()
	c.capture.requests = append(c.capture.requests, append([]byte(nil), body...))
	parent := gjson.GetBytes(body, "previous_response_id").String()
	if n := len(c.capture.completed); n > 0 && parent == c.capture.completed[n-1] {
		c.capture.parentMatches++
	}
	if c.capture.raw != nil {
		_ = json.NewEncoder(c.capture.raw).Encode(map[string]any{"direction": "out", "payload": json.RawMessage(body)})
	}
	c.capture.mu.Unlock()
	return c.frames.WriteFrame(ctx, typ, body)
}

func (c *nativeTrialConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	typ, body, err := c.frames.ReadFrame(ctx)
	if err == nil {
		c.capture.mu.Lock()
		if gjson.GetBytes(body, "type").String() == "response.completed" {
			c.capture.completed = append(c.capture.completed, gjson.GetBytes(body, "response.id").String())
		}
		if c.capture.raw != nil {
			_ = json.NewEncoder(c.capture.raw).Encode(map[string]any{"direction": "in", "payload": json.RawMessage(body)})
		}
		c.capture.mu.Unlock()
	}
	return typ, body, err
}

func nativeTrialConfig() *config.Config {
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 15
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 120
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 15
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 60
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 120
	return cfg
}

func runNativeTrial(t *testing.T, directory string, account *Account, capture *nativeTrialCapture) {
	t.Helper()
	// Observed from the fixed official 0.163 initialize RPC on this us003 host.
	// This pins the trial identity; it does not establish TLS/header-order equality.
	const nativeUA = "codex_cli_rs/0.163.0 (Ubuntu 26.4.0; x86_64) unknown (codex_cli_rs; 0.163.0)"
	SetCodexCanonicalUserAgentResolver(func() string { return nativeUA })
	defer SetCodexCanonicalUserAgentResolver(nil)
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cache := newOpenAITokenCacheStub()
	svc := &OpenAIGatewayService{cfg: nativeTrialConfig(), cache: &stubGatewayCache{},
		openAITokenProvider: NewOpenAITokenProvider(nil, cache, nil), openaiWSPassthroughDialer: capture,
		toolCorrector: NewCodexToolCorrector()}
	account.Status = StatusActive
	account.Schedulable = true
	account.Concurrency = 1
	account.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModePassthrough
	serverErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _, err := svc.GetAccessToken(ctx, account)
		if err != nil {
			http.Error(w, "broker unavailable", 503)
			serverErr <- err
			return
		}
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.CloseNow()
		_, first, err := conn.Read(ctx)
		if err != nil {
			serverErr <- err
			return
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r.Clone(ctx)
		c.Set("api_key", &APIKey{ID: 991001})
		serverErr <- svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, token, first, nil)
	}))
	defer server.Close()
	session := uuid.NewString()
	nonce := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	headers := http.Header{"User-Agent": []string{nativeUA}, "Originator": []string{"codex_cli_rs"}, "Session_id": []string{session}, "Thread-id": []string{uuid.NewString()}}
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &coderws.DialOptions{HTTPHeader: headers})
	require.NoError(t, err)
	defer conn.CloseNow()
	var sent [][]byte
	send := func(stage string, payload map[string]any) {
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		intent, err := os.OpenFile(filepath.Join(directory, stage+".intent"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		require.NoError(t, err, "consumed intent must never be replayed")
		sum := sha256.Sum256(body)
		_, err = intent.WriteString(hex.EncodeToString(sum[:]) + "\n")
		require.NoError(t, err)
		require.NoError(t, intent.Sync())
		require.NoError(t, intent.Close())
		sent = append(sent, body)
		require.NoError(t, conn.Write(ctx, coderws.MessageText, body))
	}
	readCompleted := func() gjson.Result {
		for {
			_, body, err := conn.Read(ctx)
			require.NoError(t, err)
			typ := gjson.GetBytes(body, "type").String()
			require.False(t, typ == "error" || typ == "response.failed" || typ == "response.incomplete", "upstream returned a non-success terminal event; inspect private evidence")
			if typ == "response.completed" {
				return gjson.GetBytes(body, "response")
			}
		}
	}
	tools := []any{map[string]any{"type": "function", "name": "trial_echo", "description": "Return the supplied value without side effects.", "strict": true,
		"parameters": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string", "enum": []string{nonce}}}, "required": []string{"value"}, "additionalProperties": false}}}
	base := map[string]any{"type": "response.create", "model": "gpt-6.1-sol", "instructions": "Use the trial_echo tool once when asked. After its result, output exactly TRIAL_OK_ followed by that value.",
		"input": []any{}, "tools": tools, "tool_choice": "auto", "parallel_tool_calls": false, "store": false, "stream": true,
		"reasoning": map[string]any{"effort": "xhigh", "context": "all_turns"}, "text": map[string]any{"verbosity": "low"}, "include": []string{"reasoning.encrypted_content"},
		"prompt_cache_key": session, "client_metadata": map[string]any{"session_id": session, "thread_id": headers.Get("thread-id"), "turn_id": uuid.NewString()}}
	base["generate"] = false
	send("01-prewarm", base)
	warm := readCompleted()
	require.NotEmpty(t, warm.Get("id").String())
	delete(base, "generate")
	base["previous_response_id"] = warm.Get("id").String()
	base["input"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Call trial_echo with value " + nonce}}}}
	base["tool_choice"] = map[string]any{"type": "function", "name": "trial_echo"}
	send("02-tool-request", base)
	tool := readCompleted()
	var call gjson.Result
	for _, item := range tool.Get("output").Array() {
		if item.Get("type").String() == "function_call" {
			require.False(t, call.Exists())
			call = item
		}
	}
	require.True(t, call.Exists(), "expected exactly one synthetic function call")
	require.True(t, call.Get("name").String() == "trial_echo")
	require.True(t, gjson.Get(call.Get("arguments").String(), "value").String() == nonce)
	base["previous_response_id"] = tool.Get("id").String()
	base["tool_choice"] = "none"
	base["input"] = []any{map[string]any{"type": "function_call_output", "call_id": call.Get("call_id").String(), "output": nonce}}
	send("03-tool-return", base)
	final := readCompleted()
	var finalText strings.Builder
	for _, item := range final.Get("output").Array() {
		for _, part := range item.Get("content").Array() {
			finalText.WriteString(part.Get("text").String())
		}
	}
	require.True(t, strings.TrimSpace(finalText.String()) == "TRIAL_OK_"+nonce, "final synthetic text did not match")
	_ = conn.Close(coderws.StatusNormalClosure, "trial complete")
	select {
	case <-serverErr:
	case <-time.After(5 * time.Second):
		t.Fatal("relay failed to stop")
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	require.Equal(t, 1, capture.dials)
	require.Len(t, capture.requests, 3)
	require.Len(t, capture.completed, 3)
	require.Equal(t, 2, capture.parentMatches)
	require.Equal(t, true, capture.headers["account_header_matches_token"])
	exact := 0
	semantic := 0
	contentEqual := 0
	var changedFields [][]string
	for i, body := range sent {
		if string(body) == string(capture.requests[i]) {
			exact++
		}
		var a, b map[string]any
		require.NoError(t, json.Unmarshal(body, &a))
		require.NoError(t, json.Unmarshal(capture.requests[i], &b))
		aa, _ := json.Marshal(a)
		bb, _ := json.Marshal(b)
		if string(aa) == string(bb) {
			semantic++
		}
		var changed []string
		for key, value := range a {
			left, _ := json.Marshal(value)
			right, _ := json.Marshal(b[key])
			if string(left) != string(right) {
				changed = append(changed, key)
			}
		}
		for key := range b {
			if _, ok := a[key]; !ok {
				changed = append(changed, key)
			}
		}
		changedFields = append(changedFields, changed)
		for _, key := range changed {
			require.True(t, key == "client_metadata" || key == "prompt_cache_key", "unexpected substantive request-body mutation: "+key)
		}
		delete(a, "client_metadata")
		delete(b, "client_metadata")
		delete(a, "prompt_cache_key")
		delete(b, "prompt_cache_key")
		aa, _ = json.Marshal(a)
		bb, _ = json.Marshal(b)
		if string(aa) == string(bb) {
			contentEqual++
		}
	}
	require.Equal(t, 3, contentEqual, "instructions/input/tools/parent/config changed")
	require.Zero(t, cache.getCalled)
	require.Zero(t, cache.setCalled)
	require.Zero(t, cache.lockCalled)
	require.NotContains(t, account.Credentials, "access_token")
	require.NotContains(t, account.Credentials, "refresh_token")
	summary := map[string]any{"completed": true, "scope": "isolated actual Sub2API WS ingress/token-provider harness; no DB/scheduler/billing", "upstream_connections": capture.dials,
		"prewarm": 1, "generations": 2, "completed_responses": len(capture.completed), "same_connection_parent_matches": capture.parentMatches,
		"complete_json_equal": semantic, "content_fields_equal": contentEqual, "changed_top_level_fields": changedFields, "wire_payload_bytes_equal": exact, "tool_call_and_return": true, "final_text_exact": true,
		"local_token_credentials": 0, "cache_gets": cache.getCalled, "cache_sets": cache.setCalled, "sub2api_refreshes": 0,
		"headers": capture.headers, "final_usage": json.RawMessage(final.Get("usage").Raw), "utc": time.Now().UTC().Format(time.RFC3339)}
	data, err := json.MarshalIndent(summary, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "result.json"), data, 0600))
}

func TestNativeCodexTrialOfflineCompleteChain(t *testing.T) {
	nativeBrokerFixture(t, nativeBrokerTestLease())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for i := 0; i < 3; i++ {
			_, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			response := map[string]any{"id": fmt.Sprintf("resp_offline_%d", i), "status": "completed", "model": "gpt-6.1-sol", "output": []any{}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}}
			if i == 1 {
				nonce := gjson.GetBytes(body, "tools.0.parameters.properties.value.enum.0").String()
				args, _ := json.Marshal(map[string]string{"value": nonce})
				response["output"] = []any{map[string]any{"type": "function_call", "name": "trial_echo", "call_id": "call_offline_1", "arguments": string(args)}}
			}
			if i == 2 {
				response["output"] = []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "TRIAL_OK_" + gjson.GetBytes(body, "input.0.output").String()}}}}
			}
			msg, _ := json.Marshal(map[string]any{"type": "response.completed", "response": response})
			_ = conn.Write(r.Context(), coderws.MessageText, msg)
		}
	}))
	defer upstream.Close()
	capture := &nativeTrialCapture{delegate: newDefaultOpenAIWSClientDialer(), offlineURL: "ws" + strings.TrimPrefix(upstream.URL, "http")}
	runNativeTrial(t, t.TempDir(), nativeBrokerTestAccount(), capture)
}

func TestNativeCodexTrialLive(t *testing.T) {
	directory := os.Getenv("SUB2API_NATIVE_TRIAL_DIR")
	if directory == "" {
		t.Skip("explicit private trial directory required")
	}
	// The runner creates this fresh scope before executing this test once.
	data, err := os.ReadFile(filepath.Join(directory, "binding.json"))
	require.NoError(t, err)
	var binding nativeCodexTokenLease
	require.NoError(t, json.Unmarshal(data, &binding))
	require.NotEmpty(t, binding.AccountID)
	require.Empty(t, binding.AccessToken)
	guard, err := os.OpenFile(filepath.Join(directory, "dispatch.started"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	require.NoError(t, guard.Sync())
	require.NoError(t, guard.Close())
	raw, err := os.OpenFile(filepath.Join(directory, "synthetic-frames.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	defer raw.Close()
	account := nativeBrokerTestAccount()
	account.Credentials = map[string]any{"chatgpt_account_id": binding.AccountID, "chatgpt_user_id": binding.UserID}
	runNativeTrial(t, directory, account, &nativeTrialCapture{delegate: newDefaultOpenAIWSClientDialer(), raw: raw})
}
