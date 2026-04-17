package tui

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pylonto/pylon/internal/agentimage"
	"github.com/pylonto/pylon/internal/config"
)

type pylonExample struct {
	Name        string
	Description string
}

var pylonExamples = []pylonExample{
	// Monitoring / Incident
	{"sentry-triage", "Triage Sentry errors for the payments service"},
	{"incident-responder", "Investigate and summarize production incidents"},
	{"alert-analyzer", "Analyze and categorize monitoring alerts"},

	// Code / CI
	{"pr-reviewer", "Review pull requests for bugs and style issues"},
	{"test-fixer", "Investigate and fix failing CI tests"},
	{"deploy-watcher", "Monitor deployments and flag regressions"},

	// Security
	{"vuln-scanner", "Scan dependencies for security vulnerabilities"},
	{"cve-patcher", "Patch known CVEs in project dependencies"},

	// Documentation / Reporting
	{"changelog-writer", "Generate changelogs from merged pull requests"},
	{"release-notes", "Draft release notes on new tag push"},

	// Maintenance
	{"dependency-updater", "Review and update stale dependencies"},
	{"migration-checker", "Review database migrations for safety"},
	{"log-analyzer", "Analyze application logs and surface anomalies"},

	// Communication
	{"slack-digest", "Summarize Slack channels into a daily digest"},
	{"standup-bot", "Generate standup summaries from recent commits"},

	// Productivity
	{"gmail-summarizer", "Summarize unread emails into a morning briefing"},
	{"meeting-prep", "Research attendees and topics before meetings"},
	{"expense-tracker", "Categorize and flag expenses from receipts"},
	{"competitor-watch", "Monitor competitor product changes and news"},
	{"lead-qualifier", "Score and research inbound sales leads"},
	{"support-responder", "Draft replies to customer support tickets"},
	{"content-repurposer", "Turn blog posts into social media threads"},
	{"job-screener", "Screen resumes against role requirements"},
	{"invoice-processor", "Extract and validate invoice line items"},
	{"feedback-sorter", "Categorize user feedback by theme and urgency"},

	// Branded
	{"linear-triage", "Triage and prioritize new Linear issues"},
	{"datadog-investigator", "Investigate Datadog alert anomalies"},
	{"pagerduty-responder", "Analyze PagerDuty incidents and suggest fixes"},
	{"github-stalebot", "Review and close stale GitHub issues"},
	{"bigquery-reporter", "Generate weekly reports from BigQuery data"},
	{"stripe-reconciler", "Reconcile Stripe payments against invoices"},
	{"notion-organizer", "Clean up and restructure Notion workspaces"},
	{"apollo-enricher", "Enrich Apollo contacts with public data"},
	{"vercel-previewer", "Review Vercel preview deployments for issues"},
	{"ramp-categorizer", "Auto-categorize Ramp transactions"},
	{"zendesk-responder", "Draft responses to Zendesk support tickets"},
	{"grafana-summarizer", "Summarize Grafana dashboard trends daily"},
	{"shopify-monitor", "Monitor Shopify store health and inventory"},
}

