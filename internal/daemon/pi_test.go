package daemon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/runner"
	"github.com/pylonto/pylon/internal/store"
	"github.com/pylonto/pylon/internal/testutil"
	"github.com/stretchr/testify/require"
)

func piDaemon(t *testing.T) (*Daemon, *store.Store, *config.PylonConfig) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "jobs.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	pyl := &config.PylonConfig{Name: "repair", Trigger: config.TriggerConfig{Type: "webhook", Path: "/repair", Secret: deliverySecret, SignatureHeader: "X-Pylon-Signature"},
		Workspace: config.WorkspaceConfig{Type: "git-clone", Repo: "/trusted/ciao", Ref: "{{ .body.source_revision }}"},
		Agent: &config.PylonAgent{Type: "pi", Pi: &config.PiConfig{Image: "sha256:" + strings.Repeat("a", 64), AuthDir: "/private/role", AllowedPaths: []string{"src/repair.txt"},
			Limits: store.SubscriptionLimits{DailyJobs: 2, DailyTokens: 1600000, JobTokens: 800000, JobSeconds: 60}}}}
	global := &config.GlobalConfig{Server: config.ServerConfig{Host: "127.0.0.1", Port: 18001}}
	d, err := NewPi(global, pyl, st)
	require.NoError(t, err)
	return d, st, pyl
}

func piBriefBytes(t *testing.T, source string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"v": 1, "kind": "ciao.vendor.maintenance", "purpose": "repair", "source_revision": source,
		"qualification_id": strings.Repeat("b", 64), "publication": map[string]any{"mode": "none"}, "report": map[string]any{"fixture": true}, "contract": []string{"fixture"}})
	require.NoError(t, err)
	return raw
}

