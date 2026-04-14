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
