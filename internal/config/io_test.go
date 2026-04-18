package config

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setTempHome overrides HOME to a temp directory and creates the
// necessary subdirectories so config functions resolve correctly.
func setTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".pylon", "pylons"), 0755)
	return home
}

func TestLoadAndSaveGlobal(t *testing.T) {
	setTempHome(t)

	t.Run("round-trip", func(t *testing.T) {
		cfg := &GlobalConfig{
			Version: 1,
			Server:  ServerConfig{Port: 9090, Host: "127.0.0.1", PublicURL: "https://example.com"},
			Defaults: DefaultsConfig{
				Channel: ChannelDefaults{
					Type:     "telegram",
					Telegram: &TelegramConfig{BotToken: "tok123", ChatID: 42},
				},
				Agent: AgentDefaults{Type: "claude"},
			},
			Docker: DockerConfig{MaxConcurrent: 5, DefaultTimeout: "30m"},
		}
		require.NoError(t, SaveGlobal(cfg))

		loaded, err := LoadGlobal()
		require.NoError(t, err)
		assert.Equal(t, cfg.Server.Port, loaded.Server.Port)
		assert.Equal(t, cfg.Server.Host, loaded.Server.Host)
		assert.Equal(t, cfg.Server.PublicURL, loaded.Server.PublicURL)
		assert.Equal(t, cfg.Defaults.Channel.Type, loaded.Defaults.Channel.Type)
		assert.Equal(t, cfg.Defaults.Channel.Telegram.BotToken, loaded.Defaults.Channel.Telegram.BotToken)
		assert.Equal(t, cfg.Docker.MaxConcurrent, loaded.Docker.MaxConcurrent)
	})

	t.Run("missing file returns error", func(t *testing.T) {
		setTempHome(t) // fresh temp home with no config file
		_, err := LoadGlobal()
		assert.Error(t, err)
	})
}

func TestLoadAndSavePylon(t *testing.T) {
	setTempHome(t)

	t.Run("round-trip", func(t *testing.T) {
		cfg := &PylonConfig{
			Name:    "my-pylon",
			Trigger: TriggerConfig{Type: "webhook", Path: "/hook"},
			Workspace: WorkspaceConfig{
				Type: "git-clone",
				Repo: "https://github.com/user/repo",
				Ref:  "main",
			},
			Agent: &PylonAgent{
				Type:   "claude",
				Prompt: "Fix the bug",
			},
		}
		require.NoError(t, SavePylon(cfg))

		loaded, err := LoadPylonRaw("my-pylon")
		require.NoError(t, err)
		assert.Equal(t, "my-pylon", loaded.Name)
		assert.Equal(t, "webhook", loaded.Trigger.Type)
		assert.Equal(t, "/hook", loaded.Trigger.Path)
		assert.Equal(t, "git-clone", loaded.Workspace.Type)
		assert.Equal(t, "Fix the bug", loaded.Agent.Prompt)
	})

	t.Run("missing file returns error", func(t *testing.T) {
		_, err := LoadPylonRaw("nonexistent")
		assert.Error(t, err)
	})
}

func TestListPylons(t *testing.T) {
	setTempHome(t)

	for _, name := range []string{"alpha", "beta", "gamma"} {
		require.NoError(t, SavePylon(&PylonConfig{Name: name}))
	}

	names, err := ListPylons()
	require.NoError(t, err)
	sort.Strings(names)
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, names)
}

func TestDeletePylon(t *testing.T) {
	setTempHome(t)

	require.NoError(t, SavePylon(&PylonConfig{Name: "doomed"}))
	names, _ := ListPylons()
	assert.Contains(t, names, "doomed")

	require.NoError(t, DeletePylon("doomed"))
	names, _ = ListPylons()
	assert.NotContains(t, names, "doomed")
}

func TestLoadEnv(t *testing.T) {
	home := setTempHome(t)

	envContent := "MY_KEY=my_value\n# comment\nANOTHER=val2\n"
	require.NoError(t, os.WriteFile(filepath.Join(home, ".pylon", ".env"), []byte(envContent), 0600))

	// Clear the vars first to ensure LoadEnv sets them
	t.Setenv("MY_KEY", "")
	t.Setenv("ANOTHER", "")

	LoadEnv()
	assert.Equal(t, "my_value", os.Getenv("MY_KEY"))
	assert.Equal(t, "val2", os.Getenv("ANOTHER"))

	t.Run("does not override existing", func(t *testing.T) {
		t.Setenv("MY_KEY", "existing")
		LoadEnv()
		assert.Equal(t, "existing", os.Getenv("MY_KEY"))
	})
}

