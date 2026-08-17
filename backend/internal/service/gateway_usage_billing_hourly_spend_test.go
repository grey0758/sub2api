package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildUsageBillingCommandHourlySpendCost(t *testing.T) {
	account := &Account{
		ID: 17,
		Extra: map[string]any{
			HourlySpendLimitEnabledExtraKey: true,
			HourlySpendLimitUSDExtraKey:     100.0,
		},
	}
	params := &postUsageBillingParams{
		Cost:                  &CostBreakdown{TotalCost: 2},
		User:                  &User{ID: 1},
		APIKey:                &APIKey{ID: 2},
		Account:               account,
		AccountRateMultiplier: 1.5,
	}

	cmd := buildUsageBillingCommand("req-hourly-default", &UsageLog{}, params)
	require.NotNil(t, cmd)
	require.Equal(t, 3.0, cmd.HourlySpendCost)

	zeroCost := 0.0
	cmd = buildUsageBillingCommand("req-hourly-zero", &UsageLog{AccountStatsCost: &zeroCost}, params)
	require.NotNil(t, cmd)
	require.Equal(t, 3.0, cmd.HourlySpendCost)

	customCost := 4.25
	cmd = buildUsageBillingCommand("req-hourly-custom", &UsageLog{AccountStatsCost: &customCost}, params)
	require.NotNil(t, cmd)
	require.Equal(t, customCost, cmd.HourlySpendCost)
}

func TestBuildUsageBillingCommandSkipsDisabledHourlySpend(t *testing.T) {
	cmd := buildUsageBillingCommand("req-hourly-disabled", &UsageLog{}, &postUsageBillingParams{
		Cost:                  &CostBreakdown{TotalCost: 2},
		User:                  &User{ID: 1},
		APIKey:                &APIKey{ID: 2},
		Account:               &Account{ID: 17},
		AccountRateMultiplier: 1.5,
	})
	require.NotNil(t, cmd)
	require.Zero(t, cmd.HourlySpendCost)
}

func TestBuildUsageBillingCommandHourlySpendOnlySkipsOtherBilling(t *testing.T) {
	cmd := buildUsageBillingCommand("req-hourly-only", &UsageLog{}, &postUsageBillingParams{
		Cost:                  &CostBreakdown{TotalCost: 2, ActualCost: 4},
		User:                  &User{ID: 1},
		APIKey:                &APIKey{ID: 2, Quota: 10},
		Account:               &Account{ID: 17, Extra: map[string]any{HourlySpendLimitEnabledExtraKey: true, HourlySpendLimitUSDExtraKey: 100.0}},
		AccountRateMultiplier: 1.5,
		HourlySpendOnly:       true,
	})

	require.NotNil(t, cmd)
	require.Equal(t, 3.0, cmd.HourlySpendCost)
	require.Zero(t, cmd.BalanceCost)
	require.Zero(t, cmd.SubscriptionCost)
	require.Zero(t, cmd.APIKeyQuotaCost)
	require.Zero(t, cmd.APIKeyRateLimitCost)
	require.Zero(t, cmd.AccountQuotaCost)
}
