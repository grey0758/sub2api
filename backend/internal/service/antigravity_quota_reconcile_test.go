package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func quotaReconcileFixture() (*Account, *UsageInfo, time.Time) {
	now := time.Now().UTC().Truncate(time.Second)
	limit := func(at, reset time.Time) map[string]any {
		return map[string]any{"rate_limited_at": at.Format(time.RFC3339), "rate_limit_reset_at": reset.Format(time.RFC3339)}
	}
	a := &Account{ID: 251, Platform: PlatformAntigravity, Status: StatusActive, Schedulable: true, Extra: map[string]any{"unrelated": "keep", modelRateLimitsKey: map[string]any{
		antigravityGeminiModelRateLimitKey: limit(now.Add(-24*time.Hour), now.Add(6*24*time.Hour)),
		"gemini-4-argon-low":               limit(now.Add(-24*time.Hour), now.Add(6*24*time.Hour)),
		"claude-sonnet":                    limit(now.Add(-24*time.Hour), now.Add(6*24*time.Hour)),
		"gemini-3.1-flash-image":           limit(now.Add(-24*time.Hour), now.Add(6*24*time.Hour)),
	}}}
	reset := now.Add(time.Hour)
	u := &UsageInfo{SubscriptionTier: "PRO", AntigravityQuota: map[string]*AntigravityModelQuota{"gemini-3.8-flash-high": {Utilization: 0}, "gemini-4-argon-low": {Utilization: 70}, "gemini-4-argon-high": {Utilization: 70}}, AntigravityQuotaGroups: []*AntigravityQuotaGroup{
		{Key: "gemini", Windows: []*AntigravityQuotaWindow{{Window: "5h", Utilization: 0, ResetsAt: &reset}, {Window: "weekly", Utilization: 0.2, ResetsAt: &reset}}},
		{Key: "gemini-argon-wtu", Windows: []*AntigravityQuotaWindow{{Window: "weekly", Utilization: 70, ResetsAt: &reset}}},
	}}
	return a, u, now
}

func TestAntigravityQuotaReconcilePreservesUnrelatedAndScopesArgon(t *testing.T) {
	a, u, now := quotaReconcileFixture()
	expected, next, separate, changed := antigravityQuotaReconciliation(a, u, now)
	require.True(t, changed)
	require.True(t, separate)
	require.Len(t, expected, 4)
	require.Len(t, next, 2)
	require.Contains(t, next, "claude-sonnet")
	require.Contains(t, next, "gemini-3.1-flash-image")
	require.Len(t, a.Extra[modelRateLimitsKey], 4, "input scheduler snapshot is immutable")
	a.Extra = map[string]any{modelRateLimitsKey: next, antigravitySeparateArgonQuotaKey: true}
	require.Equal(t, []string{"gemini-4-argon-high", antigravityArgonModelRateLimitKey}, a.modelRateLimitKeysForRequest(context.Background(), "gemini-4-argon-high"))
	require.Equal(t, []string{"gemini-3.8-flash-high", antigravityGeminiModelRateLimitKey}, a.modelRateLimitKeysForRequest(context.Background(), "gemini-3.8-flash-high"))
	setAccountModelRateLimitSnapshot(a, antigravityArgonModelRateLimitKey, now.Add(time.Hour), "", now)
	require.True(t, a.isModelRateLimitedWithContext(context.Background(), "gemini-4-argon-low"))
	require.False(t, a.isModelRateLimitedWithContext(context.Background(), "gemini-3.8-flash-high"))
}

func TestAntigravityQuotaReconcileRejectsUnsafeEvidence(t *testing.T) {
	for _, tc := range []string{"free", "forbidden", "error", "manual_hold", "wrong_platform", "missing_summary", "missing_five_hour", "exhausted_week", "exhausted_argon", "exhausted_model", "invalid_fraction", "short_cooldown", "recent_cooldown", "explicit_reason"} {
		t.Run(tc, func(t *testing.T) {
			a, u, now := quotaReconcileFixture()
			limits := a.Extra[modelRateLimitsKey].(map[string]any)
			switch tc {
			case "free":
				u.SubscriptionTier = "FREE"
			case "forbidden":
				u.IsForbidden = true
			case "error":
				u.ErrorCode = "network_error"
			case "manual_hold":
				a.Schedulable = false
			case "wrong_platform":
				a.Platform = PlatformOpenAI
			case "missing_summary":
				u.AntigravityQuotaGroups = nil
			case "missing_five_hour":
				u.AntigravityQuotaGroups[0].Windows = u.AntigravityQuotaGroups[0].Windows[1:]
			case "exhausted_week":
				u.AntigravityQuotaGroups[0].Windows[1].Utilization = 100
				u.AntigravityQuotaGroups[1].Windows[0].Utilization = 100
			case "exhausted_argon":
				u.AntigravityQuotaGroups[1].Windows[0].Utilization = 100
			case "exhausted_model":
				u.AntigravityQuota["gemini-4-argon-low"].Utilization = 100
			case "invalid_fraction":
				u.AntigravityQuotaGroups[1].Windows[0].Utilization = math.NaN()
			case "short_cooldown":
				for _, k := range []string{antigravityGeminiModelRateLimitKey, "gemini-4-argon-low"} {
					limits[k] = map[string]any{"rate_limited_at": now.Add(-10 * time.Minute).Format(time.RFC3339), "rate_limit_reset_at": now.Add(10 * time.Minute).Format(time.RFC3339)}
				}
			case "recent_cooldown":
				for _, k := range []string{antigravityGeminiModelRateLimitKey, "gemini-4-argon-low"} {
					limits[k].(map[string]any)["rate_limited_at"] = now.Add(-time.Minute).Format(time.RFC3339)
				}
			case "explicit_reason":
				for _, k := range []string{antigravityGeminiModelRateLimitKey, "gemini-4-argon-low"} {
					limits[k].(map[string]any)["reason"] = "capacity"
				}
			}
			_, next, _, changed := antigravityQuotaReconciliation(a, u, now)
			if changed {
				require.Contains(t, next, antigravityGeminiModelRateLimitKey)
				require.Contains(t, next, "gemini-4-argon-low")
			} // no unsafe clears even if a scope flag is learned
			if next != nil {
				require.Len(t, next, 4)
			}
		})
	}
}

type quotaReconcileRepoStub struct {
	AccountRepository
	expected, next map[string]any
	calls          int
	separate       bool
}

func (r *quotaReconcileRepoStub) ReconcileAntigravityQuota(_ context.Context, _ int64, expected, next map[string]any, separate bool) (bool, error) {
	r.expected = expected
	r.next = next
	r.calls++
	r.separate = separate
	return true, nil
}
func TestAntigravityQuotaReconcileUsesNarrowCASCapability(t *testing.T) {
	a, u, now := quotaReconcileFixture()
	r := &quotaReconcileRepoStub{}
	s := &AccountUsageService{accountRepo: r}
	s.reconcileAntigravityQuota(context.Background(), a, u, now)
	require.Equal(t, 1, r.calls)
	require.Len(t, r.expected, 4)
	require.Len(t, r.next, 2)
	require.True(t, r.separate)
}
