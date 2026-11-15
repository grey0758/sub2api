package service

import (
	"context"
	"log/slog"
	"maps"
	"math"
	"strings"
	"time"
)

const antigravitySeparateArgonQuotaKey = "antigravity_separate_argon_quota"
const antigravityArgonModelRateLimitKey = "antigravity:gemini-argon"

// Optional narrow repository capability: compare the complete observed limit map
// before changing only that map and the upstream-confirmed quota-family flag.
type antigravityQuotaReconciler interface {
	ReconcileAntigravityQuota(context.Context, int64, map[string]any, map[string]any, bool) (bool, error)
}

func isAntigravityArgonModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gemini-4-argon")
}

func separateAntigravityArgonQuota(a *Account) bool {
	if a == nil {
		return false
	}
	separate, _ := a.Extra[antigravitySeparateArgonQuotaKey].(bool)
	return separate
}

func antigravityModelRateLimitKeysForAccount(a *Account, model string) []string {
	keys := antigravityModelRateLimitKeys(model)
	if isAntigravityArgonModel(model) {
		if separateAntigravityArgonQuota(a) {
			return []string{strings.TrimSpace(model), antigravityArgonModelRateLimitKey}
		}
		// Preserve a previously observed Argon limit if a plan later changes.
		if a != nil && a.modelRateLimitResetAt(antigravityArgonModelRateLimitKey) != nil {
			keys = append(keys, antigravityArgonModelRateLimitKey)
		}
	}
	return keys
}

func antigravityQuotaGroupAvailable(u *UsageInfo, key string, now time.Time, fiveHour bool) bool {
	for _, group := range u.AntigravityQuotaGroups {
		if group == nil || group.Key != key {
			continue
		}
		weekly, five := false, false
		for _, w := range group.Windows {
			if w == nil || math.IsNaN(w.Utilization) || math.IsInf(w.Utilization, 0) || w.Utilization < 0 || w.Utilization >= 100 || w.ResetsAt == nil || !w.ResetsAt.After(now) {
				return false
			}
			switch w.Window {
			case "weekly":
				weekly = true
			case "5h":
				five = true
			default:
				return false
			}
		}
		return weekly && (!fiveHour || five)
	}
	return false
}

func antigravityQuotaReconciliation(a *Account, u *UsageInfo, started time.Time) (map[string]any, map[string]any, bool, bool) {
	if a == nil || a.Platform != PlatformAntigravity || a.Status != StatusActive || !a.Schedulable || u == nil || u.IsForbidden || u.IsBanned || u.NeedsVerify || u.ErrorCode != "" || (u.SubscriptionTier != "PRO" && u.SubscriptionTier != "ULTRA") {
		return nil, nil, false, false
	}
	// Do not infer availability from an absent summary, the display fallback, or a
	// FREE account's nominal 100% (which can still reject actual generation).
	general := antigravityQuotaGroupAvailable(u, "gemini", started, true)
	separate := false
	for _, group := range u.AntigravityQuotaGroups {
		if group != nil && group.Key == "gemini-argon-wtu" && u.SubscriptionTier == "PRO" {
			separate = true
		}
	}
	argon := general
	if separate {
		argon = antigravityQuotaGroupAvailable(u, "gemini-argon-wtu", started, false)
	}
	if !general {
		return nil, nil, false, false
	}
	expected, _ := a.Extra[modelRateLimitsKey].(map[string]any)
	if expected == nil {
		expected = map[string]any{}
	}
	next := maps.Clone(expected)
	modelAvailable := func(model string) bool {
		q := u.AntigravityQuota[model]
		return q != nil && q.Utilization >= 0 && q.Utilization < 100
	}
	allModelsAvailable := general && argon
	observed := 0
	for model := range u.AntigravityQuota {
		if isAntigravityGeminiModel(model) && !isAntigravityGeminiImageModel(model) {
			observed++
			if !modelAvailable(model) {
				allModelsAvailable = false
			}
		}
	}
	allModelsAvailable = allModelsAvailable && observed > 0
	for key, raw := range expected {
		limit, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		atRaw, _ := limit["rate_limited_at"].(string)
		resetRaw, _ := limit["rate_limit_reset_at"].(string)
		reason, _ := limit["reason"].(string)
		at, e1 := time.Parse(time.RFC3339, atRaw)
		reset, e2 := time.Parse(time.RFC3339, resetRaw)
		// Keep recent/short limits (RPM, capacity, retries), explicit non-quota
		// reasons, expired entries, and malformed records unchanged.
		if e1 != nil || e2 != nil || reason != "" || !reset.After(started) || reset.Sub(at) < time.Hour || at.After(started.Add(-5*time.Minute)) {
			continue
		}
		available := false
		switch key {
		case antigravityGeminiModelRateLimitKey:
			available = allModelsAvailable
		case antigravityArgonModelRateLimitKey:
			available = argon && allModelsAvailable
		default:
			if isAntigravityGeminiModel(key) && !isAntigravityGeminiImageModel(key) {
				available = general && modelAvailable(key)
				if isAntigravityArgonModel(key) {
					available = argon && modelAvailable(key)
				}
			}
		}
		if available {
			delete(next, key)
		}
	}
	// Never reinterpret an unresolved legacy shared cooldown as a separate pool.
	if _, retained := next[antigravityGeminiModelRateLimitKey]; retained && a.isRateLimitActiveForKey(antigravityGeminiModelRateLimitKey) && !separateAntigravityArgonQuota(a) {
		separate = false
	}
	changed := len(next) != len(expected) || separate != separateAntigravityArgonQuota(a)
	return expected, next, separate, changed
}

func (s *AccountUsageService) reconcileAntigravityQuota(ctx context.Context, a *Account, u *UsageInfo, started time.Time) {
	repo, ok := s.accountRepo.(antigravityQuotaReconciler)
	if !ok {
		return
	}
	expected, next, separate, changed := antigravityQuotaReconciliation(a, u, started)
	if !changed {
		return
	}
	applied, err := repo.ReconcileAntigravityQuota(ctx, a.ID, expected, next, separate)
	if err != nil {
		slog.Warn("antigravity quota reconciliation failed", "account_id", a.ID, "error", err)
		return
	}
	if applied {
		slog.Info("antigravity quota reconciled", "account_id", a.ID, "cleared_limits", len(expected)-len(next), "separate_argon", separate)
	}
}
