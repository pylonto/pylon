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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/runner"
	"github.com/stretchr/testify/require"
)

const deliverySecret = "a-dedicated-test-secret-at-least-32-bytes"

func deliveryDaemon(t *testing.T) *Daemon {
	d := newCronDaemon(t, map[string]*config.PylonConfig{"vendor": {
		Name: "vendor", Trigger: config.TriggerConfig{Type: "webhook", Path: "/vendor", Secret: deliverySecret, SignatureHeader: "X-Pylon-Signature"},
		Workspace: config.WorkspaceConfig{Type: "none"}, Agent: &config.PylonAgent{Prompt: "inspect {{ .body.version }}"},
	}})
	return d
}

func postDelivery(d *Daemon, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/vendor", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	mac := hmac.New(sha256.New, []byte(deliverySecret))
	mac.Write([]byte(key + "\n" + body))
	req.Header.Set("X-Pylon-Signature", hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	d.Mux.ServeHTTP(rec, req)
	return rec
}

func TestDeliveryReceiptIsDurableAndCapacityDoesNotDropWork(t *testing.T) {
	d := deliveryDaemon(t)
	d.Limiter = NewAgentLimiter(1)
	require.True(t, d.Limiter.Acquire())
	var runs atomic.Int32
	d.RunAgent = func(_ context.Context, p runner.RunParams) error {
		runs.Add(1)
		require.Equal(t, "inspect 1.2.3", p.Prompt)
		return nil
	}
	key := strings.Repeat("a", 64)
	first := postDelivery(d, key, `{"version":"1.2.3"}`)
	require.Equal(t, 202, first.Code)
	var receipt map[string]string
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &receipt))
	require.Equal(t, "1", receipt["delivery_protocol"])
	require.Equal(t, key, receipt["delivery_key"])
	require.NotEmpty(t, receipt["job_id"])
	again := postDelivery(d, key, `{"version":"1.2.3"}`)
	require.JSONEq(t, first.Body.String(), again.Body.String())
	d.drainDeliveries()
	require.Zero(t, runs.Load())
	pending, err := d.Store.PendingDeliveries()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	d.Limiter.Release()
	d.drainDeliveries()
	require.Eventually(t, func() bool { return d.Limiter.Active() == 0 }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, runs.Load())
	d.drainDeliveries()
	require.EqualValues(t, 1, runs.Load())
	require.Equal(t, 409, postDelivery(d, key, `{"version":"2.0.0"}`).Code)
}

func TestDeliveryPreflightAndAuthentication(t *testing.T) {
	d := deliveryDaemon(t)
	r := httptest.NewRecorder()
	d.Mux.ServeHTTP(r, httptest.NewRequest(http.MethodOptions, "/vendor", nil))
	require.Equal(t, 204, r.Code)
	require.Equal(t, "1", r.Header().Get("Pylon-Delivery-Protocol"))
	req := httptest.NewRequest(http.MethodPost, "/vendor", strings.NewReader(`{}`))
	req.Header.Set("Idempotency-Key", strings.Repeat("b", 64))
	r = httptest.NewRecorder()
	d.Mux.ServeHTTP(r, req)
	require.Equal(t, 401, r.Code)
	require.Equal(t, 400, postDelivery(d, "bad-key", `{}`).Code)
	require.Equal(t, 400, postDelivery(d, strings.Repeat("b", 64), `null`).Code)
	require.Equal(t, 413, postDelivery(d, strings.Repeat("c", 64), strings.Repeat("x", 65537)).Code)
}

func TestDeliveryCannotBypassApprovalOrDisabledConfig(t *testing.T) {
	d := deliveryDaemon(t)
	d.Pylons["vendor"].Channel = &config.PylonChannel{Approval: true}
	require.Equal(t, 422, postDelivery(d, strings.Repeat("b", 64), `{}`).Code)
	d.Pylons["vendor"].Disabled = true
	require.Equal(t, 404, postDelivery(d, strings.Repeat("b", 64), `{}`).Code)
}

func TestGitHubSignatureAndKeyBinding(t *testing.T) {
	body := []byte(`{"version":"1.2.3"}`)
	mac := hmac.New(sha256.New, []byte(deliverySecret))
	mac.Write(body)
	raw := hex.EncodeToString(mac.Sum(nil))
	trigger := config.TriggerConfig{Secret: deliverySecret, SignatureHeader: "X-Hub-Signature-256"}
	header := make(http.Header)
	header.Set(trigger.SignatureHeader, "sha256="+raw)
	require.True(t, verifySignature(trigger, header, body))
	header.Set("Idempotency-Key", strings.Repeat("d", 64))
	require.False(t, verifySignature(trigger, header, body), "a signed body cannot be replayed under an unsigned new key")
	header.Del("Idempotency-Key")
	header.Set(trigger.SignatureHeader, raw)
	require.False(t, verifySignature(trigger, header, body))
	t.Setenv("PYLON_UNSET_SECRET", "")
	trigger.Secret = "$PYLON_UNSET_SECRET"
	require.False(t, verifySignature(trigger, header, body))
}

type failedApprovalChannel struct{ *mockChannel }

func (c *failedApprovalChannel) SendApproval(string, string, string) (string, error) {
	return "", errors.New("channel unavailable")
}

func TestApprovalFailureNeverStartsAnAgent(t *testing.T) {
	d := deliveryDaemon(t)
	d.Pylons["vendor"].Trigger.Secret = ""
	// The route captures its original signature configuration, so use a signed non-keyed request.
	d.Pylons["vendor"].Channel = &config.PylonChannel{Approval: true}
	d.Channel = &failedApprovalChannel{newMockChannel()}
	var runs atomic.Int32
	d.RunAgent = func(context.Context, runner.RunParams) error { runs.Add(1); return nil }
	req := httptest.NewRequest(http.MethodPost, "/vendor", strings.NewReader(`{}`))
	mac := hmac.New(sha256.New, []byte(deliverySecret))
	mac.Write([]byte(`{}`))
	req.Header.Set("X-Pylon-Signature", hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	d.Mux.ServeHTTP(rec, req)
	require.Equal(t, 503, rec.Code)
	require.Zero(t, runs.Load())
	require.Zero(t, d.Limiter.Active())
}
