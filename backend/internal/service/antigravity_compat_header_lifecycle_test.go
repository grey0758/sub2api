package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type antigravityHeaderHTTPFunc struct {
	HTTPUpstream
	do func(*http.Request) (*http.Response, error)
}

func (f antigravityHeaderHTTPFunc) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return f.do(r)
}

type antigravityHeaderCloseObserver struct {
	io.Reader
	once   sync.Once
	closed chan struct{}
}

func (b *antigravityHeaderCloseObserver) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestAntigravityHeaderWaitNetworkAndBodyLifetime(t *testing.T) {
	for _, protocol := range []antigravityCompatProtocol{antigravityCompatChatCompletions, antigravityCompatResponses} {
		t.Run(string(rune('0'+protocol)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(35 * time.Millisecond)
				_, _ = io.WriteString(w, "fixture-body")
			}))
			defer server.Close()
			c, rec := newAntigravityCompatContext("POST", "/", nil)
			c.Set(antigravityCompatStreamProtocolKey, protocol)
			upstream := antigravityHeaderHTTPFunc{do: func(r *http.Request) (*http.Response, error) { return http.DefaultClient.Do(r) }}
			u := &antigravityCompatHeaderKeepaliveUpstream{HTTPUpstream: upstream, c: c, interval: 5 * time.Millisecond}
			req, err := http.NewRequestWithContext(context.Background(), "GET", server.URL, nil)
			require.NoError(t, err)
			resp, err := u.Do(req, "", 1, 1)
			require.NoError(t, err)
			require.Contains(t, rec.Body.String(), ": ping\n\n")
			require.Equal(t, -1, AntigravityCompatHeaderKeepaliveWrittenSize(c))
			payload, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, "fixture-body", string(payload))
			require.NoError(t, resp.Body.Close())
			_ = (&AntigravityGatewayService{}).writeAntigravityCompatError(c, 502, "upstream_error", "fixture failure")
			require.Equal(t, 200, rec.Code)
			require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
			if protocol == antigravityCompatResponses {
				require.Contains(t, rec.Body.String(), "event: error\n")
			}
			require.Equal(t, 1, strings.Count(rec.Body.String(), "fixture failure"))
			require.True(t, IsResponseCommitted(c))
		})
	}
}

func TestAntigravityHeaderWaitCancellationClosesLateResponse(t *testing.T) {
	c, _ := newAntigravityCompatContext("POST", "/", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	late := &antigravityHeaderCloseObserver{Reader: strings.NewReader("late"), closed: make(chan struct{})}
	upstream := antigravityHeaderHTTPFunc{do: func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return &http.Response{StatusCode: 200, Body: late}, nil
	}}
	u := &antigravityCompatHeaderKeepaliveUpstream{HTTPUpstream: upstream, c: c, interval: time.Hour}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://fixture.invalid", nil)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := u.Do(req, "", 1, 1); done <- err }()
	<-entered
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	close(release)
	select {
	case <-late.closed:
	case <-time.After(time.Second):
		t.Fatal("late response leaked after cancellation")
	}
}

func TestAntigravityHeaderWaitFastErrorsKeepJSONAndStatus(t *testing.T) {
	c, rec := newAntigravityCompatContext("POST", "/", nil)
	c.Set(antigravityCompatStreamProtocolKey, antigravityCompatResponses)
	upstream := antigravityHeaderHTTPFunc{do: func(r *http.Request) (*http.Response, error) { return nil, errors.New("fixture") }}
	u := &antigravityCompatHeaderKeepaliveUpstream{HTTPUpstream: upstream, c: c, interval: time.Second}
	req, _ := http.NewRequest("GET", "https://fixture.invalid", nil)
	_, err := u.Do(req, "", 1, 1)
	require.Error(t, err)
	_ = (&AntigravityGatewayService{}).writeAntigravityCompatError(c, 400, "invalid_request_error", "fixture")
	require.Equal(t, 400, rec.Code)
	require.NotContains(t, rec.Body.String(), "data:")
	require.False(t, AntigravityCompatHasHeaderKeepalive(c))
}
