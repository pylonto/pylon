package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPylonConfigValidate(t *testing.T) {
	// Isolate from real ~/.pylon
	t.Setenv("HOME", t.TempDir())

	t.Run("minimal valid", func(t *testing.T) {
		cfg := &PylonConfig{Name: "test"}
		assert.NoError(t, cfg.Validate(""))
	})

	t.Run("empty name", func(t *testing.T) {
		cfg := &PylonConfig{}
		assert.ErrorContains(t, cfg.Validate(""), "name is required")
	})

	t.Run("invalid trigger type", func(t *testing.T) {
		cfg := &PylonConfig{Name: "test", Trigger: TriggerConfig{Type: "push"}}
		assert.ErrorContains(t, cfg.Validate(""), "unsupported trigger type")
	})

	t.Run("cron without expression", func(t *testing.T) {
		cfg := &PylonConfig{Name: "test", Trigger: TriggerConfig{Type: "cron"}}
		assert.ErrorContains(t, cfg.Validate(""), "cron expression is required")
	})

	t.Run("invalid cron expression", func(t *testing.T) {
		cfg := &PylonConfig{Name: "test", Trigger: TriggerConfig{Type: "cron", Cron: "bad"}}
		assert.ErrorContains(t, cfg.Validate(""), "invalid cron expression")
	})

	t.Run("valid cron", func(t *testing.T) {
		cfg := &PylonConfig{Name: "test", Trigger: TriggerConfig{Type: "cron", Cron: "*/5 * * * *"}}
		assert.NoError(t, cfg.Validate(""))
	})

	t.Run("invalid timezone", func(t *testing.T) {
		cfg := &PylonConfig{Name: "test", Trigger: TriggerConfig{Timezone: "Fake/Zone"}}
		assert.ErrorContains(t, cfg.Validate(""), "invalid timezone")
	})

	t.Run("invalid workspace type", func(t *testing.T) {
		cfg := &PylonConfig{Name: "test", Workspace: WorkspaceConfig{Type: "docker"}}
		assert.ErrorContains(t, cfg.Validate(""), "unsupported workspace type")
	})

	t.Run("valid workspace types", func(t *testing.T) {
		for _, ws := range []string{"git-clone", "git-worktree", "local", "none", ""} {
			cfg := &PylonConfig{Name: "test", Workspace: WorkspaceConfig{Type: ws}}
			assert.NoError(t, cfg.Validate(""), "workspace type %q should be valid", ws)
		}
	})

	t.Run("invalid channel type override", func(t *testing.T) {
		cfg := &PylonConfig{
			Name:    "test",
			Channel: &PylonChannel{Type: "discord"},
		}
		assert.ErrorContains(t, cfg.Validate(""), "unsupported channel type")
	})

	t.Run("invalid agent type", func(t *testing.T) {
		cfg := &PylonConfig{
			Name:  "test",
			Agent: &PylonAgent{Type: "gemini"},
		}
		assert.ErrorContains(t, cfg.Validate(""), "unsupported agent type")
	})

	t.Run("invalid volume", func(t *testing.T) {
		cfg := &PylonConfig{
			Name:  "test",
			Agent: &PylonAgent{Volumes: []string{"bad-volume"}},
		}
		assert.ErrorContains(t, cfg.Validate(""), "invalid volume")
	})

	t.Run("valid volume", func(t *testing.T) {
		cfg := &PylonConfig{
			Name:  "test",
			Agent: &PylonAgent{Volumes: []string{"/host:/container:ro"}},
		}
		assert.NoError(t, cfg.Validate(""))
	})
}

func TestResolvePublicURL(t *testing.T) {
	tests := []struct {
		name      string
		pylonURL  string
		globalURL string
		host      string
		port      int
		path      string
		want      string
	}{
		{
			name:     "pylon override",
			pylonURL: "https://pylon.example.com",
			path:     "/hook",
			want:     "https://pylon.example.com/hook",
		},
		{
			name:      "falls back to global",
			globalURL: "https://global.example.com",
			path:      "/hook",
			want:      "https://global.example.com/hook",
		},
		{
			name: "constructs from host and port",
			host: "0.0.0.0",
			port: 8080,
			path: "/hook",
			want: "http://0.0.0.0:8080/hook",
		},
		{
			name:     "trailing slash stripped",
			pylonURL: "https://example.com/",
			path:     "/hook",
			want:     "https://example.com/hook",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pyl := &PylonConfig{
				Trigger: TriggerConfig{PublicURL: tt.pylonURL, Path: tt.path},
			}
			global := &GlobalConfig{
				Server: ServerConfig{PublicURL: tt.globalURL, Host: tt.host, Port: tt.port},
			}
			assert.Equal(t, tt.want, pyl.ResolvePublicURL(global))
		})
	}
}

