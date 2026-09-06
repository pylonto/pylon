package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pylonto/pylon/internal/channel"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/runner"
	"github.com/stretchr/testify/require"
)

func controlDaemon(t *testing.T) *Daemon {
	d := newCronDaemon(t, map[string]*config.PylonConfig{"vendor": {
		Name: "vendor", Trigger: config.TriggerConfig{Type: "webhook", Path: "/vendor", Secret: deliverySecret, SignatureHeader: "X-Pylon-Signature"},
		Control: &config.ControlConfig{TopicID: "42"}, Workspace: config.WorkspaceConfig{Type: "none"},
	}})
	d.Channel = newMockChannel()
	return d
}
func controlRequest(d *Daemon, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/control/vendor", strings.NewReader(body))
	r.Header.Set("Idempotency-Key", key)
	m := hmac.New(sha256.New, []byte(deliverySecret))
	m.Write([]byte(key + "\n" + body))
	r.Header.Set("X-Pylon-Signature", hex.EncodeToString(m.Sum(nil)))
	w := httptest.NewRecorder()
	d.Mux.ServeHTTP(w, r)
	return w
}
func TestControlOnlyHasNoJobOrAgentLifecycleSurface(t *testing.T) {
	fixture := controlDaemon(t)
	d := NewControl(fixture.Pylons, fixture.Store, map[string]channel.Channel{"vendor": fixture.Channel})
	require.Nil(t, d.RunAgent)
	for _, path := range []string{"/vendor", "/trigger/vendor", "/callback/job", "/hooks/job", "/api/pylons", "/reload"} {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, r)
		require.Equal(t, 404, w.Code, path)
	}
	key := strings.Repeat("7", 64)
	body := `{"op":"notify","text":"Synthetic control-only notice"}`
	require.Equal(t, 202, controlRequest(d, key, body).Code)
	require.Equal(t, 202, controlRequest(d, key, body).Code)
	require.Len(t, fixture.Channel.(*mockChannel).messages, 1)
	require.Empty(t, d.Store.List())
}

func TestControlNoticeLostReceiptNeverStartsAnAgentOrSendsTwice(t *testing.T) {
	d := controlDaemon(t)
	var runs atomic.Int32
	d.RunAgent = func(context.Context, runner.RunParams) error { runs.Add(1); return nil }
	key := strings.Repeat("c", 64)
	body := `{"op":"notify","text":"New codex detected; checking compatibility."}`
	a := controlRequest(d, key, body)
	require.Equal(t, 202, a.Code)
	b := controlRequest(d, key, body)
	require.JSONEq(t, a.Body.String(), b.Body.String())
	require.Len(t, d.Channel.(*mockChannel).messages, 1)
	require.Zero(t, runs.Load())
	require.Empty(t, d.Store.List())
	require.Equal(t, 409, controlRequest(d, key, `{"op":"notify","text":"different"}`).Code)
}

type failingNotice struct{ *mockChannel }

