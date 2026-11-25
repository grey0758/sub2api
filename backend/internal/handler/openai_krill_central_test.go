package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestKrillCentralExplicitGroups(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.KrillCompatibilityGroupID = 8
	cfg.Gateway.KrillCompatibilityGroupIDs = []int64{9, 0, -1, 9}
	for _, id := range []int64{-1, 0, 6, 7, 8, 9, 10} {
		key := &service.APIKey{GroupID: &id}
		require.Equal(t, id == 8 || id == 9, krillResponsesCompatibilityEnabled(cfg, key))
	}
	require.False(t, krillResponsesCompatibilityEnabled(nil, &service.APIKey{}))
	require.False(t, krillResponsesCompatibilityEnabled(cfg, nil))
	require.False(t, krillResponsesCompatibilityEnabled(cfg, &service.APIKey{}))
	require.False(t, krillResponsesCompatibilityEnabled(&config.Config{}, &service.APIKey{}))
}
