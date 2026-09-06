package daemon

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/pylonto/pylon/internal/store"
)

// registerControl exposes only signed, bounded state/notice operations for explicitly
// opted-in pylons. It neither spawns an agent nor trusts agent callbacks as executor facts.
// Keep this receiver loopback-only. A Docker callback proxy must never forward /control/.
func (d *Daemon) registerControl(name string) {
	d.Mux.HandleFunc("/control/"+name, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.Header().Set("Pylon-Control-Protocol", "1")
			w.WriteHeader(204)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		pyl, ok := d.pylonConfig(name)
		if !ok || pyl.Disabled || pyl.Control == nil {
			http.Error(w, "unavailable", 404)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		defer r.Body.Close()
		if err != nil {
			http.Error(w, "control body exceeds 4 KiB", 413)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if !deliveryKey.MatchString(key) {
			http.Error(w, "invalid key", 400)
			return
		}
		if pyl.Trigger.Secret == "" || pyl.Trigger.SignatureHeader == "" || !verifySignature(pyl.Trigger, r.Header, raw) {
			http.Error(w, "signed control required", 401)
			return
		}
		var request struct {
			Op          string `json:"op"`
			Text        string `json:"text,omitempty"`
			DeliveryKey string `json:"delivery_key,omitempty"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(interface{})) != io.EOF {
			http.Error(w, "invalid control request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Op {
		case "delivery_status":
			if request.Text != "" || !deliveryKey.MatchString(request.DeliveryKey) {
				http.Error(w, "invalid delivery key", 400)
				return
			}
			state, err := d.Store.DeliveryStatus(name, request.DeliveryKey)
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "unknown delivery", 404)
				return
			}
			if err != nil {
				http.Error(w, "storage unavailable", 503)
				return
			}
			json.NewEncoder(w).Encode(state)
		case "notify":
			if request.DeliveryKey != "" || len(request.Text) == 0 || len(request.Text) > 2048 || strings.ContainsRune(request.Text, '\x00') {
				http.Error(w, "invalid notice", 400)
				return
			}
			ch := d.channelFor(name)
			if ch == nil || !ch.Ready() || pyl.Control.TopicID == "" {
				http.Error(w, "channel not configured", 503)
				return
			}
			n, fresh, err := d.Store.ClaimNotice(name, key, raw)
			if errors.Is(err, store.ErrDeliveryConflict) {
				http.Error(w, "key conflict", 409)
				return
			}
			if err != nil {
				http.Error(w, "storage unavailable", 503)
				return
			}
			if fresh {
				id, sendErr := ch.SendMessage(pyl.Control.TopicID, ch.FormatText(request.Text))
				if sendErr == nil {
					if err = d.Store.CompleteNotice(name, key, id); err != nil {
						http.Error(w, "notice outcome unknown", 503)
						return
					}
					n.State = "delivered"
					n.MessageID = id
				}
			}
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(n)
		default:
			http.Error(w, "unknown operation", 400)
		}
	})
}
