package service

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/imageinput"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// These Gemini adapters pass through Anthropic's inline-only image conversion.
// Resolve URLs first so a remote image cannot silently become a text-only call.
func resolveGeminiCompatImages(ctx context.Context, req *apicompat.ResponsesRequest) error {
	start := time.Now()
	input, stats, err := imageinput.Resolve(ctx, req.Input)
	if stats.Images > 0 || err != nil {
		fields := []zap.Field{zap.Int("image_count", stats.Images), zap.Int("remote_image_count", stats.Remote), zap.Int64("image_bytes", stats.Bytes), zap.Int64("duration_ms", time.Since(start).Milliseconds())}
		if err != nil {
			var e *imageinput.Error
			if errors.As(err, &e) {
				fields = append(fields, zap.String("error_code", e.Code), zap.Int("image_index", e.Index), zap.Int("status_code", e.Status))
			}
			logger.FromContext(ctx).Warn("compat_images.rejected_before_upstream", fields...)
		} else {
			logger.FromContext(ctx).Info("compat_images.resolved", fields...)
		}
	}
	if err == nil {
		req.Input = input
	}
	return err
}

func compatImageErrorStatus(err error) int {
	var e *imageinput.Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 400
}
