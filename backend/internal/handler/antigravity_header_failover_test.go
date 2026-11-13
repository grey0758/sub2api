package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAntigravityHeaderHeartbeatDoesNotBlockFailoverOrHideExhaustion(t *testing.T) {
	for _, protocol := range []string{"chat", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/"+protocol, nil)
			before := service.AntigravityCompatHeaderKeepaliveWrittenSize(c)
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			n, err := c.Writer.Write([]byte(": ping\n\n"))
			require.NoError(t, err)
			c.Writer.Flush()
			c.Set("antigravity_compat_header_heartbeat_bytes", n)
			require.Equal(t, before, service.AntigravityCompatHeaderKeepaliveWrittenSize(c), "quota errors must remain eligible for account failover")
			h := &GatewayHandler{}
			last := &service.UpstreamFailoverError{StatusCode: 429}
			if protocol == "chat" {
				h.handleCCFailoverExhausted(c, last, true)
			} else {
				h.handleResponsesFailoverExhausted(c, last, true)
			}
			require.Equal(t, 200, rec.Code)
			if protocol == "chat" {
				require.Contains(t, rec.Body.String(), "data: {\"error\"")
			} else {
				require.Contains(t, rec.Body.String(), "response.failed")
			}
			once := rec.Body.String()
			if protocol == "chat" {
				h.handleCCFailoverExhausted(c, last, true)
			} else {
				h.handleResponsesFailoverExhausted(c, last, true)
			}
			require.Equal(t, once, rec.Body.String(), "do not append duplicate terminal errors")
			require.Greater(t, service.AntigravityCompatHeaderKeepaliveWrittenSize(c), 0)
			require.NotContains(t, strings.TrimPrefix(rec.Body.String(), ": ping\n\n"), "[DONE]")
		})
	}
}
