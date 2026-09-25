package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/runner"
	"github.com/pylonto/pylon/internal/store"
)

var deliveryKey = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (d *Daemon) acceptDelivery(w http.ResponseWriter, r *http.Request, name string, pyl *config.PylonConfig, raw []byte) {
	key := r.Header.Get("Idempotency-Key")
	if !deliveryKey.MatchString(key) {
		http.Error(w, "Idempotency-Key must be 64 lowercase hex characters", http.StatusBadRequest)
		return
	}
	// Admission requires authenticated, explicitly unattended work. Merge/release approval
	// belongs outside this executor. We never silently bypass a configured pre-run approval.
	if pyl.Trigger.Secret == "" || pyl.Trigger.SignatureHeader == "" || !verifySignature(pyl.Trigger, r.Header, raw) {
		http.Error(w, "signed delivery required", http.StatusUnauthorized)
		return
	}
	if pyl.Channel != nil && pyl.Channel.Approval {
		http.Error(w, "keyed delivery requires an unattended pylon; approval is configured", http.StatusUnprocessableEntity)
		return
	}
	if pyl.ResolveAgentType(d.Global) == "pi" {
		if _, err := runner.PiBrief(raw); err != nil || d.piStore == nil || pyl.ValidatePi() != nil {
			http.Error(w, "pi_signed_repair_role_required", http.StatusUnprocessableEntity)
			return
		}
	}
	receipt, _, err := d.Store.AcceptDelivery(name, key, raw)
	if err != nil {
		code := http.StatusServiceUnavailable
		if errors.Is(err, store.ErrDeliveryConflict) {
			code = http.StatusConflict
		} else if errors.Is(err, store.ErrDeliveryShape) {
			code = http.StatusBadRequest
		}
		http.Error(w, http.StatusText(code), code)
		return
	}
	// Nothing runs on the request stack. The queue is independently drainable/restartable.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"delivery_protocol": "1", "delivery_key": key, "job_id": receipt.JobID, "status": receipt.State,
	})
}

// RunDeliveryQueue retries capacity, not agent execution. Committing the claim before
// starting means a crash cannot automatically create a second externally effective agent.
func (d *Daemon) RunDeliveryQueue(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		d.drainDeliveriesContext(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (d *Daemon) drainDeliveries() { d.drainDeliveriesContext(context.Background()) }

func (d *Daemon) drainDeliveriesContext(ctx context.Context) {
	d.deliveryMu.Lock()
	defer d.deliveryMu.Unlock()
	pending, err := d.Store.PendingDeliveries()
	if err != nil {
		log.Printf("[delivery] could not read durable queue")
		return
	}
	for _, entry := range pending {
		if ctx.Err() != nil {
			return
		}
		pyl, ok := d.pylonConfig(entry.PylonName)
		if !ok || pyl.Disabled {
			continue
		}
		if pyl.Channel != nil && pyl.Channel.Approval {
			continue
		}
		if pyl.ResolveAgentType(d.Global) == "pi" {
			d.startPiDelivery(ctx, pyl, entry)
			continue
		}
		var body map[string]interface{}
		if json.Unmarshal(entry.Body, &body) != nil {
			continue
		}
		if !d.Limiter.Acquire() {
			return
		}
		claimed, err := d.Store.TransitionDelivery(entry, "queued", "claimed")
		if err != nil {
			d.Limiter.Release()
			log.Printf("[delivery] could not persist claim")
			return
		}
		if !claimed {
			d.Limiter.Release()
			continue
		}
		callback := fmt.Sprintf("http://host.docker.internal:%d/callback/%s", d.Global.Server.Port, entry.JobID)
		d.startReservedJob(entry.PylonName, pyl, entry.JobID, body, callback, "", "", "")
		if _, err := d.Store.TransitionDelivery(entry, "claimed", "submitted"); err != nil {
			log.Printf("[delivery] %s outcome unknown; reconcile job %s before retrying", entry.Key, entry.JobID)
		}
	}
}
