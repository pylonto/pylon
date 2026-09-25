package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pylonto/pylon/internal/pidebug"
	"github.com/stretchr/testify/require"
)

func TestPiRequestedUnsafeOrFullDebugRefusesBeforeSourceOrRuntime(t *testing.T) {
	for _, kind := range []string{"public", "symlink", "full", "repository_overlap"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Chmod(root, 0700))
			patches := t.TempDir()
			require.NoError(t, os.Chmod(patches, 0700))
			p := PiParams{Pylon: "fixture", JobID: uuid.NewString(), Base: strings.Repeat("a", 40), Repository: filepath.Join(t.TempDir(), "absent-repository"), Config: piTestConfig(), PatchRoot: patches, Deadline: time.Now().Add(time.Minute), Fixture: true}
			p.Config.DebugDir = root
			var err error
			p.Brief, err = json.Marshal(map[string]any{"v": 1, "kind": "ciao.vendor.maintenance", "purpose": "repair", "source_revision": p.Base, "qualification_id": strings.Repeat("b", 64), "publication": map[string]string{"mode": "none"}, "contract": []string{"fixture"}, "report": map[string]bool{"fixture": true}})
			require.NoError(t, err)
			switch kind {
			case "public":
				require.NoError(t, os.Chmod(root, 0755))
			case "symlink":
				link := filepath.Join(t.TempDir(), "debug")
				require.NoError(t, os.Symlink(root, link))
				p.Config.DebugDir = link
			case "full":
				for range pidebug.MaxJobs {
					require.NoError(t, os.Mkdir(filepath.Join(root, uuid.NewString()), 0700))
				}
			case "repository_overlap":
				p.Repository = root
			}
			out := RunPiJob(context.Background(), p)
			require.Equal(t, "pi_debug_unavailable", out.Failure, "unsafe capture must refuse before reaching the absent source or nonexistent image")
			require.Nil(t, out.Runtime)
			require.Nil(t, out.Debug)
			require.NotNil(t, out.Result.Usage)
			require.NotEmpty(t, out.Receipt)
			_, err = os.Lstat(filepath.Join(root, p.JobID))
			require.True(t, os.IsNotExist(err))
		})
	}
}
