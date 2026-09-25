package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPiDebugIsExplicitAndCannotOverlapRoleAuth(t *testing.T) {
	p := validPiConfig()
	before := p.Agent.Pi.Limits
	require.Empty(t, p.Agent.Pi.DebugDir)
	require.NoError(t, p.ValidatePi())
	p.Agent.Pi.DebugDir = "/private/debug"
	require.NoError(t, p.ValidatePi())
	require.Equal(t, before, p.Agent.Pi.Limits)
	for _, path := range []string{"relative", "/", "/private/../debug", "/private/role", "/private", "/private/role/debug"} {
		p.Agent.Pi.DebugDir = path
		require.Error(t, p.ValidatePi(), path)
	}
}
