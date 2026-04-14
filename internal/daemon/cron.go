package daemon

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/pylonto/pylon/internal/cron"
	"github.com/pylonto/pylon/internal/runner"
	"github.com/pylonto/pylon/internal/store"
)

// CronScheduler runs a tick loop that checks all cron pylons and fires
// jobs when their schedule matches. It ticks every 30 seconds to ensure
// no minute boundary is missed even with slight drift.
//
// Deduplication: each fire is recorded as a minute-truncated timestamp.
// A pylon will not fire twice in the same calendar minute.
//
// Hot-reload: reads d.Pylons on each tick (protected by pylonsMu), so
// config changes from WatchConfigs() are picked up immediately.
func (d *Daemon) CronScheduler(ctx context.Context) {
	lastFired := make(map[string]time.Time) // pylon name -> last fire minute

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Initial check on startup.
	d.cronTick(lastFired)

	for {
		select {
		case <-ctx.Done():
			log.Println("[cron] scheduler stopped")
			return
		case <-ticker.C:
			d.cronTick(lastFired)
		}
	}
}

func (d *Daemon) cronTick(lastFired map[string]time.Time) {
	// Snapshot the pylon map under read lock.
	d.pylonsMu.RLock()
	type cronPylon struct {
		name string
		cron string
		loc  *time.Location
	}
	var pylons []cronPylon
	for name, pyl := range d.Pylons {
		if pyl.Trigger.Type != "cron" || pyl.Trigger.Cron == "" || pyl.Disabled {
			continue
		}
		pylons = append(pylons, cronPylon{
			name: name,
			cron: pyl.Trigger.Cron,
			loc:  pyl.ResolveTimezone(d.Global),
		})
	}
	d.pylonsMu.RUnlock()

	for _, cp := range pylons {
		schedule, err := cron.Schedule(cp.cron)
		if err != nil {
			continue
		}

		now := time.Now().In(cp.loc)
		nowMinute := now.Truncate(time.Minute)

		// Already fired this minute?
		if nowMinute.Equal(lastFired[cp.name]) {
			continue
		}

		// Check if current minute matches the schedule: go back past the
		// minute boundary and compute the next fire time from there.
		prev := now.Add(-61 * time.Second)
		nextFire := schedule.Next(prev).Truncate(time.Minute)

		if !nextFire.Equal(nowMinute) {
			continue
		}

		lastFired[cp.name] = nowMinute
		log.Printf("[cron] %q firing (schedule: %s, tz: %s)", cp.name, cp.cron, cp.loc)

		d.fireCronJob(cp.name)
	}
}

func (d *Daemon) fireCronJob(name string) {
	pyl, ok := d.pylonConfig(name)
	if !ok {
		return
	}

	jobID := uuid.New().String()
	callbackURL := fmt.Sprintf("http://host.docker.internal:%d/callback/%s", d.Global.Server.Port, jobID)

	n := d.channelFor(name)
	topicName := fmt.Sprintf("%s -- %s", name, jobID[:8])
	if pyl.Channel != nil && pyl.Channel.Topic != "" {
		topicName = runner.ResolveTemplate(pyl.Channel.Topic, nil)
	}
	var topicID string
	if n != nil {
		topicID, _ = n.CreateTopic(topicName)
		if pyl.Channel != nil && pyl.Channel.Message != "" {
			n.SendMessage(topicID, runner.ResolveTemplate(pyl.Channel.Message, nil)) //nolint:errcheck // best-effort notification
		}
	}

	d.Store.Put(&store.Job{
		ID: jobID, PylonName: name, Status: "triggered",
		TopicID: topicID, CallbackURL: callbackURL,
		CreatedAt: time.Now(),
	})

	go d.runJob(name, pyl, jobID, map[string]interface{}{}, callbackURL, topicID, "", "")
}
