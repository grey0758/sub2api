package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAntigravityCompatHeartbeatBeforeUpstreamHeaders(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-pro-high","stream":true,"messages":[{"role":"user","content":"Fresh header-wait fixture"}]}`)
	c, rec := newAntigravityCompatContext("POST", "/v1/chat/completions", raw)
	writes := make(chan struct{}, 8)
	c.Writer = &antigravityCompatNotifyingWriter{ResponseWriter: c.Writer, wrote: writes}
	release := make(chan struct{})
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{antigravityCompatSuccessResponse()}, onCall: func(_ *http.Request, _ *queuedHTTPUpstreamStub) { <-release }}
	svc := newAntigravityCompatService(config.GatewayConfig{}, upstream)
	done := make(chan error, 1)
	go func() {
		_, err := svc.ForwardAsChatCompletions(context.Background(), c, newAntigravityCompatAccount(AccountTypeOAuth), raw, nil)
		done <- err
	}()
	var early bool
	select {
	case <-writes:
		early = true
	case <-time.After(17 * time.Second):
	}
	close(release)
	require.NoError(t, <-done)
	require.True(t, early, "no downstream heartbeat while upstream headers were withheld")
	require.Contains(t, rec.Body.String(), ": ping\n\n")
	require.Contains(t, rec.Body.String(), "[DONE]")
	require.Equal(t, 1, upstream.callCount, "keepalive must not regenerate")
}

func TestAntigravityCompatSlowHeadersDoNotExpireBodyBeforeRead(t *testing.T) {
	c, rec := newAntigravityCompatContext("POST", "/v1/responses", nil)
	svc := newAntigravityCompatService(config.GatewayConfig{}, nil)
	result, err := svc.handleAntigravityCompatStreamWithKeepaliveInterval(c, antigravityCompatSuccessResponse(), time.Now().Add(-3*time.Minute), "gemini-3.1-pro-high", newAntigravityResponsesStreamAdapter("gemini-3.1-pro-high"), "fixture", time.Second, 2*time.Minute)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "response.completed")
}
