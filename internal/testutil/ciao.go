// Package testutil binds opt-in cross-repository rehearsals to exact source.
package testutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func CiaoSource(t *testing.T) string {
	t.Helper()
	root, expected := os.Getenv("CIAO_MAINTENANCE_REPO"), os.Getenv("CIAO_MAINTENANCE_REVISION")
	require.True(t, filepath.IsAbs(root), "explicit immutable Ciao checkout required")
	require.Len(t, expected, 40, "set CIAO_MAINTENANCE_REVISION to the reviewed commit")
	git := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"-C", root, "-c", "core.fsmonitor=false"}, args...)...)
		command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		out, err := command.Output()
		require.NoError(t, err)
		return strings.TrimSpace(string(out))
	}
	require.Equal(t, expected, git("rev-parse", "HEAD"))
	require.Empty(t, git("status", "--porcelain", "--untracked-files=normal", "--", "scripts/vendor_maintenance_lib"), "do not test an unreported mutable importer")
	t.Logf("Ciao importer source=%s", expected)
	return root
}
