package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountHourlySpendState(t *testing.T) {
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	activeStart := now.Add(-30 * time.Minute)
	activeEnd := activeStart.Add(time.Hour)

	account := &Account{
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			HourlySpendLimitEnabledExtraKey: true,
			HourlySpendLimitUSDExtraKey:     100.0,
			HourlySpendUsedUSDExtraKey:      100.0,
			HourlySpendWindowStartExtraKey:  activeStart.Format(time.RFC3339),
			HourlySpendWindowEndExtraKey:    activeEnd.Format(time.RFC3339),
		},
	}

	state := account.HourlySpendStateAt(now)
	require.True(t, state.Enabled)
	require.Equal(t, 100.0, state.LimitUSD)
	require.Equal(t, 100.0, state.UsedUSD)
	require.True(t, state.LimitReached)
	require.Equal(t, activeStart, *state.WindowStartedAt)
	require.Equal(t, activeEnd, *state.WindowEndsAt)
	require.True(t, account.IsHourlySpendLimitExceededAt(now))

	expired := account.HourlySpendStateAt(activeEnd)
	require.Zero(t, expired.UsedUSD)
	require.False(t, expired.LimitReached)
	require.Nil(t, expired.WindowStartedAt)
	require.Nil(t, expired.WindowEndsAt)
}

func TestValidateHourlySpendLimitExtra(t *testing.T) {
	require.NoError(t, ValidateHourlySpendLimitExtra(nil))
	require.NoError(t, ValidateHourlySpendLimitExtra(map[string]any{
		HourlySpendLimitEnabledExtraKey: false,
		HourlySpendLimitUSDExtraKey:     0.0,
	}))
	require.NoError(t, ValidateHourlySpendLimitExtra(map[string]any{
		HourlySpendLimitEnabledExtraKey: true,
		HourlySpendLimitUSDExtraKey:     100.0,
	}))
	require.Error(t, ValidateHourlySpendLimitExtra(map[string]any{
		HourlySpendLimitEnabledExtraKey: "true",
		HourlySpendLimitUSDExtraKey:     100.0,
	}))
	require.Error(t, ValidateHourlySpendLimitExtra(map[string]any{
		HourlySpendLimitEnabledExtraKey: true,
		HourlySpendLimitUSDExtraKey:     0.0,
	}))
	require.Error(t, ValidateHourlySpendLimitExtra(map[string]any{
		HourlySpendLimitEnabledExtraKey: true,
		HourlySpendLimitUSDExtraKey:     "100",
	}))
}

func TestAccountIsSchedulableHonorsHourlySpendWindow(t *testing.T) {
	now := time.Now().UTC()
	account := &Account{
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			HourlySpendLimitEnabledExtraKey: true,
			HourlySpendLimitUSDExtraKey:     100.0,
			HourlySpendUsedUSDExtraKey:      100.0,
			HourlySpendWindowStartExtraKey:  now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
			HourlySpendWindowEndExtraKey:    now.Add(50 * time.Minute).Format(time.RFC3339Nano),
		},
	}
	require.False(t, account.IsSchedulable())

	account.Extra[HourlySpendWindowEndExtraKey] = now.Add(-time.Second).Format(time.RFC3339Nano)
	require.True(t, account.IsSchedulable())
}