func (c *failingNotice) SendMessage(string, string) (string, error) {
	return "", errors.New("lost acknowledgement")
}
func TestControlUnknownNoticeOutcomeCannotReplay(t *testing.T) {
	d := controlDaemon(t)
	d.Channel = &failingNotice{newMockChannel()}
	key := strings.Repeat("d", 64)
	body := `{"op":"notify","text":"Synthetic notice"}`
	a := controlRequest(d, key, body)
	require.Equal(t, 202, a.Code)
	require.Contains(t, a.Body.String(), "outcome_unknown")
	d.Channel = newMockChannel()
	b := controlRequest(d, key, body)
	require.Contains(t, b.Body.String(), "outcome_unknown")
	require.Empty(t, d.Channel.(*mockChannel).messages)
}
func TestControlStatusSeparatesAdmissionFromExecutorAndIgnoresAgentCallbackVerdict(t *testing.T) {
	d := controlDaemon(t)
	d.Limiter = NewAgentLimiter(1)
	key := strings.Repeat("e", 64)
	a := postDelivery(d, key, `{"version":"1.2.3"}`)
	require.Equal(t, 202, a.Code)
	var receipt map[string]string
	require.NoError(t, json.Unmarshal(a.Body.Bytes(), &receipt))
	query := `{"op":"delivery_status","delivery_key":"` + key + `"}`
	status := func() string { return controlRequest(d, strings.Repeat("f", 64), query).Body.String() }
	require.Contains(t, status(), `"execution":"not_started"`)
	entered := make(chan struct{})
	release := make(chan struct{})
	d.RunAgent = func(context.Context, runner.RunParams) error { close(entered); <-release; return nil }
	d.drainDeliveries()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("claimed delivery did not enter executor")
	}
	d.Store.SetCompleted(receipt["job_id"], json.RawMessage(`{"agent_says":"green"}`))
	require.Contains(t, status(), `"execution":"outcome_unknown"`)
	close(release)
	require.Eventually(t, func() bool { return d.Limiter.Active() == 0 }, time.Second, time.Millisecond)
	require.Contains(t, status(), `"execution":"executor_returned"`)
	require.NotContains(t, status(), "agent_says")
}
func TestCiaoControlSenderLostReceiptAndRestart(t *testing.T) {
	repo := os.Getenv("CIAO_MAINTENANCE_REPO")
	if repo == "" {
		t.Skip("set CIAO_MAINTENANCE_REPO to run the cross-repository seam")
	}
	require.FileExists(t, filepath.Join(repo, "scripts", "vendor_maintenance_lib", "pylon_control.py"))
	d := controlDaemon(t)
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && posts.Add(1) == 1 {
			recorder := httptest.NewRecorder()
			d.Mux.ServeHTTP(recorder, r)
			require.Equal(t, 202, recorder.Code)
			conn, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			conn.Close()
			return
		}
		d.Mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	script := `import sys
sys.path.insert(0,sys.argv[1]+"/scripts")
from test_vendor_maintenance import candidate
from vendor_maintenance_lib.store import Store
from vendor_maintenance_lib.pylon_control import pending_notifications
s=Store(sys.argv[3]); key,_=s.observe(candidate());s.pipeline_observe("codex",key,"a"*40)
secret="a-dedicated-test-secret-at-least-32-bytes"
assert pending_notifications(s,sys.argv[2],secret)[0]["state"]=="sending"
s.close();s=Store(sys.argv[3])
assert pending_notifications(s,sys.argv[2],secret)[0]["state"]=="delivered"
assert pending_notifications(s,sys.argv[2],secret)==[]
assert s.summary()["jobs"]==[]
s.close()
print("one notice, recovered receipt, zero coding jobs")
`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-c", script, repo, server.URL+"/control/vendor", t.TempDir())
	cmd.Env = []string{"PATH=/usr/bin:/bin", "PYTHONDONTWRITEBYTECODE=1", "HOME=" + t.TempDir()}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "zero coding jobs")
	require.EqualValues(t, 2, posts.Load())
	require.Len(t, d.Channel.(*mockChannel).messages, 1)
	require.Empty(t, d.Store.List())
}

func TestControlMustBeOptedInSignedBoundedAndEnabled(t *testing.T) {
	d := deliveryDaemon(t)
	require.Equal(t, 404, controlRequest(d, strings.Repeat("a", 64), `{"op":"notify","text":"no"}`).Code)
	d = controlDaemon(t)
	require.Equal(t, 400, controlRequest(d, "invalid", `{"op":"notify","text":"no"}`).Code)
	require.Equal(t, 400, controlRequest(d, strings.Repeat("a", 64), `{"op":"notify","text":"no","secret":"unknown"}`).Code)
	require.Equal(t, 413, controlRequest(d, strings.Repeat("a", 64), strings.Repeat("x", 4097)).Code)
	d.Pylons["vendor"].Disabled = true
	require.Equal(t, 404, controlRequest(d, strings.Repeat("a", 64), `{"op":"notify","text":"no"}`).Code)
}