func TestResolveTimezone(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	chicago, _ := time.LoadLocation("America/Chicago")

	tests := []struct {
		name     string
		pylonTZ  string
		globalTZ string
		want     *time.Location
	}{
		{"pylon wins", "America/New_York", "America/Chicago", ny},
		{"falls back to global", "", "America/Chicago", chicago},
		{"both empty is UTC", "", "", time.UTC},
		{"invalid pylon falls through", "Fake/Zone", "America/Chicago", chicago},
		{"both invalid is UTC", "Fake/Zone", "Fake/Zone2", time.UTC},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pyl := &PylonConfig{Trigger: TriggerConfig{Timezone: tt.pylonTZ}}
			global := &GlobalConfig{Defaults: DefaultsConfig{Timezone: tt.globalTZ}}
			assert.Equal(t, tt.want, pyl.ResolveTimezone(global))
		})
	}
}

func TestResolveTimeout(t *testing.T) {
	t.Run("pylon agent timeout", func(t *testing.T) {
		pyl := &PylonConfig{Agent: &PylonAgent{Timeout: "30m"}}
		global := &GlobalConfig{Docker: DockerConfig{DefaultTimeout: "15m"}}
		assert.Equal(t, 30*time.Minute, pyl.ResolveTimeout(global))
	})

	t.Run("falls back to global", func(t *testing.T) {
		pyl := &PylonConfig{}
		global := &GlobalConfig{Docker: DockerConfig{DefaultTimeout: "20m"}}
		assert.Equal(t, 20*time.Minute, pyl.ResolveTimeout(global))
	})

	t.Run("no agent section uses global", func(t *testing.T) {
		pyl := &PylonConfig{}
		global := &GlobalConfig{}
		global.applyDefaults()
		assert.Equal(t, 15*time.Minute, pyl.ResolveTimeout(global))
	})

	t.Run("invalid agent timeout falls back to global", func(t *testing.T) {
		pyl := &PylonConfig{Agent: &PylonAgent{Timeout: "not-a-duration"}}
		global := &GlobalConfig{Docker: DockerConfig{DefaultTimeout: "20m"}}
		assert.Equal(t, 20*time.Minute, pyl.ResolveTimeout(global))
	})
}

func TestResolveAgentType(t *testing.T) {
	tests := []struct {
		name       string
		pylonType  string
		globalType string
		want       string
	}{
		{"pylon wins", "opencode", "claude", "opencode"},
		{"falls back to global", "", "opencode", "opencode"},
		{"defaults to claude", "", "", "claude"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var agent *PylonAgent
			if tt.pylonType != "" {
				agent = &PylonAgent{Type: tt.pylonType}
			}
			pyl := &PylonConfig{Agent: agent}
			global := &GlobalConfig{Defaults: DefaultsConfig{Agent: AgentDefaults{Type: tt.globalType}}}
			assert.Equal(t, tt.want, pyl.ResolveAgentType(global))
		})
	}
}

func TestResolveAuth(t *testing.T) {
	t.Run("pylon override", func(t *testing.T) {
		pyl := &PylonConfig{Agent: &PylonAgent{Auth: "api-key"}}
		global := &GlobalConfig{}
		assert.Equal(t, "api-key", pyl.ResolveAuth(global))
	})

	t.Run("global claude default", func(t *testing.T) {
		pyl := &PylonConfig{}
		global := &GlobalConfig{
			Defaults: DefaultsConfig{
				Agent: AgentDefaults{
					Type:   "claude",
					Claude: &ClaudeDefaults{Auth: "api-key"},
				},
			},
		}
		assert.Equal(t, "api-key", pyl.ResolveAuth(global))
	})

	t.Run("claude defaults to oauth", func(t *testing.T) {
		pyl := &PylonConfig{}
		global := &GlobalConfig{Defaults: DefaultsConfig{Agent: AgentDefaults{Type: "claude"}}}
		assert.Equal(t, "oauth", pyl.ResolveAuth(global))
	})

	t.Run("non-claude defaults to api-key", func(t *testing.T) {
		pyl := &PylonConfig{Agent: &PylonAgent{Type: "opencode"}}
		global := &GlobalConfig{}
		assert.Equal(t, "api-key", pyl.ResolveAuth(global))
	})
}

