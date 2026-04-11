package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pylonto/pylon/internal/config"
)

func newConstructWizard(name string) wizardModel {
	steps := []StepDef{
		{Key: "name", Create: func() Step {
			return NewTextInputStep(
				"Pylon name",
				"A short, unique name for this pylon (used in paths and URLs).",
				"sentry-triage",
				name,
				false,
			)
		}},
		{Key: "description", Create: func() Step {
			return NewTextInputStep(
				"Description (optional)",
				"A short description of what this pylon does.",
				"Sentry error triage for the nexus project",
				"",
				false,
			)
		}},
		{Key: "trigger", Create: func() Step {
			return NewSelectStep(
				"Trigger",
				"What event starts this pylon?",
				[]selectOption{
					{"Webhook (HTTP POST)", "webhook"},
					{"Cron (scheduled)", "cron"},
				},
			)
		}},
		{Key: "workspace", Create: func() Step {
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
		{Key: "channel_choice", Create: func() Step {
			return NewSelectStep(
				"Channel",
				"Where should this pylon communicate?",
				[]selectOption{
					{"Use default", "default"},
					{"Telegram", "telegram"},
					{"Slack", "slack"},
					{"Webhook (outbound HTTP)", "webhook"},
					{"stdout (console only)", "stdout"},
				},
			)
		}},
		{Key: "agent_choice", Create: func() Step {
			return NewSelectStep(
				"Agent",
				"Which AI agent for this pylon?",
				[]selectOption{
					{"Use default", "default"},
					{"Claude Code", "claude"},
					{"OpenCode", "opencode"},
				},
			)
		}},
		{Key: "prompt", Create: func() Step {
			return NewEditorStep(
				"Default prompt",
				"Use {{ .body.X }} to inject webhook payload fields.\nExamples: {{ .body.issue.title }}, {{ .body.error }}",
				"Investigate this error and suggest a fix: {{ .body.error }}",
			)
		}},
		{Key: "approval", Create: func() Step {
			return NewConfirmStep(
				"Auto-run on trigger?",
				"Yes = agent runs immediately\nNo = you get a notification to approve first",
				true,
			)
		}},
		{Key: "confirm", Create: func() Step {
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
				{Key: "approval.topic", Create: func() Step {
					return NewTextInputStep(
						"Topic name template",
						"The group/thread subject line. Use {{ .body.X }} for webhook fields.",
						"{{ .body.issue.title }}",
						"{{ .body.issue.title }}",
						false,
					)
				}},
				{Key: "approval.message", Create: func() Step {
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
			{Key: "trigger.path", Create: func() Step {
				return NewTextInputStep(
					"Webhook path",
					"The HTTP path that triggers this pylon.",
					"/my-pylon",
					"",
					false,
				)
			}},
		}
	case "cron":
		return []StepDef{
			{Key: "trigger.cron", Create: func() Step {
				return NewCronInputStep(
					"Cron schedule",
					"e.g. 0 9 * * 1-5 (weekdays at 9am)",
					"0 9 * * 1-5",
					"0 9 * * 1-5",
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
			{Key: "workspace.repo", Create: func() Step {
				return NewTextInputStep(
					"Repository URL",
					"Use SSH (git@github.com:user/repo.git) for private repos.",
					"git@github.com:user/repo.git",
					"",
					false,
				)
			}},
			{Key: "workspace.ref", Create: func() Step {
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
			{Key: "workspace.path", Create: func() Step {
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
		return []StepDef{
			{Key: "channel_choice.tg_token", Create: func() Step {
				return NewTextInputStep(
					"Telegram bot token",
					"Use ${ENV_VAR} syntax for environment variables.",
					"${TELEGRAM_BOT_TOKEN}",
					"${TELEGRAM_BOT_TOKEN}",
					false,
				)
			}},
			{Key: "channel_choice.tg_chat_id", Create: func() Step {
				return NewTextInputStep(
					"Telegram chat ID",
					"Numeric chat ID where the bot will post.",
					"123456789",
					"",
					false,
				)
			}},
		}
	case "slack":
		return []StepDef{
			{Key: "channel_choice.slack_bot_token", Create: func() Step {
				return NewTextInputStep(
					"Slack bot token",
					"Use ${ENV_VAR} syntax for environment variables.",
					"${SLACK_BOT_TOKEN}",
					"${SLACK_BOT_TOKEN}",
					false,
				)
			}},
			{Key: "channel_choice.slack_app_token", Create: func() Step {
				return NewTextInputStep(
					"Slack app token",
					"Required for Socket Mode. Use ${ENV_VAR} syntax.",
					"${SLACK_APP_TOKEN}",
					"${SLACK_APP_TOKEN}",
					false,
				)
			}},
			{Key: "channel_choice.slack_channel_id", Create: func() Step {
				return NewTextInputStep(
					"Slack channel ID",
					"The channel where the bot will post (e.g. C1234567890).",
					"C1234567890",
					"",
					false,
				)
			}},
		}
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

	pyl := &config.PylonConfig{
		Name:        name,
		Description: values["description"],
		Created:     time.Now(),
	}

	// Trigger
	pyl.Trigger.Type = values["trigger"]
	switch pyl.Trigger.Type {
	case "webhook":
		path := values["trigger.path"]
		if path == "" {
			path = "/" + name
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		pyl.Trigger.Path = path
	case "cron":
		pyl.Trigger.Cron = values["trigger.cron"]
	}

	// Workspace
	pyl.Workspace.Type = values["workspace"]
	switch pyl.Workspace.Type {
	case "git-clone", "git-worktree":
		pyl.Workspace.Repo = values["workspace.repo"]
		pyl.Workspace.Ref = values["workspace.ref"]
	case "local":
		pyl.Workspace.Path = values["workspace.path"]
	}

	// Agent
	agentChoice := values["agent_choice"]
	if agentChoice != "default" {
		pyl.Agent = &config.PylonAgent{
			Type: agentChoice,
		}
	} else {
		pyl.Agent = &config.PylonAgent{}
	}
	pyl.Agent.Prompt = strings.TrimSpace(values["prompt"])

	// Channel
	channelChoice := values["channel_choice"]
	if channelChoice != "default" {
		pyl.Channel = &config.PylonChannel{
			Type: channelChoice,
		}
		switch channelChoice {
		case "telegram":
			chatID, _ := strconv.ParseInt(values["channel_choice.tg_chat_id"], 10, 64)
			pyl.Channel.Telegram = &config.TelegramConfig{
				BotToken: values["channel_choice.tg_token"],
				ChatID:   chatID,
			}
		case "slack":
			pyl.Channel.Slack = &config.SlackConfig{
				BotToken:  values["channel_choice.slack_bot_token"],
				AppToken:  values["channel_choice.slack_app_token"],
				ChannelID: values["channel_choice.slack_channel_id"],
			}
		}
	}

	// Approval
	if values["approval"] == "no" {
		if pyl.Channel == nil {
			pyl.Channel = &config.PylonChannel{}
		}
		pyl.Channel.Approval = true
		pyl.Channel.Topic = strings.TrimSpace(values["approval.topic"])
		pyl.Channel.Message = strings.TrimSpace(values["approval.message"])
	}

	return config.SavePylon(pyl)
}
