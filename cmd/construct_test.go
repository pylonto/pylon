package cmd

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/pylonto/pylon/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPylonConfig(t *testing.T) {
	tests := []struct {
		name  string
		input constructInputs
		check func(t *testing.T, pyl *config.PylonConfig)
	}{
		{
			name: "telegram + webhook",
			input: constructInputs{
				Name:          "tg-webhook",
				Description:   "telegram webhook pylon",
				TriggerType:   "webhook",
				TriggerPath:   "/tg-webhook",
				WorkspaceType: "none",
				ChannelChoice: "telegram",
				Telegram: &config.TelegramConfig{
					BotToken: "${TELEGRAM_BOT_TOKEN}",
					ChatID:   123,
				},
				AgentChoice: "default",
				Prompt:      "test prompt",
			},
			check: func(t *testing.T, pyl *config.PylonConfig) {
				assert.Equal(t, "webhook", pyl.Trigger.Type)
				assert.Equal(t, "/tg-webhook", pyl.Trigger.Path)

				require.NotNil(t, pyl.Channel)
				assert.Equal(t, "telegram", pyl.Channel.Type)
				require.NotNil(t, pyl.Channel.Telegram)
				assert.Equal(t, "${TELEGRAM_BOT_TOKEN}", pyl.Channel.Telegram.BotToken)
				assert.Equal(t, int64(123), pyl.Channel.Telegram.ChatID)
			},
		},
		{
			name: "telegram + cron",
			input: constructInputs{
				Name:          "tg-cron",
				TriggerType:   "cron",
				TriggerCron:   "0 9 * * 1-5",
				TriggerTZ:     "America/New_York",
				WorkspaceType: "none",
				ChannelChoice: "telegram",
				Telegram: &config.TelegramConfig{
					BotToken: "${TELEGRAM_BOT_TOKEN}",
					ChatID:   456,
				},
				AgentChoice: "default",
				Prompt:      "run audit",
			},
			check: func(t *testing.T, pyl *config.PylonConfig) {
				assert.Equal(t, "cron", pyl.Trigger.Type)
				assert.Equal(t, "0 9 * * 1-5", pyl.Trigger.Cron)
				assert.Equal(t, "America/New_York", pyl.Trigger.Timezone)

				require.NotNil(t, pyl.Channel)
				assert.Equal(t, "telegram", pyl.Channel.Type)
			},
		},
		{
			name: "slack + webhook",
			input: constructInputs{
				Name:          "sl-webhook",
				TriggerType:   "webhook",
				TriggerPath:   "/sl-webhook",
				WorkspaceType: "none",
				ChannelChoice: "slack",
				Slack: &config.SlackConfig{
					BotToken:  "${SLACK_BOT_TOKEN}",
					AppToken:  "${SLACK_APP_TOKEN}",
					ChannelID: "C999",
				},
				AgentChoice: "default",
				Prompt:      "test",
			},
			check: func(t *testing.T, pyl *config.PylonConfig) {
				assert.Equal(t, "webhook", pyl.Trigger.Type)
				assert.Equal(t, "/sl-webhook", pyl.Trigger.Path)

				require.NotNil(t, pyl.Channel)
				assert.Equal(t, "slack", pyl.Channel.Type)
				require.NotNil(t, pyl.Channel.Slack)
				assert.Equal(t, "${SLACK_BOT_TOKEN}", pyl.Channel.Slack.BotToken)
				assert.Equal(t, "C999", pyl.Channel.Slack.ChannelID)
			},
		},
		{
			name: "slack + cron",
			input: constructInputs{
				Name:          "sl-cron",
				TriggerType:   "cron",
				TriggerCron:   "*/5 * * * *",
				TriggerTZ:     "UTC",
				WorkspaceType: "none",
				ChannelChoice: "slack",
				Slack: &config.SlackConfig{
					BotToken:  "${SLACK_BOT_TOKEN}",
					AppToken:  "${SLACK_APP_TOKEN}",
					ChannelID: "C999",
				},
				AgentChoice: "default",
				Prompt:      "check status",
			},
			check: func(t *testing.T, pyl *config.PylonConfig) {
				assert.Equal(t, "cron", pyl.Trigger.Type)
				assert.Equal(t, "*/5 * * * *", pyl.Trigger.Cron)
				assert.Equal(t, "UTC", pyl.Trigger.Timezone)

				require.NotNil(t, pyl.Channel)
				assert.Equal(t, "slack", pyl.Channel.Type)
			},
		},
		{
			name: "default channel",
			input: constructInputs{
				Name:          "default-pylon",
				TriggerType:   "webhook",
				TriggerPath:   "/default-pylon",
				WorkspaceType: "none",
				ChannelChoice: "default",
				AgentChoice:   "default",
				Prompt:        "test",
			},
			check: func(t *testing.T, pyl *config.PylonConfig) {
				// No per-pylon channel override
				assert.Nil(t, pyl.Channel)
			},
		},
		{
			name: "agent override to claude",
			input: constructInputs{
				Name:          "agent-test",
				TriggerType:   "webhook",
				TriggerPath:   "/agent-test",
				WorkspaceType: "none",
				ChannelChoice: "default",
				AgentChoice:   "claude",
				Prompt:        "test",
			},
			check: func(t *testing.T, pyl *config.PylonConfig) {
				require.NotNil(t, pyl.Agent)
				assert.Equal(t, "claude", pyl.Agent.Type)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pyl := buildPylonConfig(tt.input)

			assert.Equal(t, tt.input.Name, pyl.Name)
			assert.Equal(t, tt.input.Description, pyl.Description)
			assert.Equal(t, tt.input.WorkspaceType, pyl.Workspace.Type)

			tt.check(t, pyl)
		})
	}
}

