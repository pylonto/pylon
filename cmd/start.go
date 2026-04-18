package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/pylonto/pylon/internal/agentimage"
	"github.com/pylonto/pylon/internal/channel"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/cron"
	"github.com/pylonto/pylon/internal/daemon"
	"github.com/pylonto/pylon/internal/runner"
	"github.com/pylonto/pylon/internal/store"
)

func init() {
	startCmd.Flags().Bool("only", false, "Run a foreground daemon loaded with just the named pylons (for local debug). Does not talk to a running daemon.")
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(restartCmd)
}

var startCmd = &cobra.Command{
	Use:               "start [name...]",
	Short:             "Ensure the Pylon daemon is running and picks up the named pylon(s)",
	Long:              "Start the daemon if it's not running, or restart the running one (via systemd) so it registers newly-constructed pylons. Use --only to run a foreground debug daemon scoped to specific pylons.",
	ValidArgsFunction: completePylonNames,
	RunE:              runStart,
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the Pylon daemon",
	Run:   func(cmd *cobra.Command, args []string) { fmt.Println("Send SIGTERM to the running pylon process.") },
}

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the Pylon daemon",
	Run:   func(cmd *cobra.Command, args []string) { fmt.Println("Stop and re-run `pylon start`.") },
}

// daemonRunningTimeout caps the liveness probe used by daemonRunning. Overridden
// in tests so they don't pay the full 2s penalty.
var daemonRunningTimeout = 2 * time.Second

func runStart(cmd *cobra.Command, args []string) error {
	config.LoadEnv()
	global, err := config.LoadGlobal()
	if err != nil {
		return fmt.Errorf("load config: %w (run `pylon setup` first)", err)
	}

	only, _ := cmd.Flags().GetBool("only")
	if only {
		if len(args) == 0 {
			return errors.New("--only requires at least one pylon name")
		}
		return runDaemonForeground(global, args)
	}

	if daemonRunning(global.Server.Host, global.Server.Port) {
		return ensureViaRestart(global, args)
	}
	// If args were passed without --only, treat them as advisory. The daemon
	// still boots with all pylons so the systemd ExecStart and post-construct
	// "pylon start foo" flow share the same behavior.
	return runDaemonForeground(global, nil)
}

