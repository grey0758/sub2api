package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPreserveConcurrencyOnStartupEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("GATEWAY_PRESERVE_CONCURRENCY_ON_STARTUP", "true")
	cfg, err := Load()
	require.NoError(t, err)
	require.True(t, cfg.Gateway.PreserveConcurrencyOnStartup)
}