func TestBuildPylonConfig_WebhookPathDefaults(t *testing.T) {
	t.Run("empty path defaults to /name", func(t *testing.T) {
		pyl := buildPylonConfig(constructInputs{
			Name:          "my-pylon",
			TriggerType:   "webhook",
			TriggerPath:   "",
			WorkspaceType: "none",
			ChannelChoice: "default",
			AgentChoice:   "default",
		})
		assert.Equal(t, "/my-pylon", pyl.Trigger.Path)
	})

	t.Run("path without leading slash gets one", func(t *testing.T) {
		pyl := buildPylonConfig(constructInputs{
			Name:          "my-pylon",
			TriggerType:   "webhook",
			TriggerPath:   "webhook",
			WorkspaceType: "none",
			ChannelChoice: "default",
			AgentChoice:   "default",
		})
		assert.Equal(t, "/webhook", pyl.Trigger.Path)
	})

	t.Run("public URL override", func(t *testing.T) {
		pyl := buildPylonConfig(constructInputs{
			Name:          "my-pylon",
			TriggerType:   "webhook",
			TriggerPath:   "/my-pylon",
			TriggerURL:    "https://custom.app/",
			WorkspaceType: "none",
			ChannelChoice: "default",
			AgentChoice:   "default",
		})
		assert.Equal(t, "https://custom.app", pyl.Trigger.PublicURL, "trailing slash should be trimmed")
	})
}

func TestBuildPylonConfig_Workspace(t *testing.T) {
	t.Run("git-clone", func(t *testing.T) {
		pyl := buildPylonConfig(constructInputs{
			Name:          "ws-test",
			TriggerType:   "webhook",
			TriggerPath:   "/ws-test",
			WorkspaceType: "git-clone",
			WorkspaceRepo: "git@github.com:test/repo.git",
			WorkspaceRef:  "develop",
			ChannelChoice: "default",
			AgentChoice:   "default",
		})
		assert.Equal(t, "git-clone", pyl.Workspace.Type)
		assert.Equal(t, "git@github.com:test/repo.git", pyl.Workspace.Repo)
		assert.Equal(t, "develop", pyl.Workspace.Ref)
	})

	t.Run("local", func(t *testing.T) {
		pyl := buildPylonConfig(constructInputs{
			Name:          "ws-test",
			TriggerType:   "webhook",
			TriggerPath:   "/ws-test",
			WorkspaceType: "local",
			WorkspacePath: "/home/user/project",
			ChannelChoice: "default",
			AgentChoice:   "default",
		})
		assert.Equal(t, "local", pyl.Workspace.Type)
		assert.Equal(t, "/home/user/project", pyl.Workspace.Path)
	})
}

func TestBuildPylonConfig_Approval(t *testing.T) {
	pyl := buildPylonConfig(constructInputs{
		Name:          "approval-test",
		TriggerType:   "webhook",
		TriggerPath:   "/approval-test",
		WorkspaceType: "none",
		ChannelChoice: "telegram",
		Telegram: &config.TelegramConfig{
			BotToken: "${TELEGRAM_BOT_TOKEN}",
			ChatID:   789,
		},
		AgentChoice:   "default",
		Prompt:        "test",
		Approval:      true,
		TopicTemplate: "{{ .body.issue.title }}",
		MsgTemplate:   "Alert: {{ .body.error }}",
	})

	require.NotNil(t, pyl.Channel)
	assert.True(t, pyl.Channel.Approval)
	assert.Equal(t, "{{ .body.issue.title }}", pyl.Channel.Topic)
	assert.Equal(t, "Alert: {{ .body.error }}", pyl.Channel.Message)
}

func TestBuildPylonConfig_Volumes(t *testing.T) {
	pyl := buildPylonConfig(constructInputs{
		Name:          "vol-test",
		TriggerType:   "webhook",
		TriggerPath:   "/vol-test",
		WorkspaceType: "none",
		ChannelChoice: "default",
		AgentChoice:   "default",
		Volumes:       []string{"~/.config/gcloud:/home/pylon/.config/gcloud:ro"},
		Prompt:        "test",
	})

	require.NotNil(t, pyl.Agent)
	require.Len(t, pyl.Agent.Volumes, 1)
	assert.Equal(t, "~/.config/gcloud:/home/pylon/.config/gcloud:ro", pyl.Agent.Volumes[0])
}

