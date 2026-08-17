package service

import (
	"encoding/json"
	"errors"
	"math"
	"time"
)

const (
	HourlySpendLimitEnabledExtraKey = "hourly_spend_limit_enabled"
	HourlySpendLimitUSDExtraKey     = "hourly_spend_limit_usd"
	HourlySpendUsedUSDExtraKey      = "hourly_spend_used_usd"
	HourlySpendWindowStartExtraKey  = "hourly_spend_window_started_at"
	HourlySpendWindowEndExtraKey    = "hourly_spend_window_ends_at"
)

var HourlySpendRuntimeExtraKeys = []string{
	HourlySpendUsedUSDExtraKey,
	HourlySpendWindowStartExtraKey,
	HourlySpendWindowEndExtraKey,
}

type HourlySpendState struct {
	Enabled         bool
	LimitUSD        float64
	UsedUSD         float64
	WindowStartedAt *time.Time
	WindowEndsAt    *time.Time
	LimitReached    bool
}

func (a *Account) HourlySpendLimitEnabled() bool {
	return a != nil && a.getExtraBool(HourlySpendLimitEnabledExtraKey) && a.HourlySpendLimitUSD() > 0
}

func (a *Account) HourlySpendLimitUSD() float64 {
	if a == nil {
		return 0
	}
	return a.getExtraFloat64(HourlySpendLimitUSDExtraKey)
}

func (a *Account) HourlySpendStateAt(now time.Time) HourlySpendState {
	state := HourlySpendState{
		Enabled:  a.HourlySpendLimitEnabled(),
		LimitUSD: a.HourlySpendLimitUSD(),
	}
	if !state.Enabled || a == nil {
		return state
	}

	windowStart := a.getExtraTime(HourlySpendWindowStartExtraKey)
	windowEnd := a.getExtraTime(HourlySpendWindowEndExtraKey)
	if windowStart.IsZero() || windowEnd.IsZero() || !now.Before(windowEnd) {
		return state
	}

	state.UsedUSD = a.getExtraFloat64(HourlySpendUsedUSDExtraKey)
	state.WindowStartedAt = &windowStart
	state.WindowEndsAt = &windowEnd
	state.LimitReached = state.UsedUSD >= state.LimitUSD
	return state
}

func (a *Account) IsHourlySpendLimitExceededAt(now time.Time) bool {
	return a.HourlySpendStateAt(now).LimitReached
}

func ValidateHourlySpendLimitExtra(extra map[string]any) error {
	if extra == nil {
		return nil
	}

	enabled := false
	if raw, exists := extra[HourlySpendLimitEnabledExtraKey]; exists {
		value, ok := raw.(bool)
		if !ok {
			return errors.New("hourly_spend_limit_enabled must be a boolean")
		}
		enabled = value
	}

	limit := 0.0
	if raw, exists := extra[HourlySpendLimitUSDExtraKey]; exists {
		value, ok := hourlySpendNumeric(raw)
		if !ok || value < 0 {
			return errors.New("hourly_spend_limit_usd must be a finite number greater than or equal to 0")
		}
		limit = value
	}
	if enabled && limit <= 0 {
		return errors.New("hourly_spend_limit_usd must be greater than 0 when the hourly spend limit is enabled")
	}
	return nil
}

func ClearHourlySpendRuntime(extra map[string]any) {
	for _, key := range HourlySpendRuntimeExtraKeys {
		delete(extra, key)
	}
}

func hourlySpendNumeric(raw any) (float64, bool) {
	var value float64
	switch typed := raw.(type) {
	case float64:
		value = typed
	case float32:
		value = float64(typed)
	case int:
		value = float64(typed)
	case int32:
		value = float64(typed)
	case int64:
		value = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		value = parsed
	default:
		return 0, false
	}
	return value, !math.IsNaN(value) && !math.IsInf(value, 0)
}