func newConstructWizard(name string) wizardModel {
	ex := pylonExamples[rand.IntN(len(pylonExamples))]

	steps := []StepDef{
		{Key: "name", Create: func(_ map[string]string) Step {
			return NewTextInputStep(
				"Pylon name",
				"A short, unique name for this pylon (used in paths and URLs).",
				ex.Name,
				name,
				false,
			)
		}},
		{Key: "description", Create: func(_ map[string]string) Step {
			return NewTextInputStep(
				"Description (optional)",
				"A short description of what this pylon does.",
				ex.Description,
				"",
				false,
			)
		}},
		{Key: "trigger", Create: func(_ map[string]string) Step {
			return NewSelectStep(
				"Trigger",
				"What event starts this pylon?",
				[]selectOption{
					{"Webhook (HTTP POST)", "webhook"},
					{"Cron (scheduled)", "cron"},
				},
			)
		}},
		{Key: "workspace", Create: func(_ map[string]string) Step {
			return NewSelectStep(
				"Workspace",
				"How should the agent access code?",
				[]selectOption{
					{"Git clone (fresh clone per job)", "git-clone"},
					{"Git worktree (faster, uses local repo)", "git-worktree"},
					{"Local path (mount a directory)", "local"},
					{"None (no codebase needed)", "none"},
				},
			)
		}},
		{Key: "channel_choice", Create: func(_ map[string]string) Step {
			channelDefault := "Use default"
			if g, err := config.LoadGlobal(); err == nil && g.Defaults.Channel.Type != "" {
				channelDefault += " (" + g.Defaults.Channel.Type + ")"
			}
			return NewSelectStep(
				"Channel",
				"Where should this pylon communicate?",
				[]selectOption{
					{channelDefault, "default"},
					{"Telegram", "telegram"},
					{"Slack", "slack"},
					{"Webhook (outbound HTTP)", "webhook"},
					{"stdout (console only)", "stdout"},
				},
			)
		}},
		{Key: "agent_choice", Create: func(_ map[string]string) Step {
			agentDefault := "Use default"
			if g, err := config.LoadGlobal(); err == nil && g.Defaults.Agent.Type != "" {
				agentDefault += " (" + g.Defaults.Agent.Type + ")"
			}
			return NewSelectStep(
				"Agent",
				"Which AI agent for this pylon?",
				[]selectOption{
					{agentDefault, "default"},
					{"Claude Code", "claude"},
					{"OpenCode", "opencode"},
				},
			)
		}},
		{Key: "volumes", Create: func(_ map[string]string) Step {
			return NewTextInputStep(
				"Volume mounts (optional)",
				"Comma-separated host paths to mount into the container.\nFormat: source:target[:ro|rw]  (default: ro). Leave blank for none.",
				"~/.config/gcloud:/home/pylon/.config/gcloud:ro",
				"",
				false,
			)
		}},
		{Key: "prompt", Create: func(_ map[string]string) Step {
			return NewEditorStep(
				"Default prompt",
				"Use {{ .body.X }} to inject webhook payload fields.\nExamples: {{ .body.issue.title }}, {{ .body.error }}",
				"Investigate this error and suggest a fix: {{ .body.error }}",
			)
		}},
		{Key: "approval", Create: func(_ map[string]string) Step {
			return NewConfirmStep(
				"Auto-run on trigger?",
				"Yes = agent runs immediately\nNo = you get a notification to approve first",
				true,
			)
		}},
		{Key: "summary", Create: func(values map[string]string) Step {
			return NewInfoStep(
				"Review",
				"Press enter to save this pylon with the settings above.",
				buildSummary(values),
			)
		}},
		{Key: "confirm", Create: func(_ map[string]string) Step {
			return NewConfirmStep(
				"Save this pylon?",
				"",
				true,
			)
		}},
	}

	return newWizardModel("Construct", steps, constructOnStepDone, constructOnComplete)
}

func constructOnStepDone(key, value string, values map[string]string) []StepDef {
	switch key {
	case "trigger":
		return triggerSteps(value)
	case "workspace":
		return workspaceSteps(value)
	case "channel_choice":
		return constructChannelSteps(value)
	case "approval":
		if value == "no" {
			return []StepDef{
				{Key: "approval.topic", Create: func(_ map[string]string) Step {
					return NewTextInputStep(
						"Topic name template",
						"The group/thread subject line. Use {{ .body.X }} for webhook fields.",
						"{{ .body.issue.title }}",
						"{{ .body.issue.title }}",
						false,
					)
				}},
				{Key: "approval.message", Create: func(_ map[string]string) Step {
					return NewEditorStep(
						"Notification message template",
						"Shown above the Investigate/Ignore buttons. Use {{ .body.X }} for webhook fields.",
						"{{ .body.issue.title }}\n{{ .body.error }}",
					)
				}},
			}
		}
	}
	return nil
}

func triggerSteps(triggerType string) []StepDef {
	switch triggerType {
	case "webhook":
		return []StepDef{
			{Key: "trigger.path", Create: func(_ map[string]string) Step {
				return NewTextInputStep(
					"Webhook path",
					"The HTTP path that triggers this pylon.",
					"/my-pylon",
					"",
					false,
				)
			}},
			{Key: "trigger.public_url", Create: func(_ map[string]string) Step {
				return NewTextInputStep(
					"Public URL (optional)",
					"Override the global public URL for this pylon. Leave blank to use the default.",
					"https://agent-arnold.app",
					"",
					false,
				)
			}},
		}
	case "cron":
		detectedTZ := config.DetectSystemTimezone()
		return []StepDef{
			{Key: "trigger.cron", Create: func(_ map[string]string) Step {
				return NewCronInputStep(
					"Cron schedule",
					"e.g. 0 9 * * 1-5 (weekdays at 9am)",
					"0 9 * * 1-5",
					"0 9 * * 1-5",
				)
			}},
			{Key: "trigger.timezone", Create: func(_ map[string]string) Step {
				return NewFilterSelectStep(
					"Timezone",
					"Schedule fires in this timezone. Auto-detected: "+detectedTZ,
					TimezoneOptions(),
					detectedTZ,
				)
			}},
		}
	}
	return nil
}

