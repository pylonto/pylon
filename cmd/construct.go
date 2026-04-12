package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/pylonto/pylon/internal/agentimage"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/proxy"
	"github.com/pylonto/pylon/internal/runner"
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

	// Check if pylon already exists
	if _, err := config.LoadPylon(name); err == nil {
		return fmt.Errorf("pylon %q already exists", name)
	}

	// Load global config for defaults
	global, err := config.LoadGlobal()
	if err != nil {
		fmt.Println("Warning: no global config found. Run `pylon setup` first for defaults.")
		global = &config.GlobalConfig{}
		global.Server.Port = 8080
	}

	// Check for template
	if tmpl, _ := cmd.Flags().GetString("from"); tmpl != "" {
		return constructFromTemplate(name, tmpl, global)
	}

	fmt.Printf("\nConstructing pylon: %s\n\n", name)

	var description string
	if err := huh.NewInput().
		Title("Description (optional):").
		Placeholder("Sentry error triage for the nexus project").
		Value(&description).Run(); err != nil {
		return err
	}

	pyl := &config.PylonConfig{
		Name:        name,
		Description: description,
		Created:     time.Now().UTC(),
	}

	// Trigger
	var triggerType string
	if err := huh.NewSelect[string]().
		Title("Trigger -- what event starts this pylon?").
		Options(
			huh.NewOption("Webhook (HTTP POST)", "webhook"),
			huh.NewOption("Cron (scheduled)", "cron"),
			huh.NewOption("Chat command (coming soon)", "chat"),
			huh.NewOption("API call (coming soon)", "api"),
		).
		Value(&triggerType).Run(); err != nil {
		return err
	}

	pyl.Trigger.Type = triggerType
	switch triggerType {
	case "webhook":
		path := "/" + name
		if err := huh.NewInput().
			Title("Webhook path:").
			Description(fmt.Sprintf("Your server: http://%s:%d", global.Server.Host, global.Server.Port)).
			Value(&path).Run(); err != nil {
			return err
		}
		if path == "" {
			path = "/" + name
		}
		if path[0] != '/' {
			path = "/" + path
		}
		pyl.Trigger.Path = path

		// Public URL override (per-pylon)
		defaultBase := global.Server.PublicURL
		if defaultBase == "" {
			defaultBase = fmt.Sprintf("http://%s:%d", global.Server.Host, global.Server.Port)
		}
		var publicURL string
		if err := huh.NewInput().
			Title("Public URL for this webhook (optional):").
			Description(fmt.Sprintf("Leave blank to use default: %s", defaultBase)).
			Placeholder("https://my-service.app").
			Value(&publicURL).Run(); err != nil {
			return err
		}
		if publicURL != "" {
			pyl.Trigger.PublicURL = strings.TrimRight(publicURL, "/")
		}
	case "cron":
		schedule := "0 9 * * 1-5"
		if err := huh.NewInput().
			Title("Cron schedule:").
			Description("e.g. 0 9 * * 1-5").
			Value(&schedule).Run(); err != nil {
			return err
		}
		pyl.Trigger.Cron = schedule
		fmt.Printf("  Schedule: %s (%s)\n", schedule, describeCron(schedule))

		// Timezone selection
		detectedTZ := config.DetectSystemTimezone()
		timezone := detectedTZ
		var tzOptions []huh.Option[string]
		for _, tz := range config.TimezoneList() {
			label := tz
			if tz == detectedTZ {
				label += " (detected)"
			}
			tzOptions = append(tzOptions, huh.NewOption(label, tz))
		}
		if err := huh.NewSelect[string]().
			Title("Timezone:").
			Description(fmt.Sprintf("Auto-detected: %s", detectedTZ)).
			Options(tzOptions...).
			Value(&timezone).
			Height(15).
			Run(); err != nil {
			return err
		}
		pyl.Trigger.Timezone = timezone
		fmt.Printf("  Timezone: %s\n", timezone)
	default:
		comingSoon(triggerType)
		return nil
	}

	// Workspace
	var wsType string
	if err := huh.NewSelect[string]().
		Title("Workspace -- how should the agent access code?").
		Options(
			huh.NewOption("Git clone (fresh clone per job)", "git-clone"),
			huh.NewOption("Git worktree (faster, uses local repo)", "git-worktree"),
			huh.NewOption("Local path (mount a directory)", "local"),
			huh.NewOption("None (no codebase needed)", "none"),
		).
		Value(&wsType).Run(); err != nil {
		return err
	}

	pyl.Workspace.Type = wsType
	switch wsType {
	case "git-clone", "git-worktree":
		var repo string
		ref := "main"
		if err := huh.NewInput().Title("Repo URL:").
			Description("Use SSH (git@github.com:user/repo.git) for private repos").
			Value(&repo).Run(); err != nil {
			return err
		}
		repo = runner.ToSSHURL(repo)
		if err := huh.NewInput().Title("Default branch:").Value(&ref).Run(); err != nil {
			return err
		}
		if ref == "" {
			ref = "main"
		}
		pyl.Workspace.Repo = repo
		pyl.Workspace.Ref = ref
	case "local":
		var path string
		if err := huh.NewInput().Title("Local path:").Value(&path).Run(); err != nil {
			return err
		}
		pyl.Workspace.Path = path
	}

	// Channel
	channelLabel := "none"
	if global.Defaults.Channel.Type != "" {
		channelLabel = global.Defaults.Channel.Type
	}

	var channelChoice string
	if err := huh.NewSelect[string]().
		Title("Channel -- where should this pylon send alerts?").
		Options(
			huh.NewOption(fmt.Sprintf("Use default (%s)", channelLabel), "default"),
			huh.NewOption("Telegram (configure new)", "telegram"),
			huh.NewOption("Slack (configure new)", "slack"),
			huh.NewOption("Discord (coming soon)", "discord"),
			huh.NewOption("WhatsApp (coming soon)", "whatsapp"),
			huh.NewOption("iMessage (coming soon)", "imessage"),
			huh.NewOption("Webhook (generic HTTP POST)", "webhook"),
			huh.NewOption("stdout (console only)", "stdout"),
		).
		Value(&channelChoice).Run(); err != nil {
		return err
	}

	if channelChoice != "default" {
		switch channelChoice {
		case "slack":
			sl, err := setupSlack()
			if err != nil {
				return err
			}
			pyl.Channel = &config.PylonChannel{Type: "slack", Slack: sl}
		case "discord", "whatsapp", "imessage":
			comingSoon(channelChoice)
		default:
			pyl.Channel = &config.PylonChannel{Type: channelChoice}
		}
	}

	// Agent
	agentLabel := "none"
	if global.Defaults.Agent.Type != "" {
		agentLabel = global.Defaults.Agent.Type
		if global.Defaults.Agent.Claude != nil {
			agentLabel = fmt.Sprintf("Claude Code, %s", global.Defaults.Agent.Claude.Auth)
		} else if global.Defaults.Agent.OpenCode != nil {
			if global.Defaults.Agent.OpenCode.Auth == "api-key" {
				agentLabel = fmt.Sprintf("OpenCode, %s", global.Defaults.Agent.OpenCode.Provider)
			} else {
				agentLabel = "OpenCode, Zen"
			}
		}
	}

	var agentChoice string
	if err := huh.NewSelect[string]().
		Title("Agent -- which AI agent for this pylon?").
		Options(
			huh.NewOption(fmt.Sprintf("Use default (%s)", agentLabel), "default"),
			huh.NewOption("Claude Code (configure new)", "claude"),
			huh.NewOption("OpenCode (configure new)", "opencode"),
			huh.NewOption("Codex (coming soon)", "codex"),
			huh.NewOption("Aider (coming soon)", "aider"),
			huh.NewOption("Custom command", "custom"),
		).
		Value(&agentChoice).Run(); err != nil {
		return err
	}

	if agentChoice != "default" {
		switch agentChoice {
		case "codex", "aider":
			comingSoon(agentChoice)
		default:
			pyl.Agent = &config.PylonAgent{Type: agentChoice}
		}
	}

	// Ensure agent image is built
	effectiveType := agentChoice
	if effectiveType == "default" {
		effectiveType = global.Defaults.Agent.Type
		if effectiveType == "" {
			effectiveType = "claude"
		}
	}
	agentimage.Ensure(effectiveType)

	// Host tools (from global config)
	if len(global.Tools) > 0 {
		var options []huh.Option[string]
		for _, t := range global.Tools {
			options = append(options, huh.NewOption(fmt.Sprintf("%s (%s)", t.Name, t.Path), t.Name))
		}

		var toolChoices []string
		if err := huh.NewMultiSelect[string]().
			Title("Host tools -- which CLIs can this agent use?").
			Description("Select the tools this agent is allowed to execute on the host.\nLeave empty for none.").
			Options(options...).
			Value(&toolChoices).Run(); err != nil {
			return err
		}

		if len(toolChoices) > 0 {
			if pyl.Agent == nil {
				pyl.Agent = &config.PylonAgent{}
			}
			// Copy full ToolConfig for selected tools
			for _, name := range toolChoices {
				if t := global.ToolByName(name); t != nil {
					pyl.Agent.Tools = append(pyl.Agent.Tools, *t)
				}
			}
		}
	}

	// Prompt
	prompt := "Investigate this error and suggest a fix: {{ .body.error }}"
	if err := huh.NewText().
		Title("Default prompt:").
		Description("Use {{ .body.X }} to inject webhook payload fields.\nExamples: {{ .body.issue.title }}, {{ .body.error }}, {{ .body.pull_request.head.ref }}").
		Value(&prompt).Run(); err != nil {
		return err
	}
	if pyl.Agent == nil {
		pyl.Agent = &config.PylonAgent{}
	}
	pyl.Agent.Prompt = prompt

	// Approval
	var approval bool
	if err := huh.NewConfirm().
		Title("Require human approval before agent runs?").
		Description("Yes = you get a notification with Investigate/Ignore buttons (recommended for Sentry)\nNo = agent runs immediately on every webhook").
		Value(&approval).Run(); err != nil {
		return err
	}

	if pyl.Channel == nil {
		pyl.Channel = &config.PylonChannel{}
	}
	pyl.Channel.Approval = approval

	if approval {
		topicTemplate := "{{ .body.issue.title }}"
		if err := huh.NewInput().
			Title("Topic name template:").
			Description("The group/thread subject line. Use {{ .body.X }} for webhook fields.").
			Value(&topicTemplate).Run(); err != nil {
			return err
		}
		if topicTemplate != "" {
			pyl.Channel.Topic = topicTemplate
		}

		msgTemplate := "{{ .body.issue.title }}\n{{ .body.error }}"
		if err := huh.NewText().
			Title("Notification message template:").
			Description("Shown in Telegram above the Investigate/Ignore buttons.\nUse {{ .body.X }} for webhook fields.").
			Value(&msgTemplate).Run(); err != nil {
			return err
		}
		if msgTemplate != "" {
			pyl.Channel.Message = msgTemplate
		}
	}

	// Save
	if err := config.SavePylon(pyl); err != nil {
		return fmt.Errorf("saving pylon: %w", err)
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

func constructFromTemplate(name, tmpl string, global *config.GlobalConfig) error {
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