func TestSaveEnvVar(t *testing.T) {
	setTempHome(t)

	t.Run("creates and appends", func(t *testing.T) {
		require.NoError(t, SaveEnvVar("KEY_A", "val_a"))
		require.NoError(t, SaveEnvVar("KEY_B", "val_b"))

		data, err := os.ReadFile(EnvPath())
		require.NoError(t, err)
		assert.Contains(t, string(data), "KEY_A=val_a")
		assert.Contains(t, string(data), "KEY_B=val_b")
	})

	t.Run("updates existing in place", func(t *testing.T) {
		require.NoError(t, SaveEnvVar("KEY_A", "new_val"))

		data, err := os.ReadFile(EnvPath())
		require.NoError(t, err)
		assert.Contains(t, string(data), "KEY_A=new_val")
		assert.NotContains(t, string(data), "KEY_A=val_a")
	})
}

func TestSavePylonEnvVar(t *testing.T) {
	home := setTempHome(t)

	t.Run("creates pylon dir and file", func(t *testing.T) {
		require.NoError(t, SavePylonEnvVar("my-pylon", "SLACK_BOT_TOKEN", "xoxb-abc"))

		path := filepath.Join(home, ".pylon", "pylons", "my-pylon", ".env")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Contains(t, string(data), "SLACK_BOT_TOKEN=xoxb-abc")
	})

	t.Run("upserts existing key without duplicating", func(t *testing.T) {
		require.NoError(t, SavePylonEnvVar("my-pylon", "SLACK_BOT_TOKEN", "xoxb-new"))
		require.NoError(t, SavePylonEnvVar("my-pylon", "SLACK_APP_TOKEN", "xapp-abc"))

		data, err := os.ReadFile(filepath.Join(home, ".pylon", "pylons", "my-pylon", ".env"))
		require.NoError(t, err)
		assert.Contains(t, string(data), "SLACK_BOT_TOKEN=xoxb-new")
		assert.NotContains(t, string(data), "xoxb-abc")
		assert.Contains(t, string(data), "SLACK_APP_TOKEN=xapp-abc")
	})

	t.Run("does not touch global .env", func(t *testing.T) {
		// Seed global .env with a different token
		require.NoError(t, SaveEnvVar("GLOBAL_TOKEN", "global-val"))
		require.NoError(t, SavePylonEnvVar("other-pylon", "SLACK_BOT_TOKEN", "xoxb-other"))

		globalData, err := os.ReadFile(filepath.Join(home, ".pylon", ".env"))
		require.NoError(t, err)
		assert.Contains(t, string(globalData), "GLOBAL_TOKEN=global-val")
		assert.NotContains(t, string(globalData), "xoxb-other")
	})
}

func TestLoadPylonEnvFile(t *testing.T) {
	home := setTempHome(t)

	t.Run("reads env vars", func(t *testing.T) {
		pylonDir := filepath.Join(home, ".pylon", "pylons", "test-pylon")
		os.MkdirAll(pylonDir, 0755)
		envContent := "SECRET=abc123\n# ignored\nTOKEN=xyz\n\nBAD_LINE\n"
		require.NoError(t, os.WriteFile(filepath.Join(pylonDir, ".env"), []byte(envContent), 0600))

		m := LoadPylonEnvFile("test-pylon")
		assert.Equal(t, "abc123", m["SECRET"])
		assert.Equal(t, "xyz", m["TOKEN"])
		assert.Len(t, m, 2) // BAD_LINE and comment excluded
	})

	t.Run("missing file returns empty map", func(t *testing.T) {
		m := LoadPylonEnvFile("no-such-pylon")
		assert.Empty(t, m)
	})
}

