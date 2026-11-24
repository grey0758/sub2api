package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

type krillFixtureUpstream struct {
	service.HTTPUpstream
	client *http.Client
}

func (u krillFixtureUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.client.Do(req)
}

func TestKrillResponsesHTTPStreamTermination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const partial = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n"
	const complete = "\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"model\":\"gpt-6.1-sol\",\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n"
	const rejected = "\nevent: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"failed\",\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"fixture\",\"type\":\"invalid_request_error\"}}}\n\n"
	for _, tc := range []struct {
		name      string
		group     int64
		terminal  string
		wantCode  string
		wantUsage int
	}{
		{name: "missing terminal", group: 8, wantCode: "upstream_stream_incomplete"},
		{name: "completed", group: 8, terminal: complete, wantUsage: 1},
		{name: "provider failure", group: 8, terminal: rejected, wantCode: "context_length_exceeded"},
		{name: "outside Krill group", group: 9, terminal: "\n", wantCode: "upstream_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/v1/responses", r.URL.Path)
				posts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, partial)
				w.(http.Flusher).Flush()
				_, _ = io.WriteString(w, tc.terminal)
			}))
			defer upstream.Close()

			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Default.RateMultiplier = 1
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			cfg.Gateway.KrillCompatibilityGroupID = 8
			account := service.Account{
				ID: 11, Name: "krill-fixture", Status: service.StatusActive, Schedulable: true,
				Type: service.AccountTypeAPIKey, Platform: service.PlatformOpenAI,
				Credentials: map[string]any{"api_key": "fixture", "base_url": upstream.URL},
				Extra:       map[string]any{"openai_passthrough": true},
			}
			accounts := openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}
			usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 3)}
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			defer billing.Stop()
			gateway := service.NewOpenAIGatewayService(accounts, usage, nil, nil, nil, nil, nil, cfg, nil, nil,
				service.NewBillingService(cfg, nil), nil, billing, krillFixtureUpstream{client: upstream.Client()},
				&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
			h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing,
				service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
			c, recorder := newGinContextForEndpoint(t, EndpointResponses)
			c.Request.Body = io.NopCloser(strings.NewReader(`{"model":"gpt-6.1-sol","stream":true,"input":"fixture"}`))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
				ID: 2, GroupID: &tc.group, Group: &service.Group{
					ID: tc.group, Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1,
				}, User: &service.User{ID: 100, Status: service.StatusActive},
			})
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 100})

			h.Responses(c)

			require.Equal(t, int32(1), posts.Load())
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Len(t, usage.created, tc.wantUsage)
			body := recorder.Body.String()
			if tc.wantCode == "" {
				require.Equal(t, partial+complete, body)
				return
			}
			require.NotContains(t, body, "response.completed")
			require.Equal(t, 1, strings.Count(body, "event: response.failed\n"))
			failure, found := strings.CutPrefix(body, partial+"\n")
			require.True(t, found)
			if tc.terminal == rejected {
				require.Equal(t, partial+rejected, body)
				return
			}
			_, reported := parseResponsesFailedSSE(t, failure)
			require.Equal(t, tc.wantCode, reported["code"])
		})
	}
}

func TestKrillIncompleteResponsesStreamError(t *testing.T) {
	const partial = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n"
	const code = "upstream_stream_incomplete"
	h := &OpenAIGatewayHandler{}
	for _, upstreamOutput := range []string{partial, partial + "\n"} {
		c, recorder := newGinContextForEndpoint(t, EndpointResponses)
		c.Header("Content-Type", "text/event-stream")
		_, writeErr := c.Writer.WriteString(upstreamOutput)
		require.NoError(t, writeErr)
		require.True(t, h.ensureKrillIncompleteResponsesStreamError(c, service.ErrOpenAIPassthroughStreamMissingTerminal, true))
		wantPrefix := upstreamOutput + "\n"
		require.True(t, strings.HasPrefix(recorder.Body.String(), wantPrefix))
		_, failure := parseResponsesFailedSSE(t, strings.TrimPrefix(recorder.Body.String(), wantPrefix))
		require.Equal(t, code, failure["code"])
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: response.failed\n"))
		streamError, ok := service.GetOpsStreamError(c)
		require.True(t, ok)
		require.Equal(t, code, streamError.Code)
		require.Equal(t, http.StatusBadGateway, streamError.IntendedStatus)
	}

	for _, tc := range []struct {
		name      string
		path      string
		output    string
		committed bool
		canceled  bool
		enabled   bool
		err       error
	}{
		{name: "unrelated group", path: EndpointResponses, output: partial, err: service.ErrOpenAIPassthroughStreamMissingTerminal},
		{name: "no bytes written", path: EndpointResponses, enabled: true, err: service.ErrOpenAIPassthroughStreamMissingTerminal},
		{name: "other error", path: EndpointResponses, output: partial, enabled: true, err: errors.New("other error")},
		{name: "compact", path: EndpointResponses + "/compact", output: partial, enabled: true, err: service.ErrOpenAIPassthroughStreamMissingTerminal},
		{name: "terminal already sent", path: EndpointResponses, output: partial, committed: true, enabled: true, err: service.ErrOpenAIPassthroughStreamMissingTerminal},
		{name: "canceled", path: EndpointResponses, output: partial, canceled: true, enabled: true, err: service.ErrOpenAIPassthroughStreamMissingTerminal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, recorder := newGinContextForEndpoint(t, tc.path)
			if tc.output != "" {
				_, writeErr := c.Writer.WriteString(tc.output)
				require.NoError(t, writeErr)
			}
			if tc.committed {
				service.MarkResponseCommitted(c)
			}
			if tc.canceled {
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
			}
			require.False(t, h.ensureKrillIncompleteResponsesStreamError(c, tc.err, tc.enabled))
			require.Equal(t, tc.output, recorder.Body.String())
		})
	}
}

func TestKrillGoalValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const goal = `{"type":"function_call_output","namespace":"agent","name":"goal_objective","id":"fco_synthetic_goal","output":"Synthetic goal","internal_chat_message_metadata_passthrough":{"turn_id":"synthetic"}}`
	const ordinary = `{"type":"function_call_output","name":"example","output":"Synthetic tool output"}`
	const matchedCall = `{"type":"function_call","name":"example","call_id":"call_synthetic","arguments":"{}"}`
	const matchedOutput = `{"type":"function_call_output","call_id":"call_synthetic","output":"Synthetic tool output"}`
	for _, tc := range []struct {
		name  string
		input string
		group int64
		valid bool
	}{
		{"exact goal", goal, 8, true},
		{"goal and correlated history", goal + "," + matchedCall + "," + matchedOutput, 8, true},
		{"goal and unmatched history", goal + "," + ordinary, 8, false},
		{"goal with an unmatched call ID", goal + "," + matchedOutput, 8, false},
		{"unrelated group", goal, 9, false},
		{"ordinary missing call ID", ordinary, 8, false},
		{"duplicate member", `{"type":"function_call_output","namespace":"agent","name":"goal_objective","name":"other","output":"Synthetic goal"}`, 8, false},
		{"null call ID", `{"type":"function_call_output","namespace":"agent","name":"goal_objective","output":"Synthetic goal","call_id":null}`, 8, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.KrillCompatibilityGroupID = 8
			key := &service.APIKey{GroupID: &tc.group}
			body := []byte(`{"model":"gpt-6.2-sol","input":[` + tc.input + `]}`)
			original := string(body)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			h := &OpenAIGatewayHandler{}
			require.Equal(t, tc.valid, h.validateFunctionCallOutputRequest(c, body, zap.NewNop(), krillResponsesCompatibilityEnabled(cfg, key)))
			if !tc.valid {
				require.Equal(t, http.StatusBadRequest, w.Code)
			}
			require.Equal(t, original, string(body))
		})
	}
}

func TestKrillReasoningEffortCompatibility(t *testing.T) {
	account := &service.Account{
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
		Extra:    map[string]any{"openai_passthrough": true},
	}
	body := []byte(`{ "model":"gpt-6.1-sol", "reasoning":{"effort":"none","summary":"auto"}, "input":[{"role":"user","content":"synthetic"}] }`)
	want, err := sjson.SetBytes(body, "reasoning.effort", "low")
	require.NoError(t, err)
	require.Equal(t, want, normalizeKrillResponsesEffort(body, account))

	for _, candidate := range [][]byte{
		[]byte(`{"model":"gpt-6.1-sol","reasoning":{"effort":"xhigh"}}`),
		[]byte(`{"model":"gpt-6.2-sol","reasoning":{"effort":"none"}}`),
		[]byte(`{"model":"gpt-6.1-sol","reasoning":{}}`),
		[]byte(`{"model":"gpt-6.1-sol","reasoning":{"effort":"none","effort":"high"}}`),
	} {
		require.Equal(t, candidate, normalizeKrillResponsesEffort(candidate, account))
	}
	require.Equal(t, body, normalizeKrillResponsesEffort(body, nil))
	require.Equal(t, "none", gjson.GetBytes(body, "reasoning.effort").String())
}
