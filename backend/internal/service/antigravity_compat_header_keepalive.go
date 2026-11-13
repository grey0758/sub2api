package service

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const antigravityCompatHeaderHeartbeatBytesKey = "antigravity_compat_header_heartbeat_bytes"
const antigravityCompatStreamProtocolKey = "antigravity_compat_stream_protocol"

// Only the network call runs in the worker. All Gin/Writer access stays on the
// request goroutine, including heartbeats, error handling and account failover.
// This decorator never retries or changes the request body.
type antigravityCompatHeaderKeepaliveUpstream struct {
	HTTPUpstream
	c             *gin.Context
	interval      time.Duration
	cancelRequest context.CancelFunc
}

func (u *antigravityCompatHeaderKeepaliveUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	if u.interval <= 0 {
		return u.HTTPUpstream.Do(req, proxyURL, accountID, concurrency)
	}
	ctx, cancel := context.WithCancel(req.Context())
	type outcome struct {
		resp *http.Response
		err  error
	}
	results := make(chan outcome)
	abandoned := make(chan struct{})
	defer close(abandoned)
	go func() {
		resp, err := u.HTTPUpstream.Do(req.WithContext(ctx), proxyURL, accountID, concurrency)
		select {
		case results <- outcome{resp, err}:
		case <-abandoned:
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
		}
	}()
	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()
	for {
		select {
		case result := <-results:
			if result.err != nil || result.resp == nil {
				cancel()
				if result.resp != nil && result.resp.Body != nil {
					_ = result.resp.Body.Close()
				}
				return result.resp, result.err
			}
			if result.resp.Body == nil {
				cancel()
			} else {
				result.resp.Body = &antigravityCompatCancelBody{ReadCloser: result.resp.Body, cancel: cancel}
			}
			return result.resp, nil
		case <-ctx.Done():
			cancel()
			return nil, ctx.Err()
		case <-ticker.C:
			// Never write another heartbeat after a known downstream cancellation.
			if err := ctx.Err(); err != nil {
				cancel()
				return nil, err
			}
			if !u.c.Writer.Written() {
				u.c.Header("Content-Type", "text/event-stream")
				u.c.Header("Cache-Control", "no-cache")
				u.c.Header("Connection", "keep-alive")
				u.c.Header("X-Accel-Buffering", "no")
				u.c.Status(http.StatusOK)
			}
			n, err := u.c.Writer.Write([]byte(": ping\n\n"))
			previous, _ := u.c.Get(antigravityCompatHeaderHeartbeatBytesKey)
			count, _ := previous.(int)
			u.c.Set(antigravityCompatHeaderHeartbeatBytesKey, count+n)
			if err != nil {
				cancel()
				if u.cancelRequest != nil {
					u.cancelRequest()
				}
				return nil, err
			}
			u.c.Writer.Flush()
		}
	}
}

type antigravityCompatCancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *antigravityCompatCancelBody) Close() error { defer b.cancel(); return b.ReadCloser.Close() }

// Header-wait comments carry no generated output. Keep the existing quota
// failover behaviour even after HTTP 200 has been committed by a heartbeat.
func AntigravityCompatHeaderKeepaliveWrittenSize(c *gin.Context) int {
	if c == nil || c.Writer == nil {
		return -1
	}
	size := c.Writer.Size()
	v, _ := c.Get(antigravityCompatHeaderHeartbeatBytesKey)
	count, _ := v.(int)
	if count > 0 {
		size -= count
		if size <= 0 {
			return -1
		}
	}
	return size
}
func AntigravityCompatHasHeaderKeepalive(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, _ := c.Get(antigravityCompatHeaderHeartbeatBytesKey)
	count, _ := v.(int)
	return count > 0
}
func antigravityCompatCommittedStreamError(c *gin.Context, message string) error {
	protocol, _ := c.Get(antigravityCompatStreamProtocolKey)
	p, _ := protocol.(antigravityCompatProtocol)
	return writeAntigravityCompatRecoveryStreamError(c, p, message)
}
