//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestKrillCentralSimpleModeIsolation(t *testing.T) {
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.KrillStrictGroupIsolation = true
	cfg.Gateway.KrillCompatibilityGroupID = 8
	cfg.Gateway.KrillCompatibilityGroupIDs = []int64{9}
	a := Account{ID: 11, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, AccountGroups: []AccountGroup{{GroupID: 8}}}
	b := Account{ID: 12, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, AccountGroups: []AccountGroup{{GroupID: 9}}}
	repo := groupAwareStubOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: []Account{a, b}}}
	s := &OpenAIGatewayService{cfg: cfg, accountRepo: repo}
	for _, id := range []int64{8, 9} {
		accounts, err := s.listSchedulableAccounts(context.Background(), &id, PlatformOpenAI)
		require.NoError(t, err)
		require.Len(t, accounts, 1)
		require.Equal(t, id+3, accounts[0].ID)
		require.Equal(t, id == 8, s.openAIAccountMatchesSchedulingGroup(&a, &id))
		require.Equal(t, id == 9, s.openAIAccountMatchesSchedulingGroup(&b, &id))
	}
	outside := int64(7)
	require.True(t, s.openAIAccountMatchesSchedulingGroup(&a, &outside))
	cfg.Gateway.KrillStrictGroupIsolation = false
	group := int64(9)
	require.True(t, s.openAIAccountMatchesSchedulingGroup(&a, &group))
}