func TestConstructFromTemplate(t *testing.T) {
	tests := []struct {
		name     string
		template string
		check    func(t *testing.T, pyl *config.PylonConfig)
	}{
		{
			name:     "sentry template",
			template: "sentry",
			check: func(t *testing.T, pyl *config.PylonConfig) {
				assert.Equal(t, "webhook", pyl.Trigger.Type)
				assert.Equal(t, "/sentry-test", pyl.Trigger.Path)
				assert.Equal(t, "git-clone", pyl.Workspace.Type)
				require.NotNil(t, pyl.Channel)
				assert.True(t, pyl.Channel.Approval)
				assert.Contains(t, pyl.Channel.Message, "body.data.event.title")
				require.NotNil(t, pyl.Agent)
				assert.Contains(t, pyl.Agent.Prompt, "Sentry")
				assert.Equal(t, "10m", pyl.Agent.Timeout)
			},
		},
		{
			name:     "github-pr template",
			template: "github-pr",
			check: func(t *testing.T, pyl *config.PylonConfig) {
				assert.Equal(t, "webhook", pyl.Trigger.Type)
				assert.Equal(t, "/github-pr-test", pyl.Trigger.Path)
				assert.Equal(t, "git-clone", pyl.Workspace.Type)
				assert.Contains(t, pyl.Workspace.Repo, "body.repository.clone_url")
				assert.Contains(t, pyl.Workspace.Ref, "body.pull_request.head.ref")
				require.NotNil(t, pyl.Channel)
				assert.False(t, pyl.Channel.Approval)
				require.NotNil(t, pyl.Agent)
				assert.Contains(t, pyl.Agent.Prompt, "pull request")
			},
		},
		{
			name:     "blank template",
			template: "blank",
			check: func(t *testing.T, pyl *config.PylonConfig) {
				assert.Equal(t, "webhook", pyl.Trigger.Type)
				assert.Equal(t, "/blank-test", pyl.Trigger.Path)
				assert.Equal(t, "none", pyl.Workspace.Type)
				require.NotNil(t, pyl.Agent)
				assert.Empty(t, pyl.Agent.Prompt)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())

			pylonName := tt.template + "-test"
			err := constructFromTemplate(pylonName, tt.template)
			require.NoError(t, err)

			pyl, err := config.LoadPylonRaw(pylonName)
			require.NoError(t, err)
			assert.Equal(t, pylonName, pyl.Name)

			tt.check(t, pyl)
		})
	}
}

func TestConstructFromTemplate_unknownTemplate(t *testing.T) {
	err := constructFromTemplate("test", "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown template")
}

func TestPersistChatID(t *testing.T) {
	t.Run("persists to pylon config when pylon has telegram channel", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")

		pyl := &config.PylonConfig{
			Name:    "persist-test",
			Trigger: config.TriggerConfig{Type: "webhook", Path: "/test"},
			Channel: &config.PylonChannel{
				Type: "telegram",
				Telegram: &config.TelegramConfig{
					BotToken: "${TELEGRAM_BOT_TOKEN}",
					ChatID:   0,
				},
			},
		}
		require.NoError(t, config.SavePylon(pyl))

		err := persistChatID("persist-test", pyl, 99999)
		require.NoError(t, err)

		// Reload and verify
		updated, err := config.LoadPylon("persist-test")
		require.NoError(t, err)
		assert.Equal(t, int64(99999), updated.Channel.Telegram.ChatID)
	})

	t.Run("persists to global config when pylon has no channel", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")

		// Save global config with telegram
		global := &config.GlobalConfig{
			Version: 1,
			Server:  config.ServerConfig{Port: 8080, Host: "0.0.0.0"},
			Docker:  config.DockerConfig{MaxConcurrent: 3, DefaultTimeout: "15m"},
		}
		global.Defaults.Channel.Type = "telegram"
		global.Defaults.Channel.Telegram = &config.TelegramConfig{
			BotToken: "${TELEGRAM_BOT_TOKEN}",
			ChatID:   0,
		}
		require.NoError(t, config.SaveGlobal(global))

		pyl := &config.PylonConfig{
			Name:    "no-channel-pylon",
			Trigger: config.TriggerConfig{Type: "webhook", Path: "/test"},
		}

		err := persistChatID("no-channel-pylon", pyl, 88888)
		require.NoError(t, err)

		// Reload global and verify
		updated, err := config.LoadGlobal()
		require.NoError(t, err)
		assert.Equal(t, int64(88888), updated.Defaults.Channel.Telegram.ChatID)
	})
}

func TestCompletePylonNames(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Create some pylons
	for _, name := range []string{"alpha", "beta", "gamma"} {
		pyl := &config.PylonConfig{
			Name:    name,
			Trigger: config.TriggerConfig{Type: "webhook", Path: "/" + name},
		}
		require.NoError(t, config.SavePylon(pyl))
	}

	names, directive := completePylonNames(nil, nil, "")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	assert.Len(t, names, 3)
	assert.Contains(t, names, "alpha")
	assert.Contains(t, names, "beta")
	assert.Contains(t, names, "gamma")
}

func TestCompletePylonNames_alreadyHasArg(t *testing.T) {
	names, directive := completePylonNames(nil, []string{"existing"}, "")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	assert.Nil(t, names)
}
