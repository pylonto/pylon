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

// constructInputs holds the collected user inputs from the construct wizard.
type constructInputs struct {
	Name          string
	Description   string
	TriggerType   string
	TriggerPath   string
	TriggerCron   string
	TriggerTZ     string
	TriggerURL    string
	WorkspaceType string
	WorkspaceRepo string
	WorkspaceRef  string
	WorkspacePath string
	ChannelChoice string
	Telegram      *config.TelegramConfig
	Slack         *config.SlackConfig
	AgentChoice   string
	Volumes       []string
	Prompt        string
	Approval      bool
	TopicTemplate string
	MsgTemplate   string
}

// buildPylonConfig assembles a PylonConfig from the user's construct inputs.
func buildPylonConfig(in constructInputs) *config.PylonConfig {
	pyl := &config.PylonConfig{
		Name:        in.Name,
		Description: in.Description,
		Created:     time.Now().UTC(),
	}

	// Trigger
	pyl.Trigger.Type = in.TriggerType
	switch in.TriggerType {
	case "webhook":
		path := in.TriggerPath
		if path == "" {
			path = "/" + in.Name
		}
		if path[0] != '/' {
			path = "/" + path
		}
		pyl.Trigger.Path = path
		if in.TriggerURL != "" {
			pyl.Trigger.PublicURL = strings.TrimRight(in.TriggerURL, "/")
		}
	case "cron":
		pyl.Trigger.Cron = in.TriggerCron
		pyl.Trigger.Timezone = in.TriggerTZ
	}

	// Workspace
	pyl.Workspace.Type = in.WorkspaceType
	switch in.WorkspaceType {
	case "git-clone", "git-worktree":
		pyl.Workspace.Repo = in.WorkspaceRepo
		pyl.Workspace.Ref = in.WorkspaceRef
	case "local":
		pyl.Workspace.Path = in.WorkspacePath
	}

	// Channel
	if in.ChannelChoice != "default" {
		pyl.Channel = &config.PylonChannel{Type: in.ChannelChoice}
		switch in.ChannelChoice {
		case "telegram":
			pyl.Channel.Telegram = in.Telegram
		case "slack":
			pyl.Channel.Slack = in.Slack
		}
	}

	// Agent
	if in.AgentChoice != "default" {
		pyl.Agent = &config.PylonAgent{Type: in.AgentChoice}
	}

	// Volumes and prompt
	if len(in.Volumes) > 0 || in.Prompt != "" {
		if pyl.Agent == nil {
			pyl.Agent = &config.PylonAgent{}
		}
		pyl.Agent.Volumes = in.Volumes
		pyl.Agent.Prompt = in.Prompt
	}

	// Approval
	if in.Approval {
		if pyl.Channel == nil {
			pyl.Channel = &config.PylonChannel{}
		}
		pyl.Channel.Approval = true
		pyl.Channel.Topic = in.TopicTemplate
		pyl.Channel.Message = in.MsgTemplate
	}

	return pyl
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
		return constructFromTemplate(name, tmpl)
	}

	fmt.Printf("\nConstructing pylon: %s\n\n", name)

	in := constructInputs{Name: name}

	if err := huh.NewInput().
		Title("Description (optional):").
		Placeholder("Sentry error triage for the nexus project").
		Value(&in.Description).Run(); err != nil {
		return err
	}

	// Trigger
	if err := huh.NewSelect[string]().
		Title("Trigger -- what event starts this pylon?").
		Options(
			huh.NewOption("Webhook (HTTP POST)", "webhook"),
			huh.NewOption("Cron (scheduled)", "cron"),
		).
		Value(&in.TriggerType).Run(); err != nil {
		return err
	}

	switch in.TriggerType {
	case "webhook":
		in.TriggerPath = "/" + name
		if err := huh.NewInput().
			Title("Webhook path:").
			Description(fmt.Sprintf("Your server: http://%s:%d", global.Server.Host, global.Server.Port)).
			Value(&in.TriggerPath).Run(); err != nil {
			return err
		}

		// Public URL override (per-pylon)
		defaultBase := global.Server.PublicURL
		if defaultBase == "" {
			defaultBase = fmt.Sprintf("http://%s:%d", global.Server.Host, global.Server.Port)
		}
		if err := huh.NewInput().
			Title("Public URL for this webhook (optional):").
			Description(fmt.Sprintf("Leave blank to use default: %s", defaultBase)).
			Placeholder("https://my-service.app").
			Value(&in.TriggerURL).Run(); err != nil {
			return err
		}
	case "cron":
		in.TriggerCron = "0 9 * * 1-5"
		if err := huh.NewInput().
			Title("Cron schedule:").
			Description("e.g. 0 9 * * 1-5").
			Value(&in.TriggerCron).Run(); err != nil {
			return err
		}
		fmt.Printf("  Schedule: %s (%s)\n", in.TriggerCron, describeCron(in.TriggerCron))

		// Timezone selection
		detectedTZ := config.DetectSystemTimezone()
		in.TriggerTZ = detectedTZ
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
			Value(&in.TriggerTZ).
			Height(15).
			Run(); err != nil {
			return err
		}
		fmt.Printf("  Timezone: %s\n", in.TriggerTZ)
	default:
		comingSoon(in.TriggerType)
		return nil
	}

	// Workspace
	if err := huh.NewSelect[string]().
		Title("Workspace -- how should the agent access code?").
		Options(
			huh.NewOption("Git clone (fresh clone per job)", "git-clone"),
			huh.NewOption("Git worktree (faster, uses local repo)", "git-worktree"),
			huh.NewOption("Local path (mount a directory)", "local"),
			huh.NewOption("None (no codebase needed)", "none"),
		).
		Value(&in.WorkspaceType).Run(); err != nil {
		return err
	}

	switch in.WorkspaceType {
	case "git-clone", "git-worktree":
		in.WorkspaceRef = "main"
		if err := huh.NewInput().Title("Repo URL:").
			Description("Use SSH (git@github.com:user/repo.git) for private repos").
			Value(&in.WorkspaceRepo).Run(); err != nil {
			return err
		}
		in.WorkspaceRepo = runner.ToSSHURL(in.WorkspaceRepo)
		if err := huh.NewInput().Title("Default branch:").Value(&in.WorkspaceRef).Run(); err != nil {
			return err
		}
		if in.WorkspaceRef == "" {
			in.WorkspaceRef = "main"
		}
	case "local":
		if err := huh.NewInput().Title("Local path:").Value(&in.WorkspacePath).Run(); err != nil {
			return err
		}
	}

	// Channel
	channelLabel := "none"
	if global.Defaults.Channel.Type != "" {
		channelLabel = global.Defaults.Channel.Type
	}

	if err := huh.NewSelect[string]().
		Title("Channel -- where should this pylon send alerts?").
		Options(
			huh.NewOption(fmt.Sprintf("Use default (%s)", channelLabel), "default"),
			huh.NewOption("Telegram (configure new)", "telegram"),
			huh.NewOption("Slack (configure new)", "slack"),
			huh.NewOption("Webhook (generic HTTP POST)", "webhook"),
			huh.NewOption("stdout (console only)", "stdout"),
		).
		Value(&in.ChannelChoice).Run(); err != nil {
		return err
	}

	if in.ChannelChoice != "default" {
		switch in.ChannelChoice {
		case "telegram":
			tg, err := setupTelegram()
			if err != nil {
				return err
			}
			in.Telegram = tg
		case "slack":
			sl, err := setupSlack()
			if err != nil {
				return err
			}
			in.Slack = sl
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

	if err := huh.NewSelect[string]().
		Title("Agent -- which AI agent for this pylon?").
		Options(
			huh.NewOption(fmt.Sprintf("Use default (%s)", agentLabel), "default"),
			huh.NewOption("Claude Code (configure new)", "claude"),
			huh.NewOption("OpenCode (configure new)", "opencode"),
			huh.NewOption("Custom command", "custom"),
		).
		Value(&in.AgentChoice).Run(); err != nil {
		return err
	}

	// Ensure agent image is built
	effectiveType := in.AgentChoice
	if effectiveType == "default" {
		effectiveType = global.Defaults.Agent.Type
		if effectiveType == "" {
			effectiveType = "claude"
		}
	}
	agentimage.Ensure(effectiveType)

	// Volume mounts
	var volumeInput string
	if err := huh.NewInput().
		Title("Volume mounts (optional):").
		Description("Mount host paths into the container. Comma-separated.\nFormat: source:target[:ro|rw]  (default: ro)\nExample: ~/.config/gcloud:/home/pylon/.config/gcloud").
		Placeholder("~/.config/gcloud:/home/pylon/.config/gcloud:ro").
		Value(&volumeInput).Run(); err != nil {
		return err
	}
	if volumeInput != "" {
		for _, v := range strings.Split(volumeInput, ",") {
			v = strings.TrimSpace(v)
			if v != "" {
				in.Volumes = append(in.Volumes, v)
			}
		}
	}

	// Prompt
	if in.TriggerType == "cron" {
		in.Prompt = "Run your scheduled task."
	} else {
		in.Prompt = "Investigate this error and suggest a fix: {{ .body.error }}"
	}
	promptDesc := "The prompt sent to the agent on each scheduled run."
	if in.TriggerType != "cron" {
		promptDesc = "Use {{ .body.X }} to inject webhook payload fields.\nExamples: {{ .body.issue.title }}, {{ .body.error }}, {{ .body.pull_request.head.ref }}"
	}
	if err := huh.NewText().
		Title("Default prompt:").
		Description(promptDesc).
		Value(&in.Prompt).Run(); err != nil {
		return err
	}

	// Approval
	var approvalDesc string
	if in.TriggerType == "cron" {
		approvalDesc = "Yes = you get a notification with Investigate/Ignore buttons\nNo = agent runs immediately on every scheduled run"
	} else {
		approvalDesc = "Yes = you get a notification with Investigate/Ignore buttons (recommended for Sentry)\nNo = agent runs immediately on every webhook"
	}
	if err := huh.NewConfirm().
		Title("Require human approval before agent runs?").
		Description(approvalDesc).
		Value(&in.Approval).Run(); err != nil {
		return err
	}

	if in.Approval {
		in.TopicTemplate = "{{ .body.issue.title }}"
		if err := huh.NewInput().
			Title("Topic name template:").
			Description("The group/thread subject line. Use {{ .body.X }} for webhook fields.").
			Value(&in.TopicTemplate).Run(); err != nil {
			return err
		}

		in.MsgTemplate = "{{ .body.issue.title }}\n{{ .body.error }}"
		if err := huh.NewText().
			Title("Notification message template:").
			Description("Shown in Telegram above the Investigate/Ignore buttons.\nUse {{ .body.X }} for webhook fields.").
			Value(&in.MsgTemplate).Run(); err != nil {
			return err
		}
	}

	pyl := buildPylonConfig(in)

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