func workspaceSteps(wsType string) []StepDef {
	switch wsType {
	case "git-clone", "git-worktree":
		return []StepDef{
			{Key: "workspace.repo", Create: func(_ map[string]string) Step {
				return NewTextInputStep(
					"Repository URL",
					"Use SSH (git@github.com:user/repo.git) for private repos.",
					"git@github.com:user/repo.git",
					"",
					false,
				)
			}},
			{Key: "workspace.ref", Create: func(_ map[string]string) Step {
				return NewTextInputStep(
					"Default branch",
					"",
					"main",
					"main",
					false,
				)
			}},
		}
	case "local":
		return []StepDef{
			{Key: "workspace.path", Create: func(_ map[string]string) Step {
				return NewTextInputStep(
					"Local path",
					"Absolute path to mount into the agent container.",
					"/home/user/project",
					"",
					false,
				)
			}},
		}
	}
	return nil
}

func constructChannelSteps(channelType string) []StepDef {
	switch channelType {
	case "telegram":
		return TelegramSteps("channel_choice")
	case "slack":
		return SlackSteps("channel_choice")
	}
	return nil
}

func constructOnComplete(values map[string]string) error {
	if values["confirm"] != "yes" {
		return nil
	}

	name := values["name"]
	if name == "" {
		return fmt.Errorf("pylon name is required")
	}

	in := pylonInputsFromValues(name, values)
	pyl := config.BuildPylon(in)
	if err := config.SavePylon(pyl); err != nil {
		return fmt.Errorf("saving pylon: %w", err)
	}
	if err := config.SavePylonSecrets(name, secretsFromValues(values)); err != nil {
		return fmt.Errorf("saving pylon secrets: %w", err)
	}

	effectiveAgent := in.AgentChoice
	if effectiveAgent == "" || effectiveAgent == "default" {
		if g, err := config.LoadGlobal(); err == nil && g.Defaults.Agent.Type != "" {
			effectiveAgent = g.Defaults.Agent.Type
		} else {
			effectiveAgent = "claude"
		}
	}
	agentimage.Ensure(effectiveAgent)

	return nil
}

// pylonInputsFromValues translates the wizard's values map into the shared
// PylonInputs struct consumed by config.BuildPylon.
func pylonInputsFromValues(name string, values map[string]string) config.PylonInputs {
	in := config.PylonInputs{
		Name:          name,
		Description:   values["description"],
		TriggerType:   values["trigger"],
		TriggerPath:   values["trigger.path"],
		TriggerCron:   values["trigger.cron"],
		TriggerTZ:     values["trigger.timezone"],
		TriggerURL:    values["trigger.public_url"],
		WorkspaceType: values["workspace"],
		WorkspaceRepo: values["workspace.repo"],
		WorkspaceRef:  values["workspace.ref"],
		WorkspacePath: values["workspace.path"],
		ChannelChoice: values["channel_choice"],
		AgentChoice:   values["agent_choice"],
		Prompt:        strings.TrimSpace(values["prompt"]),
		Approval:      values["approval"] == "no",
		TopicTemplate: strings.TrimSpace(values["approval.topic"]),
		MsgTemplate:   strings.TrimSpace(values["approval.message"]),
	}

	if in.AgentChoice == "" {
		in.AgentChoice = "default"
	}

	for _, v := range strings.Split(values["volumes"], ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			in.Volumes = append(in.Volumes, v)
		}
	}

	switch in.ChannelChoice {
	case "telegram":
		chatID, _ := strconv.ParseInt(values["channel_choice.tg_chat_id"], 10, 64)
		in.Telegram = &config.TelegramConfig{
			BotToken: "${TELEGRAM_BOT_TOKEN}",
			ChatID:   chatID,
		}
	case "slack":
		in.Slack = &config.SlackConfig{
			BotToken:  "${SLACK_BOT_TOKEN}",
			AppToken:  "${SLACK_APP_TOKEN}",
			ChannelID: values["channel_choice.slack_channel_id"],
		}
	}

	return in
}

