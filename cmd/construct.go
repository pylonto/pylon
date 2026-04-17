package cmd

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/proxy"
	"github.com/pylonto/pylon/internal/tui"
)

func init() {
	constructCmd.Flags().String("from", "", "Create from template (sentry, github-pr, cron-audit, blank)")
	rootCmd.AddCommand(constructCmd)
}

var constructCmd = &cobra.Command{
	Use:   "construct <name>",
	Short: "Create a new pylon pipeline",
	Args:  cobra.ExactArgs(1),
	RunE:  runConstruct,
}

func runConstruct(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Reject if a pylon with this name already exists.
	if _, err := config.LoadPylon(name); err == nil {
		return fmt.Errorf("pylon %q already exists", name)
	}

	// Template path bypasses the wizard entirely.
	if tmpl, _ := cmd.Flags().GetString("from"); tmpl != "" {
		return constructFromTemplate(name, tmpl)
	}

	if err := requireTTY(); err != nil {
		return err
	}

	// Load env so the wizard can reuse SLACK_* / TELEGRAM_BOT_TOKEN from the
	// global .env without requiring the user to re-enter them.
	config.LoadEnv()

	wiz := tui.NewConstructWizard(name)
	if _, err := tea.NewProgram(wiz, tea.WithAltScreen()).Run(); err != nil {
		return err
	}

	// Surface save errors so scripts see a non-zero exit code.
	if wiz.Err != nil {
		return wiz.Err
	}

	// User canceled (ESC/Ctrl+C) or declined at the final confirm step.
	if !wiz.Saved {
		fmt.Println("Construction canceled -- no pylon saved.")
		return nil
	}

	// Post-construct summary. The bubbletea alt-screen suppresses inline
	// prints during the wizard, so this is the first output the user sees
	// after it exits.
	global, err := config.LoadGlobal()
	if err != nil {
		return fmt.Errorf("loading global config for summary: %w", err)
	}
	pyl, err := config.LoadPylon(name)
	if err != nil {
		return fmt.Errorf("reloading saved pylon %q: %w", name, err)
	}
	fmt.Printf("\nPylon constructed: %s\n", name)
	fmt.Printf("  Config: %s\n", config.PylonPath(name))
	if pyl.Trigger.Type == "webhook" {
		fmt.Printf("  Webhook: %s\n", pyl.ResolvePublicURL(global))
		proxy.PrintHints(pyl.Trigger.Path, global.Server.Port)
	}
	fmt.Printf("\n  Start it:  pylon start %s\n", name)
	fmt.Printf("  Test it:   pylon test %s\n", name)
	fmt.Printf("  Edit it:   pylon edit %s\n\n", name)
	return nil
}

func constructFromTemplate(name, tmpl string) error {
	pyl := &config.PylonConfig{
		Name:    name,
		Created: time.Now().UTC(),
	}

	switch tmpl {
	case "sentry":
		pyl.Trigger = config.TriggerConfig{Type: "webhook", Path: "/" + name}
		pyl.Workspace = config.WorkspaceConfig{Type: "git-clone"}
		pyl.Channel = &config.PylonChannel{
			Message:  "{{ .body.data.event.title }}\n{{ .body.data.event.culprit }}\n{{ .body.data.event.web_url }}",
			Approval: true,
		}
		pyl.Agent = &config.PylonAgent{
			Prompt:  "Investigate this Sentry error and suggest a fix.\n\nTitle: {{ .body.data.event.title }}\nCulprit: {{ .body.data.event.culprit }}\nLevel: {{ .body.data.event.level }}\nPlatform: {{ .body.data.event.platform }}\nSentry URL: {{ .body.data.event.web_url }}",
			Timeout: "10m",
		}
	case "github-pr":
		pyl.Trigger = config.TriggerConfig{Type: "webhook", Path: "/" + name}
		pyl.Workspace = config.WorkspaceConfig{Type: "git-clone",
			Repo: "{{ .body.repository.clone_url }}", Ref: "{{ .body.pull_request.head.ref }}"}
		pyl.Channel = &config.PylonChannel{
			Message:  "PR #{{ .body.number }}: {{ .body.pull_request.title }}",
			Approval: false,
		}
		pyl.Agent = &config.PylonAgent{
			Prompt:  "Review this pull request. Check for bugs, security issues, and suggest improvements.",
			Timeout: "15m",
		}
	case "cron-audit":
		pyl.Trigger = config.TriggerConfig{Type: "cron", Cron: "0 9 * * 1", Timezone: config.DetectSystemTimezone()}
		pyl.Workspace = config.WorkspaceConfig{Type: "git-clone"}
		pyl.Channel = &config.PylonChannel{Message: "Weekly codebase audit", Approval: false}
		pyl.Agent = &config.PylonAgent{
			Prompt:  "Audit this codebase for security vulnerabilities, outdated dependencies, and code quality issues. Provide a summary report.",
			Timeout: "30m",
		}

		var repo string
		ref := "main"
		if err := huh.NewInput().Title("Repo URL:").Value(&repo).Run(); err != nil {
			return err
		}
		if err := huh.NewInput().Title("Default branch:").Value(&ref).Run(); err != nil {
			return err
		}
		if ref == "" {
			ref = "main"
		}
		pyl.Workspace.Repo = repo
		pyl.Workspace.Ref = ref
	case "blank":
		pyl.Trigger = config.TriggerConfig{Type: "webhook", Path: "/" + name}
		pyl.Workspace = config.WorkspaceConfig{Type: "none"}
		pyl.Agent = &config.PylonAgent{Prompt: ""}
	default:
		return fmt.Errorf("unknown template %q (available: sentry, github-pr, cron-audit, blank)", tmpl)
	}

	if err := config.SavePylon(pyl); err != nil {
		return fmt.Errorf("saving pylon: %w", err)
	}

	fmt.Printf("\nPylon constructed from template %q: %s\n", tmpl, name)
	fmt.Printf("  Config: %s\n", config.PylonPath(name))
	fmt.Printf("  Edit it: pylon edit %s\n\n", name)
	return nil
}
