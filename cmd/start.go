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
	"github.com/pylonto/pylon/internal/daemon"
	"github.com/pylonto/pylon/internal/notifier"
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
		pylons[name] = pyl
	}

	// Open a shared store (uses first pylon's DB for now, or a global one)
	dbPath := config.PylonDBPath(pylonNames[0])
	if len(pylonNames) > 1 {
		dbPath = config.Dir() + "/pylon.db"
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer st.Close()

	if n := st.RecoverFromDB(); n > 0 {
		log.Printf("[pylon] recovered %d pending jobs", n)
	}

	// Prune orphans
	activeIDs := make(map[string]bool)
	for _, j := range st.List() {
		activeIDs[j.ID] = true
	}
	runner.PruneOrphanedWorkspaces(activeIDs)
	runner.PruneOrphanedContainers(activeIDs)

	// Set up notifier
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var globalNotifier notifier.Notifier
	switch global.Defaults.Notifier.Type {
	case "telegram":
		if global.Defaults.Notifier.Telegram != nil {
			tg := global.Defaults.Notifier.Telegram
			token := os.ExpandEnv(tg.BotToken)
			if token != "" {
				globalNotifier = notifier.NewTelegramNotifier(ctx, token, tg.ChatID, tg.AllowedUsers)
				log.Println("[pylon] telegram notifications enabled")
			} else {
				log.Println("[pylon] TELEGRAM_BOT_TOKEN not set, notifications disabled")
			}
		}
	case "slack":
		if global.Defaults.Notifier.Slack != nil {
			sl := global.Defaults.Notifier.Slack
			botToken := os.ExpandEnv(sl.BotToken)
			appToken := os.ExpandEnv(sl.AppToken)
			if botToken != "" && appToken != "" {
				globalNotifier = notifier.NewSlackNotifier(ctx, botToken, appToken, sl.ChannelID, sl.AllowedUsers)
				log.Println("[pylon] slack notifications enabled")
			} else {
				log.Println("[pylon] SLACK_BOT_TOKEN or SLACK_APP_TOKEN not set, notifications disabled")
			}
		}
	}

	// Build per-pylon notifiers for pylons that override the default
	perPylon := make(map[string]notifier.Notifier)
	for name, pyl := range pylons {
		if pyl.Notify == nil {
			continue
		}
		switch pyl.Notify.Type {
		case "telegram":
			if pyl.Notify.Telegram != nil {
				tg := pyl.Notify.Telegram
				token := os.ExpandEnv(tg.BotToken)
				if token != "" {
					perPylon[name] = notifier.NewTelegramNotifier(ctx, token, tg.ChatID, tg.AllowedUsers)
					log.Printf("[pylon] %q: using custom telegram bot", name)
				}
			}
		case "slack":
			if pyl.Notify.Slack != nil {
				sl := pyl.Notify.Slack
				botToken := os.ExpandEnv(sl.BotToken)
				appToken := os.ExpandEnv(sl.AppToken)
				if botToken != "" && appToken != "" {
					perPylon[name] = notifier.NewSlackNotifier(ctx, botToken, appToken, sl.ChannelID, sl.AllowedUsers)
					log.Printf("[pylon] %q: using custom slack bot", name)
				}
			}
		}
	}

	d := daemon.New(global, pylons, st, globalNotifier, perPylon)

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
			path = pyl.Trigger.Cron
		}
		fmt.Printf("  %-24s ok  %s %s\n", name, trigger, path)
	}

	addr := fmt.Sprintf("%s:%d", global.Server.Host, global.Server.Port)
	fmt.Printf("\n%d pylons active -- listening on %s\n\n", len(pylons), addr)

	server := &http.Server{Addr: addr, Handler: d.Mux}

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
