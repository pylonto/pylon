package cmd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/pylonto/pylon/internal/agentimage"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/cron"
	"github.com/pylonto/pylon/internal/daemon"
	"github.com/pylonto/pylon/internal/channel"
	"github.com/pylonto/pylon/internal/runner"
	"github.com/pylonto/pylon/internal/store"
)

func init() {
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(restartCmd)
}

var startCmd = &cobra.Command{
	Use:               "start [name]",
	Short:             "Start the Pylon daemon",
	Long:              "Start the daemon and power up all pylons, or a single named pylon.",
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

func runStart(cmd *cobra.Command, args []string) error {
	config.LoadEnv()
	global, err := config.LoadGlobal()
	if err != nil {
		return fmt.Errorf("load config: %w (run `pylon setup` first)", err)
	}

	// Load pylons
	var pylonNames []string
	if len(args) > 0 {
		pylonNames = args
	} else {
		pylonNames, err = config.ListPylons()
		if err != nil {
			return fmt.Errorf("listing pylons: %w", err)
		}
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
		defer st.Close()
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

	// Set up channels
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Build a channel per pylon using ResolveChannel (merges global + per-pylon).
	channels := make(map[string]channel.Channel)
	for name, pyl := range pylons {
		pylonEnv := config.LoadPylonEnvFile(name)
		expand := func(s string) string {
			return config.ExpandWithPylonEnv(s, pylonEnv)
		}
		chType, tg, sl := pyl.ResolveChannel(global)
		switch chType {
		case "telegram":
			if tg != nil {
				token := expand(tg.BotToken)
				if token != "" {
					channels[name] = channel.NewTelegram(ctx, token, tg.ChatID, tg.AllowedUsers)
					log.Printf("[pylon] %q: telegram enabled", name)
				} else {
					log.Printf("[pylon] %q: telegram token not set, notifications disabled", name)
				}
			}
		case "slack":
			if sl != nil {
				botToken := expand(sl.BotToken)
				appToken := expand(sl.AppToken)
				if botToken != "" && appToken != "" {
					channels[name] = channel.NewSlack(ctx, botToken, appToken, sl.ChannelID, sl.AllowedUsers)
					log.Printf("[pylon] %q: slack enabled", name)
				} else {
					log.Printf("[pylon] %q: slack tokens not set, notifications disabled", name)
				}
			}
		}
	}
	d := daemon.New(global, pylons, st, nil, channels)

	// Ensure agent images exist for all active pylons
	seen := make(map[string]bool)
	for _, pyl := range pylons {
		agentType := pyl.ResolveAgentType(global)
		if !seen[agentType] {
			seen[agentType] = true
			agentimage.Ensure(agentType)
		}
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

	addr := fmt.Sprintf("%s:%d", global.Server.Host, global.Server.Port)
	fmt.Printf("\n%d pylons active -- listening on %s\n\n", len(pylons), addr)

	server := &http.Server{Addr: addr, Handler: d.Mux}

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
		server.Close()
	}()

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("server: %w", err)
	}
	log.Println("[pylon] stopped")
	return nil
}
