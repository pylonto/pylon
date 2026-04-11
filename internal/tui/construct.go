package tui

import (
	"fmt"
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
			opts := []selectOption{
				{"Use default", "default"},
			}
			// Could add per-pylon channel options here
			opts = append(opts, selectOption{"stdout (console only)", "stdout"})
			return NewSelectStep(
				"Channel",
				"Where should this pylon communicate?",
				opts,
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
				return NewTextInputStep(
					"Cron schedule",
					"e.g. 0 9 * * 1-5 (weekdays at 9am)",
					"0 9 * * 1-5",
					"0 9 * * 1-5",
					false,
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