func TestLoadGlobal_MigratesStaleClaudeImage(t *testing.T) {
	home := setTempHome(t)

	raw := "version: 1\n" +
		"defaults:\n" +
		"  channel:\n" +
		"    type: telegram\n" +
		"    telegram:\n" +
		"      bot_token: tok\n" +
		"      chat_id: 1\n" +
		"  agent:\n" +
		"    type: claude\n" +
		"    claude:\n" +
		"      image: pylon/agent-claude\n" +
		"      auth: oauth\n"
	require.NoError(t, os.WriteFile(filepath.Join(home, ".pylon", "config.yaml"), []byte(raw), 0644))

	loaded, err := LoadGlobal()
	require.NoError(t, err)
	assert.Equal(t, "ghcr.io/pylonto/agent-claude", loaded.Defaults.Agent.Claude.Image)

	onDisk, err := os.ReadFile(filepath.Join(home, ".pylon", "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "ghcr.io/pylonto/agent-claude")
	assert.NotContains(t, string(onDisk), "pylon/agent-claude\n")
}

func TestLoadGlobal_MigratesStaleOpenCodeImage(t *testing.T) {
	home := setTempHome(t)

	raw := "version: 1\n" +
		"defaults:\n" +
		"  channel:\n" +
		"    type: telegram\n" +
		"    telegram:\n" +
		"      bot_token: tok\n" +
		"      chat_id: 1\n" +
		"  agent:\n" +
		"    type: opencode\n" +
		"    opencode:\n" +
		"      image: pylon/agent-opencode\n" +
		"      auth: api-key\n" +
		"      provider: anthropic\n"
	require.NoError(t, os.WriteFile(filepath.Join(home, ".pylon", "config.yaml"), []byte(raw), 0644))

	loaded, err := LoadGlobal()
	require.NoError(t, err)
	assert.Equal(t, "ghcr.io/pylonto/agent-opencode", loaded.Defaults.Agent.OpenCode.Image)

	onDisk, err := os.ReadFile(filepath.Join(home, ".pylon", "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "ghcr.io/pylonto/agent-opencode")
	assert.NotContains(t, string(onDisk), "pylon/agent-opencode\n")
}

func TestLoadGlobal_PreservesCustomImage(t *testing.T) {
	home := setTempHome(t)

	raw := "version: 1\n" +
		"defaults:\n" +
		"  channel:\n" +
		"    type: telegram\n" +
		"    telegram:\n" +
		"      bot_token: tok\n" +
		"      chat_id: 1\n" +
		"  agent:\n" +
		"    type: claude\n" +
		"    claude:\n" +
		"      image: my-registry.example.com/claude:v2\n" +
		"      auth: oauth\n"
	path := filepath.Join(home, ".pylon", "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(raw), 0644))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	loaded, err := LoadGlobal()
	require.NoError(t, err)
	assert.Equal(t, "my-registry.example.com/claude:v2", loaded.Defaults.Agent.Claude.Image)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "custom image config should not be rewritten")
}

func TestLoadGlobal_SaveFailureDoesNotBlock(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode permissions")
	}
	home := setTempHome(t)

	raw := "version: 1\n" +
		"defaults:\n" +
		"  channel:\n" +
		"    type: telegram\n" +
		"    telegram:\n" +
		"      bot_token: tok\n" +
		"      chat_id: 1\n" +
		"  agent:\n" +
		"    type: claude\n" +
		"    claude:\n" +
		"      image: pylon/agent-claude\n" +
		"      auth: oauth\n"
	cfgPath := filepath.Join(home, ".pylon", "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(raw), 0644))

	// Make the config file read-only so SaveGlobal's WriteFile fails on O_TRUNC.
	require.NoError(t, os.Chmod(cfgPath, 0400))
	t.Cleanup(func() { _ = os.Chmod(cfgPath, 0644) })

	loaded, err := LoadGlobal()
	require.NoError(t, err)
	assert.Equal(t, "ghcr.io/pylonto/agent-claude", loaded.Defaults.Agent.Claude.Image)

	// On-disk file should still have the stale value because save failed.
	onDisk, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "pylon/agent-claude")
	assert.NotContains(t, string(onDisk), "ghcr.io/pylonto/agent-claude")
}

func TestMigrate_Idempotent(t *testing.T) {
	cfg := &GlobalConfig{
		Defaults: DefaultsConfig{
			Agent: AgentDefaults{
				Claude:   &ClaudeDefaults{Image: "pylon/agent-claude"},
				OpenCode: &OpenCodeDefaults{Image: "pylon/agent-opencode"},
			},
		},
	}
	assert.True(t, cfg.migrate())
	assert.False(t, cfg.migrate())
	assert.Equal(t, "ghcr.io/pylonto/agent-claude", cfg.Defaults.Agent.Claude.Image)
	assert.Equal(t, "ghcr.io/pylonto/agent-opencode", cfg.Defaults.Agent.OpenCode.Image)
}

func TestGlobalExists(t *testing.T) {
	setTempHome(t)

	t.Run("no config", func(t *testing.T) {
		assert.False(t, GlobalExists())
	})

	t.Run("with config", func(t *testing.T) {
		require.NoError(t, SaveGlobal(&GlobalConfig{
			Defaults: DefaultsConfig{
				Channel: ChannelDefaults{Type: "telegram", Telegram: &TelegramConfig{BotToken: "t"}},
			},
		}))
		assert.True(t, GlobalExists())
	})
}