func TestResolveProvider(t *testing.T) {
	t.Run("pylon override", func(t *testing.T) {
		pyl := &PylonConfig{Agent: &PylonAgent{Provider: "openai"}}
		global := &GlobalConfig{}
		assert.Equal(t, "openai", pyl.ResolveProvider(global))
	})

	t.Run("global opencode default", func(t *testing.T) {
		pyl := &PylonConfig{}
		global := &GlobalConfig{
			Defaults: DefaultsConfig{
				Agent: AgentDefaults{
					OpenCode: &OpenCodeDefaults{Provider: "anthropic"},
				},
			},
		}
		assert.Equal(t, "anthropic", pyl.ResolveProvider(global))
	})

	t.Run("empty when nothing set", func(t *testing.T) {
		pyl := &PylonConfig{}
		global := &GlobalConfig{}
		assert.Equal(t, "", pyl.ResolveProvider(global))
	})
}

func TestResolveChannel(t *testing.T) {
	t.Run("pylon override", func(t *testing.T) {
		tg := &TelegramConfig{BotToken: "pylon-tok"}
		pyl := &PylonConfig{Channel: &PylonChannel{Type: "telegram", Telegram: tg}}
		global := &GlobalConfig{Defaults: DefaultsConfig{Channel: ChannelDefaults{Type: "slack"}}}
		typ, gotTG, gotSL := pyl.ResolveChannel(global)
		assert.Equal(t, "telegram", typ)
		assert.Equal(t, tg, gotTG)
		assert.Nil(t, gotSL)
	})

	t.Run("falls back to global", func(t *testing.T) {
		sl := &SlackConfig{BotToken: "global-tok"}
		pyl := &PylonConfig{}
		global := &GlobalConfig{Defaults: DefaultsConfig{Channel: ChannelDefaults{Type: "slack", Slack: sl}}}
		typ, gotTG, gotSL := pyl.ResolveChannel(global)
		assert.Equal(t, "slack", typ)
		assert.Nil(t, gotTG)
		assert.Equal(t, sl, gotSL)
	})
}

func TestResolveAgentImage(t *testing.T) {
	t.Run("custom image from global", func(t *testing.T) {
		pyl := &PylonConfig{}
		global := &GlobalConfig{
			Defaults: DefaultsConfig{
				Agent: AgentDefaults{
					Type:   "claude",
					Claude: &ClaudeDefaults{Image: "custom/image:latest"},
				},
			},
		}
		assert.Equal(t, "custom/image:latest", pyl.ResolveAgentImage(global))
	})

	t.Run("default claude image", func(t *testing.T) {
		pyl := &PylonConfig{}
		global := &GlobalConfig{Defaults: DefaultsConfig{Agent: AgentDefaults{Type: "claude"}}}
		assert.Equal(t, "ghcr.io/pylonto/agent-claude", pyl.ResolveAgentImage(global))
	})

	t.Run("default opencode image", func(t *testing.T) {
		pyl := &PylonConfig{Agent: &PylonAgent{Type: "opencode"}}
		global := &GlobalConfig{}
		assert.Equal(t, "ghcr.io/pylonto/agent-opencode", pyl.ResolveAgentImage(global))
	})

	t.Run("ignores stale claude literal", func(t *testing.T) {
		pyl := &PylonConfig{}
		global := &GlobalConfig{
			Defaults: DefaultsConfig{
				Agent: AgentDefaults{
					Type:   "claude",
					Claude: &ClaudeDefaults{Image: "pylon/agent-claude"},
				},
			},
		}
		assert.Equal(t, "ghcr.io/pylonto/agent-claude", pyl.ResolveAgentImage(global))
	})

	t.Run("ignores stale opencode literal", func(t *testing.T) {
		pyl := &PylonConfig{Agent: &PylonAgent{Type: "opencode"}}
		global := &GlobalConfig{
			Defaults: DefaultsConfig{
				Agent: AgentDefaults{
					OpenCode: &OpenCodeDefaults{Image: "pylon/agent-opencode"},
				},
			},
		}
		assert.Equal(t, "ghcr.io/pylonto/agent-opencode", pyl.ResolveAgentImage(global))
	})
}
