package service

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Opt-in wall-clock regression: no provider, credentials or production traffic.
// The exact Forward path waits beyond both the old 120s body deadline and a
// 125s proxy silence limit, while its downstream receives flushed comments.
func TestAntigravityCompatHeaderWaitBeyond125Seconds(t *testing.T) {
	if os.Getenv("SUB2API_HEADER_LONG_TEST") != "1" {
		t.Skip("opt-in 130-second header-stall acceptance")
	}
	gin.SetMode(gin.TestMode)
	raw := []byte(`{"model":"gemini-3.1-pro-high","stream":true,"messages":[{"role":"user","content":"inert long-stall fixture"}]}`)
	upstream := antigravityHeaderHTTPFunc{do: func(r *http.Request) (*http.Response, error) {
		select {
		case <-time.After(130 * time.Second):
			return antigravityCompatSuccessResponse(), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}}
	svc := newAntigravityCompatService(config.GatewayConfig{}, upstream)
	outcome := make(chan error, 1)
	router := gin.New()
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		_, err := svc.ForwardAsChatCompletions(c.Request.Context(), c, newAntigravityCompatAccount(AccountTypeOAuth), raw, nil)
		outcome <- err
	})
	server := httptest.NewServer(router)
	defer server.Close()
	req, err := http.NewRequestWithContext(context.Background(), "POST", server.URL+"/v1/chat/completions", strings.NewReader(string(raw)))
	require.NoError(t, err)
	started := time.Now()
	resp, err := (&http.Client{Timeout: 150 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	headers := time.Since(started)
	require.Less(t, headers, 20*time.Second)
	scan := bufio.NewScanner(resp.Body)
	beats := 0
	done := false
	last := started
	maxGap := time.Duration(0)
	for scan.Scan() {
		line := scan.Text()
		if strings.HasPrefix(line, ":") {
			now := time.Now()
			if gap := now.Sub(last); gap > maxGap {
				maxGap = gap
			}
			last = now
			beats++
		}
		if line == "data: [DONE]" {
			done = true
		}
	}
	require.NoError(t, scan.Err())
	require.NoError(t, <-outcome)
	require.True(t, done)
	require.GreaterOrEqual(t, beats, 8)
	require.Less(t, maxGap, 20*time.Second)
	t.Logf("headers=%s elapsed=%s heartbeats=%d max_gap=%s final_done=%v provider_calls=1", headers, time.Since(started), beats, maxGap, done)
	_, _ = io.Copy(io.Discard, resp.Body)
}
