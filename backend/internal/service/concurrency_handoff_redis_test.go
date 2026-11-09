package service_test

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestConcurrencyHandoffRetainsLiveRedisSlotsAndQueue(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	ctx := context.Background()
	old := repository.NewConcurrencyCache(client, 1, 60)
	ok, err := old.AcquireAccountSlot(ctx, 159, 1, "old-worker:in-flight")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = old.AcquireUserSlot(ctx, 2, 1, "old-worker:in-flight")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = old.IncrementWaitCount(ctx, 2, 2)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = old.IncrementAccountWaitCount(ctx, 159, 2)
	require.NoError(t, err)
	require.True(t, ok)
	next := repository.NewConcurrencyCache(client, 1, 60)
	cfg := &config.Config{Gateway: config.GatewayConfig{PreserveConcurrencyOnStartup: true}}
	require.NotNil(t, service.ProvideConcurrencyService(next, nil, cfg))
	n, err := next.GetAccountConcurrency(ctx, 159)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	n, err = next.GetUserConcurrency(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	n, err = next.GetAccountWaitingCount(ctx, 159)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	loads, err := next.GetUsersLoadBatch(ctx, []service.UserWithConcurrency{{ID: 2, MaxConcurrency: 1}})
	require.NoError(t, err)
	require.Equal(t, 1, loads[2].WaitingCount)
	ok, err = next.AcquireAccountSlot(ctx, 159, 1, "new-worker:next")
	require.NoError(t, err)
	require.False(t, ok, "new worker must respect old in-flight capacity")
	require.NoError(t, old.ReleaseAccountSlot(ctx, 159, "old-worker:in-flight"))
	require.NoError(t, old.ReleaseUserSlot(ctx, 2, "old-worker:in-flight"))
	require.NoError(t, old.DecrementAccountWaitCount(ctx, 159))
	require.NoError(t, old.DecrementWaitCount(ctx, 2))
	ok, err = next.AcquireAccountSlot(ctx, 159, 1, "new-worker:next")
	require.NoError(t, err)
	require.True(t, ok, "old worker releases capacity normally")
	require.NoError(t, next.ReleaseAccountSlot(ctx, 159, "new-worker:next"))
}