// daemonRunning returns true when another pylon daemon is already answering on
// host:port. It reuses the doctor.go liveness pattern: GET /callback/doctor-ping
// -- the daemon's callback handler only accepts POST, so a GET yields 405,
// which is a signature unlikely to be emitted by an unrelated HTTP server on
// the same port.
func daemonRunning(host string, port int) bool {
	url := fmt.Sprintf("http://%s:%d/callback/doctor-ping", loopbackHost(host), port)
	client := &http.Client{Timeout: daemonRunningTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusMethodNotAllowed
}

// loopbackHost rewrites a bind-only address (empty or 0.0.0.0) into the
// loopback IP so HTTP clients don't fail with EADDRNOTAVAIL on macOS/Windows
// when the daemon is configured with the default Host.
func loopbackHost(host string) string {
	if host == "" || host == "0.0.0.0" {
		return "127.0.0.1"
	}
	return host
}

// systemdUnitActive returns true if a systemd unit named "pylon" is currently
// active under either the user or system scope.
func systemdUnitActive() bool {
	if exec.Command("systemctl", "--user", "is-active", "--quiet", "pylon").Run() == nil {
		return true
	}
	return exec.Command("systemctl", "is-active", "--quiet", "pylon").Run() == nil
}

// ensureViaRestart is the "daemon already running" branch: it asks systemd to
// restart the unit so the daemon re-scans ~/.pylon/pylons/ and registers any
// pylons constructed since the unit last started.
func ensureViaRestart(global *config.GlobalConfig, targets []string) error {
	if !systemdUnitActive() {
		return fmt.Errorf("another pylon daemon is running on port %d but is not managed by systemd -- stop it (Ctrl-C or kill) and rerun `pylon start`, or run `pylon service install` to put it under systemd", global.Server.Port)
	}

	if len(targets) > 0 {
		fmt.Printf("Restarting pylon daemon to pick up: %s\n", strings.Join(targets, ", "))
	} else {
		fmt.Println("Restarting pylon daemon...")
	}

	if err := systemctlRestart(); err != nil {
		return fmt.Errorf("systemctl restart pylon: %w", err)
	}

	// Poll for liveness; the unit takes ~1s to rebind.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if daemonRunning(global.Server.Host, global.Server.Port) {
			names, _ := config.ListPylons()
			fmt.Printf("Daemon restarted. %d pylons active.\n", len(names))
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("daemon did not come back within 10s -- check `systemctl status pylon`")
}

// systemctlRestart runs `systemctl --user restart pylon`, falling back to the
// system scope only if the user unit isn't installed. Real user-scope errors
// (masked unit, daemon-reload required, DBus issues on headless SSH) are
// surfaced to the caller rather than silently retried under system scope.
func systemctlRestart() error {
	out, err := exec.Command("systemctl", "--user", "restart", "pylon").CombinedOutput()
	if err == nil {
		return nil
	}
	if !strings.Contains(string(out), "not loaded") && !strings.Contains(string(out), "could not be found") {
		return fmt.Errorf("systemctl --user restart pylon: %w: %s", err, strings.TrimSpace(string(out)))
	}
	sysOut, sysErr := exec.Command("systemctl", "restart", "pylon").CombinedOutput()
	if sysErr != nil {
		return fmt.Errorf("systemctl restart pylon: %w: %s", sysErr, strings.TrimSpace(string(sysOut)))
	}
	return nil
}

// openListener binds the daemon's TCP port. Separated so tests can assert that
// background goroutines never start when the bind fails.
func openListener(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

// runDaemonForeground boots the daemon process in the foreground. When filter
// is nil, all enabled pylons are loaded; otherwise only the named ones. The
// listener is bound BEFORE any goroutine is started so a bind failure exits
// cleanly without a misleading "N pylons active" banner or a trailing
// "[cron] scheduler stopped" log.
func runDaemonForeground(global *config.GlobalConfig, filter []string) error {
	pylonNames, err := resolvePylonNames(filter)
	if err != nil {
		return err
	}
	if len(pylonNames) == 0 {
		fmt.Println("No pylons found. Run `pylon construct <name>` to create one.")
		return nil
	}

	pylons := make(map[string]*config.PylonConfig)
	for _, name := range pylonNames {
		pyl, err := config.LoadPylon(name)
		if err != nil {
			log.Printf("[pylon] skipping %q: %v", name, err)
			continue
		}
		if pyl.Disabled {
			log.Printf("[pylon] skipping %q: disabled", name)
			continue
		}
		pylons[name] = pyl
	}

	// Open per-pylon stores so each pylon's jobs live in its own DB.
	stores := make(map[string]*store.Store)
	for name := range pylons {
		st, err := store.Open(config.PylonDBPath(name))
		if err != nil {
			return fmt.Errorf("opening database for %s: %w", name, err)
		}
		defer st.Close() //nolint:gocritic // bounded by pylon count, all close on exit
		stores[name] = st
	}
	st := store.NewMulti(stores)

	if n := st.RecoverFromDB(); n > 0 {
		log.Printf("[pylon] recovered %d pending jobs", n)
	}

	// Prune orphans
	activeIDs := make(map[string]bool)
	for _, j := range st.List() {
		activeIDs[j.ID] = true
	}
	runner.PruneOrphanedWorkspaces(activeIDs)
	runner.PruneWorktreeMetadata()
	runner.PruneOrphanedContainers(activeIDs)

	// Bind the listener BEFORE starting any background work. If this fails,
	// we exit without polluting output with a success banner or leaking a
	// scheduler-stopped log.
	addr := fmt.Sprintf("%s:%d", global.Server.Host, global.Server.Port)
	ln, err := openListener(addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	// Reap any running agent containers on the way out. The per-job goroutines
	// have their own defers, but those only run if the goroutine survives
	// process teardown, which isn't guaranteed on signal.
	defer func() {
		if n := runner.KillAllJobContainers(); n > 0 {
			log.Printf("[pylon] killed %d running job container(s) on shutdown", n)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Build channels (requires ctx so bots can be canceled on shutdown).
	channels := buildChannels(ctx, global, pylons)
	for name, ch := range channels {
		if !ch.Ready() {
			log.Printf("[pylon] %q: channel pending -- send a message to the bot to auto-detect", name)
		}
	}
	d := daemon.New(global, pylons, st, nil, channels)

	// Ensure agent images exist for all active pylons
	seen := make(map[string]bool)
	for _, pyl := range pylons {
		img := pyl.ResolveAgentImage(global)
		if seen[img] {
			continue
		}
		seen[img] = true
		agentimage.EnsureImage(img, pyl.ResolveAgentType(global))
	}

	fmt.Printf("\nPowering up pylons...\n\n")
	for name, pyl := range pylons {
		trigger := pyl.Trigger.Type
		path := pyl.Trigger.Path
		if pyl.Trigger.Cron != "" {
			loc := pyl.ResolveTimezone(global)
			next := cron.NextFire(pyl.Trigger.Cron, loc)
			path = fmt.Sprintf("%s (%s) [%s] next: %s",
				pyl.Trigger.Cron,
				describeCron(pyl.Trigger.Cron),
				loc.String(),
				next.Format("Jan 02 15:04"),
			)
		}
		fmt.Printf("  %-24s ok  %s %s\n", name, trigger, path)
	}
	fmt.Printf("\n%d pylons active -- listening on %s\n\n", len(pylons), addr)

	server := &http.Server{Handler: d.Mux}

	// Hot-reload pylon configs on file change
	go d.WatchConfigs(ctx)

	// Start cron scheduler for time-based triggers
	go d.CronScheduler(ctx)

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("[pylon] received %v, shutting down...", sig)
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if err := server.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server: %w", err)
	}
	log.Println("[pylon] stopped")
	return nil
}

// resolvePylonNames returns the pylon names to load. When filter is nil or
// empty, all pylons in ~/.pylon/pylons/ are returned.
func resolvePylonNames(filter []string) ([]string, error) {
	if len(filter) > 0 {
		return filter, nil
	}
	return config.ListPylons()
}

// buildChannels sets up a channel bot per pylon based on its resolved channel
// config. Returns only channels that had a non-empty token; channels with
// missing env vars log a warning and are skipped.
func buildChannels(ctx context.Context, global *config.GlobalConfig, pylons map[string]*config.PylonConfig) map[string]channel.Channel {
	channels := make(map[string]channel.Channel)
	for name, pyl := range pylons {
		pylonEnv := config.LoadPylonEnvFile(name)
		expand := func(s string) string { return config.ExpandWithPylonEnv(s, pylonEnv) }
		chType, tg, sl := pyl.ResolveChannel(global)
		switch chType {
		case "telegram":
			if tg == nil {
				continue
			}
			token := expand(tg.BotToken)
			if token == "" {
				log.Printf("[pylon] %q: telegram token not set, notifications disabled", name)
				continue
			}
			ch := channel.NewTelegram(ctx, token, tg.ChatID, tg.AllowedUsers)
			if tg.ChatID == 0 {
				pylName, pylCfg := name, pyl
				ch.OnChatDetected(func(detectedID int64) {
					if err := persistChatID(pylName, pylCfg, detectedID); err != nil {
						log.Printf("[pylon] %q: failed to persist detected chat_id: %v", pylName, err)
					} else {
						log.Printf("[pylon] %q: saved detected chat_id %d", pylName, detectedID)
					}
				})
			}
			channels[name] = ch
			log.Printf("[pylon] %q: telegram enabled", name)
		case "slack":
			if sl == nil {
				continue
			}
			botToken := expand(sl.BotToken)
			appToken := expand(sl.AppToken)
			if botToken == "" || appToken == "" {
				log.Printf("[pylon] %q: slack tokens not set, notifications disabled", name)
				continue
			}
			channels[name] = channel.NewSlack(ctx, botToken, appToken, sl.ChannelID, sl.AllowedUsers)
			log.Printf("[pylon] %q: slack enabled", name)
		}
	}
	return channels
}

// persistChatID writes the auto-detected Telegram chat_id back to the
// appropriate config file (per-pylon if the pylon has its own channel config,
// otherwise global).
func persistChatID(pylonName string, pyl *config.PylonConfig, chatID int64) error {
	if pyl.Channel != nil && pyl.Channel.Telegram != nil {
		fresh, err := config.LoadPylonRaw(pylonName)
		if err != nil {
			return fmt.Errorf("reloading pylon config: %w", err)
		}
		if fresh.Channel == nil || fresh.Channel.Telegram == nil {
			return fmt.Errorf("pylon %q channel config disappeared", pylonName)
		}
		fresh.Channel.Telegram.ChatID = chatID
		return config.SavePylon(fresh)
	}
	freshGlobal, err := config.LoadGlobal()
	if err != nil {
		return fmt.Errorf("reloading global config: %w", err)
	}
	if freshGlobal.Defaults.Channel.Telegram != nil {
		freshGlobal.Defaults.Channel.Telegram.ChatID = chatID
		return config.SaveGlobal(freshGlobal)
	}
	return fmt.Errorf("no telegram config found to persist chat_id")
}
