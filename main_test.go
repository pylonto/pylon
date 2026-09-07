package main

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPiEmbeddedImageMatchesTheReviewedBuildContext(t *testing.T) {
	// The Docker allowlist owns the context; the binary must not ship a stale,
	// independently maintained subset when a required runtime file is added.
	raw, err := agentFS.ReadFile("agent/pi/.dockerignore")
	require.NoError(t, err)
	allowed := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "!") {
			name := strings.TrimPrefix(line, "!")
			require.NotContains(t, name, "/")
			allowed[name] = true
			info, err := fs.Stat(agentFS, "agent/pi/"+name)
			require.NoError(t, err, name)
			require.True(t, info.Mode().IsRegular(), name)
		}
	}
	require.NotEmpty(t, allowed)
	entries, err := agentFS.ReadDir("agent/pi")
	require.NoError(t, err)
	require.Len(t, entries, len(allowed))
	for _, entry := range entries {
		require.True(t, allowed[entry.Name()], entry.Name())
	}
}