// secretsFromValues extracts raw tokens from the wizard values so they can be
// written to the per-pylon .env file. Empty / placeholder values are skipped.
func secretsFromValues(values map[string]string) map[string]string {
	secrets := map[string]string{}
	switch values["channel_choice"] {
	case "telegram":
		if tok := values["channel_choice.tg_token"]; isPlausibleSecret(tok) {
			secrets["TELEGRAM_BOT_TOKEN"] = tok
		}
	case "slack":
		if tok := values["channel_choice.slack_bot_token"]; isPlausibleSecret(tok) {
			secrets["SLACK_BOT_TOKEN"] = tok
		}
		if tok := values["channel_choice.slack_app_token"]; isPlausibleSecret(tok) {
			secrets["SLACK_APP_TOKEN"] = tok
		}
	}
	return secrets
}

// isPlausibleSecret returns false for empty strings and for the
// "Verified @user (from env)" results returned by env-reuse async steps.
func isPlausibleSecret(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "Verified ") || strings.HasPrefix(s, "Using token") {
		return false
	}
	return true
}

// buildSummary renders a terse YAML-ish preview of the pylon about to be saved,
// used by the "Review" info step.
func buildSummary(values map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Name:        %s\n", values["name"])
	if d := values["description"]; d != "" {
		fmt.Fprintf(&b, "Description: %s\n", d)
	}
	fmt.Fprintf(&b, "Trigger:     %s", values["trigger"])
	switch values["trigger"] {
	case "webhook":
		fmt.Fprintf(&b, " (%s)", values["trigger.path"])
	case "cron":
		fmt.Fprintf(&b, " (%s, %s)", values["trigger.cron"], values["trigger.timezone"])
	}
	fmt.Fprintf(&b, "\nWorkspace:   %s", values["workspace"])
	if repo := values["workspace.repo"]; repo != "" {
		fmt.Fprintf(&b, " (%s @ %s)", repo, values["workspace.ref"])
	}
	fmt.Fprintf(&b, "\nChannel:     %s\n", values["channel_choice"])
	fmt.Fprintf(&b, "Agent:       %s\n", values["agent_choice"])
	// "approval" holds the answer to "Auto-run on trigger?" (yes=auto, no=require approval).
	// Label the summary with the same question so the meaning doesn't flip.
	fmt.Fprintf(&b, "Auto-run:    %s\n", values["approval"])
	return b.String()
}

// NewConstructWizard returns a standalone tea.Model wrapping the construct
// wizard, for use as `pylon construct <name>`. After Run() returns the caller
// inspects Saved/Err to distinguish success, user-cancel, and save-error.
func NewConstructWizard(name string) *ConstructWizardModel {
	return &ConstructWizardModel{
		name: name,
		wiz:  newConstructWizard(name),
	}
}

// ConstructWizardModel is a thin tea.Model wrapper around wizardModel for the
// standalone `pylon construct` CLI. Exported so cmd/construct.go can read the
// completion state after tea.Program returns.
//
// Exit-state contract (caller must check in this order):
//
//  1. Err != nil       -- the wizard's onComplete returned an error
//     (e.g. SavePylon failed); CLI should return Err.
//  2. Saved == false   -- user canceled (ESC/Ctrl+C) or declined at the
//     final "Save this pylon?" confirm step; CLI
//     should print a cancel message and exit 0.
//  3. Saved == true    -- pylon persisted; CLI should print the summary.
type ConstructWizardModel struct {
	name   string
	wiz    wizardModel
	width  int
	height int
	Saved  bool
	Err    error
}

func (m *ConstructWizardModel) Init() tea.Cmd { return m.wiz.Init() }

func (m *ConstructWizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			if c, ok := m.wiz.active.(Cancelable); ok {
				c.Cancel()
			}
			return m, tea.Quit
		}
	case wizardCompleteMsg:
		// constructOnComplete returns nil both for a real save and for the
		// "confirm=no" early-out. Distinguish by checking the confirm value.
		if m.wiz.values["confirm"] == "yes" {
			m.Saved = true
		}
		return m, tea.Quit
	}

	var cmd tea.Cmd
	m.wiz, cmd = m.wiz.Update(msg)
	// onComplete errors keep the wizard open in the underlying wizardModel;
	// promote them to a hard exit so the CLI can surface the error.
	if m.wiz.err != nil && m.Err == nil {
		m.Err = m.wiz.err
		return m, tea.Quit
	}
	return m, cmd
}

func (m *ConstructWizardModel) View() string {
	width := m.width
	if width < 60 {
		return "\n  terminal too narrow -- resize to at least 60 columns\n"
	}
	return "\n" + m.wiz.View(width, m.height) + "\n"
}
