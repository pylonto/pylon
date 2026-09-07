package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/runner"
	"github.com/pylonto/pylon/internal/store"
)

// NewPi reuses signed ingress, the durable queue and Store, but deliberately
// excludes cron/manual/follow-up/callback/notification routes and config reload.
// One role, one pylon and one ledger own this subscription allocation.
func NewPi(global *config.GlobalConfig, pyl *config.PylonConfig, st *store.Store) (*Daemon, error) {
	if pyl.ValidatePi() != nil || net.ParseIP(global.Server.Host) == nil || !net.ParseIP(global.Server.Host).IsLoopback() ||
		global.Defaults.Channel.Type != "" || pyl.Disabled {
		return nil, errors.New("pi_requires_isolated_loopback_role")
	}
	d := &Daemon{Global: global, Pylons: map[string]*config.PylonConfig{pyl.Name: pyl}, Store: store.NewMulti(map[string]*store.Store{pyl.Name: st}),
		Limiter: NewAgentLimiter(1), Mux: http.NewServeMux(), RunPi: runner.RunPiJob, piStore: st}
	d.registerWebhook(pyl.Name, pyl)
	return d, nil
}

func (d *Daemon) startPiDelivery(ctx context.Context, pyl *config.PylonConfig, entry store.Delivery) bool {
	if d.piStore == nil || d.RunPi == nil || pyl.ValidatePi() != nil {
		return false
	}
	base, err := runner.PiBrief(entry.Body)
	if err != nil {
		return false
	}
	if !d.Limiter.Acquire() {
		return false
	}
	hash := sha256.Sum256(entry.Body)
	claim, fresh, err := d.piStore.ClaimSubscription(entry.JobID, hex.EncodeToString(hash[:]), pyl.Agent.Pi.Limits, time.Now())
	if err != nil || !fresh {
		d.Limiter.Release()
		return false
	}
	d.piJobs.Add(1)
	go func() {
		defer d.piJobs.Done()
		defer d.Limiter.Release()
		out := d.RunPi(ctx, runner.PiParams{Pylon: pyl.Name, JobID: entry.JobID, Brief: entry.Body, Base: base, Repository: pyl.Workspace.Repo,
			Config: *pyl.Agent.Pi, Deadline: time.Unix(claim.Deadline, 0), PatchRoot: filepath.Join(config.Dir(), "pi-patches")})
		if out.Result.Pause != "" {
			// Pause must persist even if usage/termination is unknown and cannot
			// settle the claim. Resume is explicit and never erases that claim.
			if err := d.piStore.PauseSubscription(out.Result.Pause, time.Now()); err != nil {
				log.Print("[pi] pause persistence failed; claim retained")
				return
			}
		}
		if err := d.piStore.FinishSubscription(entry.JobID, out.Result, time.Now()); err != nil {
			log.Print("[pi] execution or usage unresolved; claim retained")
			return
		}
		// Only the durable trusted executor receipt owns this transition. No
		// transcript, tool result or callback can finish/refund a Pi job.
		if _, err := d.Store.TransitionDelivery(entry, "claimed", "submitted"); err != nil {
			log.Print("[pi] delivery transition unavailable; no execution replay")
		}
	}()
	return true
}

func (d *Daemon) WaitPiJobs() { d.piJobs.Wait() }
