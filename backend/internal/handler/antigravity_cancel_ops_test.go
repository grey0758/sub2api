package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsGeminiCancelledHeartbeatIsNotRecovered(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 4)
	gin.SetMode(gin.TestMode)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		c.Set(opsModelKey, "gemini-4-argon-high")
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{AccountID: 254, Kind: "request_error", Message: "context canceled"}})
		c.Writer.Write([]byte(": ping\n\n"))
		c.Writer.Flush()
		ctx, cancel := context.WithCancel(c.Request.Context())
		cancel()
		c.Request = c.Request.WithContext(ctx)
		service.MarkOpsStreamFailure(c, "client_disconnected", "client_disconnected", "Client disconnected before upstream response", 499)
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Equal(t, 200, rec.Code)
	require.Equal(t, ": ping\n\n", rec.Body.String())
	require.Equal(t, int64(1), OpsErrorLogQueueLength())
	job := <-opsErrorLogQueue
	require.Equal(t, 499, job.entry.StatusCode)
	require.Equal(t, "api_error", job.entry.ErrorType) // The existing Ops taxonomy normalizes custom types.
	require.Contains(t, job.entry.ErrorBody, `"code":"client_disconnected"`)
	require.NotContains(t, job.entry.ErrorMessage, "Recovered")
	require.Equal(t, "Client disconnected before upstream response", job.entry.ErrorMessage)
}
