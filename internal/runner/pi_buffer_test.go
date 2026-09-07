package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Hide WriterTo so io.Copy must exercise the destination's complete method set.
type piReaderOnly struct{ io.Reader }

func TestPiBufferBoundsEveryCopyPath(t *testing.T) {
	for _, method := range []string{"write", "copy", "writer_to"} {
		for _, size := range []int{0, 1, 2, 9} {
			t.Run(method+"/"+strings.Repeat("x", size), func(t *testing.T) {
				var b piBuffer
				b.max = 1
				input := strings.Repeat("x", size)
				var err error
				switch method {
				case "write":
					_, err = b.Write([]byte(input))
				case "copy":
					_, err = io.Copy(&b, piReaderOnly{strings.NewReader(input)})
				case "writer_to":
					_, err = io.Copy(&b, strings.NewReader(input))
				}
				require.LessOrEqual(t, b.Len(), b.max)
				if size > b.max {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					require.Equal(t, input, string(b.Bytes()))
				}
			})
		}
	}
	var b piBuffer
	b.max = 3
	_, err := b.Write([]byte("ab"))
	require.NoError(t, err)
	_, err = io.Copy(&b, piReaderOnly{strings.NewReader("cd")})
	require.Error(t, err)
	require.Equal(t, "ab", string(b.Bytes()), "accumulated writes cannot evade the cap")
}

func TestPiGitOutputBoundUsesTheSharedWriter(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	git := func(args ...string) {
		t.Helper()
		_, err := piGit(ctx, home, repo, 4096, args...)
		require.NoError(t, err)
	}
	git("init", "--template=")
	file := filepath.Join(repo, "fixture.txt")
	require.NoError(t, os.WriteFile(file, []byte("before\n"), 0600))
	git("add", "--", "fixture.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "-m", "Fixture")
	require.NoError(t, os.WriteFile(file, []byte("after\n"), 0600))
	full, err := piGit(ctx, home, repo, 4096, "diff", "HEAD", "--")
	require.NoError(t, err)
	require.Greater(t, len(full), 1, "prove real Git has more output than the test cap")
	require.Equal(t, "nonempty", piDiffOutcome(full, err))
	bounded, observed, err := piGitWithStats(ctx, home, repo, 1, "diff", "HEAD", "--")
	require.ErrorIs(t, err, errPiOutputBound)
	require.Empty(t, bounded)
	require.LessOrEqual(t, observed, 1)
	require.Equal(t, "output_bound", piDiffOutcome(bounded, err))
	require.NoError(t, os.WriteFile(file, []byte("before\n"), 0600))
	empty, observed, err := piGitWithStats(ctx, home, repo, 4096, "diff", "HEAD", "--")
	require.NoError(t, err)
	require.Empty(t, empty)
	require.Zero(t, observed)
	require.Equal(t, "empty", piDiffOutcome(empty, err))
	failed, _, err := piGitWithStats(ctx, home, repo, 4096, "diff", "--pylon-invalid-option", "HEAD", "--")
	require.Error(t, err)
	require.Equal(t, "git_failed", piDiffOutcome(failed, err))
	canceled, stop := context.WithCancel(ctx)
	stop()
	require.ErrorIs(t, canceled.Err(), context.Canceled)
	failed, _, err = piGitWithStats(canceled, home, repo, 4096, "diff", "HEAD", "--")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "canceled", piDiffOutcome(failed, err))
	deadline, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer stop()
	require.ErrorIs(t, deadline.Err(), context.DeadlineExceeded)
	failed, _, err = piGitWithStats(deadline, home, repo, 4096, "diff", "HEAD", "--")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, "deadline", piDiffOutcome(failed, err))
}
