package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type handoffConcurrencyCache struct {
	ConcurrencyCache
	activeSlots     int
	waiting         int
	startupCleanups int
}

func (c *handoffConcurrencyCache) CleanupStaleProcessSlots(context.Context, string) error {
	c.startupCleanups++
	c.activeSlots = 0
	c.waiting = 0
	return nil
}

func TestProvideConcurrencyServicePreservesInFlightHandoff(t *testing.T) {
	for _, preserve := range []bool{true, false} {
		cache := &handoffConcurrencyCache{activeSlots: 3, waiting: 2}
		cfg := &config.Config{Gateway: config.GatewayConfig{PreserveConcurrencyOnStartup: preserve}}
		require.NotNil(t, ProvideConcurrencyService(cache, nil, cfg))
		if preserve {
			require.Equal(t, 3, cache.activeSlots)
			require.Equal(t, 2, cache.waiting)
			require.Zero(t, cache.startupCleanups)
		} else {
			require.Equal(t, 1, cache.startupCleanups)
		}
	}
}
