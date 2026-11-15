//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAntigravityQuotaReconcileCASPreservesAccountAndRejectsNewLimit(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	limits := map[string]any{"antigravity:gemini": map[string]any{"rate_limited_at": "2026-11-14T06:00:00Z", "rate_limit_reset_at": "2026-11-21T00:00:00Z"}, "claude-sonnet": map[string]any{"rate_limit_reset_at": "2026-11-21T00:00:00Z"}}
	a, err := tx.Client().Account.Create().SetName("quota-reconcile-cas").SetPlatform(service.PlatformAntigravity).SetType(service.AccountTypeOAuth).SetCredentials(map[string]any{"refresh_token": "fixture-never-change"}).SetStatus(service.StatusActive).SetSchedulable(true).SetExtra(map[string]any{"unrelated": "keep", "model_rate_limits": limits}).Save(ctx)
	require.NoError(t, err)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	// A concurrent 429 replaces the model map after the caller observed it.
	require.NoError(t, repo.SetModelRateLimit(ctx, a.ID, "gemini-4-argon-high", time.Now().Add(time.Hour)))
	applied, err := repo.ReconcileAntigravityQuota(ctx, a.ID, limits, map[string]any{}, true)
	require.NoError(t, err)
	require.False(t, applied)
	current, err := tx.Client().Account.Get(ctx, a.ID)
	require.NoError(t, err)
	expected := current.Extra["model_rate_limits"].(map[string]any)
	next := map[string]any{"claude-sonnet": expected["claude-sonnet"], "gemini-4-argon-high": expected["gemini-4-argon-high"]}
	applied, err = repo.ReconcileAntigravityQuota(ctx, a.ID, expected, next, true)
	require.NoError(t, err)
	require.True(t, applied)
	after, err := tx.Client().Account.Get(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, current.Credentials, after.Credentials)
	require.Equal(t, current.Status, after.Status)
	require.Equal(t, current.Schedulable, after.Schedulable)
	require.Equal(t, "keep", after.Extra["unrelated"])
	require.Equal(t, true, after.Extra["antigravity_separate_argon_quota"])
	want, _ := json.Marshal(next)
	got, _ := json.Marshal(after.Extra["model_rate_limits"])
	require.JSONEq(t, string(want), string(got))
}
