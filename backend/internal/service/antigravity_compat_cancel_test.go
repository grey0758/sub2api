package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// A header heartbeat commits HTTP200 without generated output. Cancellation
// must remain a client failure, with no recovery, billing result or SSE append.
func TestAntigravityCompatCancelledHeaderWaitIsNotRecovered(t *testing.T) {
	for _, protocol := range []string{"chat", "responses"} {
		for _, heartbeat := range []bool{false, true} {
			t.Run(protocol+map[bool]string{false: "/before_headers", true: "/after_heartbeat"}[heartbeat], func(t *testing.T) {
				field := "messages"
				if protocol == "responses" {
					field = "input"
				}
				raw := []byte(`{"model":"gemini-3.1-pro-high","stream":true,"` + field + `":[{"role":"user","content":"inert cancellation fixture"}]}`)
				c, rec := newAntigravityCompatContext("POST", "/", raw)
				ctx, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
				before := ""
				if heartbeat {
					before = ": ping\n\n"
					_, _ = c.Writer.Write([]byte(before))
					c.Writer.Flush()
					c.Set(antigravityCompatHeaderHeartbeatBytesKey, len(before))
				}
				calls := 0
				// This transport observes the cancellation rather than returning a body.
				transport := antigravityHeaderHTTPFunc{do: func(r *http.Request) (*http.Response, error) {
					calls++
					cancel()
					return nil, r.Context().Err()
				}}
				svc := newAntigravityCompatService(config.GatewayConfig{}, transport)
				var result *ForwardResult
				var err error
				if protocol == "chat" {
					result, err = svc.ForwardAsChatCompletions(ctx, c, newAntigravityCompatAccount(AccountTypeOAuth), raw, nil)
				} else {
					result, err = svc.ForwardAsResponses(ctx, c, newAntigravityCompatAccount(AccountTypeOAuth), raw, nil)
				}
				require.ErrorIs(t, err, context.Canceled)
				require.Nil(t, result)
				require.Equal(t, 1, calls)
				require.Equal(t, before, rec.Body.String(), "do not append errors or completion to a disconnected client")
				marked := GetOpsStreamErrors(c)
				require.Len(t, marked, 1)
				require.Equal(t, "client_disconnected", marked[0].ErrType)
				require.Equal(t, 499, marked[0].IntendedStatus)
				require.True(t, marked[0].CountTowardsSLA, "logical499 must override committed200 for Ops")
				require.False(t, marked[0].RequestScoped, "do not also record upstream events as recovered")
				if !heartbeat {
					require.Equal(t, 499, c.Writer.Status())
				}
			})
		}
	}
}

func TestAntigravityCompatUncancelledTransportErrorRemainsUpstreamFailure(t *testing.T) {
	c, rec := newAntigravityCompatContext("POST", "/", nil)
	err := (&AntigravityGatewayService{}).handleAntigravityCompatTransportError(c, errors.New("fixture transport"))
	require.Error(t, err)
	require.Equal(t, 502, rec.Code)
	require.Empty(t, GetOpsStreamErrors(c))
}