func piDeliver(t *testing.T, d *Daemon, key string, raw []byte) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/repair", bytes.NewReader(raw))
	mac := hmac.New(sha256.New, []byte(deliverySecret))
	_, err := mac.Write(append([]byte(key+"\n"), raw...))
	require.NoError(t, err)
	req.Header.Set("X-Pylon-Signature", hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	d.Mux.ServeHTTP(response, req)
	require.Equal(t, 202, response.Code, response.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.NotEmpty(t, body["job_id"])
	return body["job_id"]
}

func TestPiDispatcherFreshClaimExactlyOnceAndNoLegacyRoutes(t *testing.T) {
	d, st, pyl := piDaemon(t)
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	d.RunPi = func(ctx context.Context, p runner.PiParams) runner.PiOutcome {
		calls.Add(1)
		require.False(t, p.Fixture)
		require.Empty(t, p.FixtureCase)
		require.WithinDuration(t, time.Now().Add(time.Minute), p.Deadline, 2*time.Second)
		close(started)
		<-release
		return runner.PiOutcome{Result: store.SubscriptionResult{Outcome: "executor_returned", Usage: &store.SubscriptionUsage{Input: 100, Output: 20}}}
	}
	raw := piBriefBytes(t, strings.Repeat("a", 40))
	// Even signed ingress cannot select the executor-only no-model switch.
	raw = append(raw[:len(raw)-1], []byte(`,"fixture":true,"fixture_case":"hang"}`)...)
	key := strings.Repeat("c", 64)
	job := piDeliver(t, d, key, raw)
	require.Equal(t, job, piDeliver(t, d, key, raw))
	d.drainDeliveries()
	<-started
	status, err := st.SubscriptionStatus(time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, status.Unresolved)
	require.EqualValues(t, 800000, status.TokensToday)
	d.drainDeliveries()
	require.EqualValues(t, 1, calls.Load())
	for _, path := range []string{"/trigger/repair", "/callback/" + job, "/hooks/" + job, "/control/repair"} {
		res := httptest.NewRecorder()
		d.Mux.ServeHTTP(res, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		require.Equal(t, 404, res.Code)
	}
	require.False(t, d.runJob(pyl.Name, pyl, job, nil, "", "", "", ""))
	require.EqualValues(t, 1, calls.Load())
	close(release)
	d.WaitPiJobs()
	d.drainDeliveries()
	require.EqualValues(t, 1, calls.Load())
	status, err = st.SubscriptionStatus(time.Now())
	require.NoError(t, err)
	require.Equal(t, 0, status.Unresolved)
	require.EqualValues(t, 120, status.TokensToday)
}

func TestPiDispatcherUnknownUsagePauseAndRestartCannotReplay(t *testing.T) {
	d, st, pyl := piDaemon(t)
	var calls atomic.Int32
	d.RunPi = func(context.Context, runner.PiParams) runner.PiOutcome {
		calls.Add(1)
		return runner.PiOutcome{Result: store.SubscriptionResult{Outcome: "executor_failed", Pause: "quota_exhausted"}}
	}
	piDeliver(t, d, strings.Repeat("c", 64), piBriefBytes(t, strings.Repeat("a", 40)))
	d.drainDeliveries()
	d.WaitPiJobs()
	status, err := st.SubscriptionStatus(time.Now())
	require.NoError(t, err)
	require.Equal(t, "quota_exhausted", status.Paused)
	require.Equal(t, 1, status.Unresolved)
	require.NoError(t, st.PauseSubscription("", time.Now()))
	restarted, err := NewPi(d.Global, pyl, st)
	require.NoError(t, err)
	restarted.RunPi = d.RunPi
	piDeliver(t, restarted, strings.Repeat("d", 64), piBriefBytes(t, strings.Repeat("a", 40)))
	restarted.drainDeliveries()
	restarted.WaitPiJobs()
	require.EqualValues(t, 1, calls.Load())
	status, err = st.SubscriptionStatus(time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, status.Unresolved)
}

// Opt-in native rehearsal: signed admission -> actual durable dispatcher -> two
// disposable Docker containers -> Pi SDK tools -> Ciao's clean import boundary.
// No model/OAuth/network is used; the runtime's fixture switch is not on ingress.
func TestPiDockerTransportPreparedPatch(t *testing.T) {
	image := os.Getenv("PYLON_PI_REHEARSAL_IMAGE")
	if image == "" {
		t.Skip("explicit disposable Docker rehearsal only")
	}
	ciao := testutil.CiaoSource(t)
	d, st, pyl := piDaemon(t)
	pyl.Agent.Pi.Image = image
	pyl.Agent.Pi.Limits.JobSeconds = 120
	repository := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	git := func(args ...string) string {
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Dir = repository
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@localhost", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@localhost"}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	git("init", "--template=")
	require.NoError(t, os.Mkdir(filepath.Join(repository, "src"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(repository, "src", "repair.txt"), []byte("broken\n"), 0644))
	git("add", "--", "src/repair.txt")
	git("commit", "-m", "Controlled fixture")
	base := git("rev-parse", "HEAD")
	pyl.Workspace.Repo = repository
	// Generate the actual preexisting brief, rather than require controller changes.
	script := `import json,sys
from vendor_maintenance_lib.workflow import repair_brief
report={"outcome":"review_required","source":{"ciao_revision":sys.argv[1]},"candidate":{"vendor":"pi","version":"0.84.4"},"fixture_only":True}
print(json.dumps(repair_brief({"id":"b"*64,"state":"done","report":json.dumps(report)},"repair")))`
	cmd := exec.Command("python3", "-c", script, base)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(ciao, "scripts"))
	raw, err := cmd.Output()
	require.NoError(t, err)
	var outcome runner.PiOutcome
	var calls atomic.Int32
	d.RunPi = func(ctx context.Context, p runner.PiParams) runner.PiOutcome {
		calls.Add(1)
		p.Fixture = true
		outcome = runner.RunPiJob(ctx, p)
		return outcome
	}
	key := strings.Repeat("c", 64)
	job := piDeliver(t, d, key, raw)
	d.drainDeliveries()
	d.WaitPiJobs()
	require.Equal(t, "executor_returned", outcome.Result.Outcome, "failure=%s runtime=%+v", outcome.Failure, outcome.Runtime)
	require.NotNil(t, outcome.Runtime)
	require.True(t, outcome.Runtime.Fixture)
	require.Equal(t, 0, outcome.Runtime.Requests)
	require.Equal(t, 3, outcome.Runtime.Tools)
	require.NotEmpty(t, outcome.Patch)
	patch, err := os.ReadFile(outcome.Patch)
	require.NoError(t, err)
	require.Contains(t, string(patch), "-broken\n+repaired")
	status, err := st.SubscriptionStatus(time.Now())
	require.NoError(t, err)
	require.Zero(t, status.Unresolved)
	require.Zero(t, status.TokensToday)
	require.Equal(t, job, piDeliver(t, d, key, raw))
	d.drainDeliveries()
	d.WaitPiJobs()
	require.EqualValues(t, 1, calls.Load())
	importer := `import json,sys
from pathlib import Path
from vendor_maintenance_lib.patching import prepared_patch,MAX_PATCH
assert MAX_PATCH==1048576
with prepared_patch(sys.argv[1],sys.argv[2],Path(sys.argv[3]).read_bytes(),["src/repair.txt"]) as accepted:
 assert accepted["base"]!=accepted["head"]
 assert (accepted["repository"]/"src/repair.txt").read_text()=="repaired\n"
 print(json.dumps({"accepted":True,"base":accepted["base"],"head":accepted["head"],"paths":accepted["paths"]}))`
	cmd = exec.Command("python3", "-c", importer, repository, base, outcome.Patch)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(ciao, "scripts"))
	imported, err := cmd.CombinedOutput()
	require.NoError(t, err, string(imported))
	t.Log(string(imported))
	original, err := os.ReadFile(filepath.Join(repository, "src", "repair.txt"))
	require.NoError(t, err)
	require.Equal(t, "broken\n", string(original))
	t.Logf("native fixture: job=%s image=%s patch_sha256=%s model_calls=0 tool_calls=3", job, image, outcome.PatchSHA256)
}
