package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/runner"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCiaoExampleConfig(t *testing.T) {
	root := os.Getenv("CIAO_MAINTENANCE_REPO")
	if root == "" {
		t.Skip("set CIAO_MAINTENANCE_REPO to the Ciao checkout")
	}
	file := filepath.Join(root, "integrations", "maintenance", "pylon.yaml.example")
	raw, err := os.ReadFile(file) // #nosec G703 -- explicit operator-selected test checkout, not ingress
	require.NoError(t, err)
	var p config.PylonConfig
	require.NoError(t, yaml.Unmarshal(raw, &p))
	require.NoError(t, p.Validate(file))
	require.Equal(t, "git-clone", p.Workspace.Type)
	require.Equal(t, "{{ .body.source_revision }}", p.Workspace.Ref)
	require.Equal(t, "api_key", p.Agent.Auth)
	require.Empty(t, p.Agent.Volumes)
	require.False(t, p.Channel.Approval)
	require.Equal(t, "X-Pylon-Signature", p.Trigger.SignatureHeader)
}

// Explicitly enabled cross-repository contract rehearsal: real Python sender, real HTTP,
// real SQLite, an acknowledgement lost AFTER commit, and a fake agent (zero model turns).
func TestCiaoSenderAgainstDurableReceiver(t *testing.T) {
	root := os.Getenv("CIAO_MAINTENANCE_REPO")
	if root == "" {
		t.Skip("set CIAO_MAINTENANCE_REPO to the Ciao checkout")
	}
	require.FileExists(t, filepath.Join(root, "scripts", "vendor_maintenance.py"))
	d := deliveryDaemon(t)
	var posts, runs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && posts.Add(1) == 1 {
			captured := httptest.NewRecorder()
			d.Mux.ServeHTTP(captured, r)
			require.Equal(t, http.StatusAccepted, captured.Code)
			http.Error(w, "acknowledgement lost after commit", http.StatusServiceUnavailable)
			return
		}
		d.Mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	d.RunAgent = func(context.Context, runner.RunParams) error { runs.Add(1); return nil }
	script := `
import json,sys
from vendor_maintenance_lib.store import Store
from vendor_maintenance_lib.workflow import deliver
s=Store(sys.argv[1])
try:
 key=s.prepare_delivery("synthetic-qualified-job", {"version":"1.2.3"}, sys.argv[2])
 first=deliver(s,key,sys.argv[3],now=100)
 assert first["state"]=="retry_wait", first
 second=deliver(s,key,sys.argv[3],now=1000)
 assert second["state"]=="accepted", second
 third=deliver(s,key,sys.argv[3],now=2000)
 assert third["receipt"]==second["receipt"]
 print(json.dumps({"result":"durable duplicate recovered", "attempts":second["attempts"]}))
finally:s.close()
`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-c", script, t.TempDir(), server.URL+"/vendor", deliverySecret)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(root, "scripts"))
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.EqualValues(t, 2, posts.Load())
	pending, err := d.Store.PendingDeliveries()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	d.drainDeliveries()
	require.Eventually(t, func() bool { return d.Limiter.Active() == 0 }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, runs.Load())
	t.Log(string(output))
}
