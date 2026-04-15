package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyDefaults(t *testing.T) {
	t.Run("zero value config gets defaults", func(t *testing.T) {
		cfg := &GlobalConfig{}
		cfg.applyDefaults()
		assert.Equal(t, 1, cfg.Version)
		assert.Equal(t, 8080, cfg.Server.Port)
		assert.Equal(t, "0.0.0.0", cfg.Server.Host)
		assert.Equal(t, 3, cfg.Docker.MaxConcurrent)
		assert.Equal(t, "15m", cfg.Docker.DefaultTimeout)
	})

	t.Run("preset values preserved", func(t *testing.T) {
		cfg := &GlobalConfig{
			Version: 2,
			Server:  ServerConfig{Port: 9090, Host: "127.0.0.1"},
			Docker:  DockerConfig{MaxConcurrent: 5, DefaultTimeout: "30m"},
		}
		cfg.applyDefaults()
		assert.Equal(t, 2, cfg.Version)
		assert.Equal(t, 9090, cfg.Server.Port)
		assert.Equal(t, "127.0.0.1", cfg.Server.Host)
		assert.Equal(t, 5, cfg.Docker.MaxConcurrent)
		assert.Equal(t, "30m", cfg.Docker.DefaultTimeout)
	})
}

func TestDefaultTimeoutDuration(t *testing.T) {
	tests := []struct {
		name    string
		timeout string
		want    time.Duration
	}{
		{"30 minutes", "30m", 30 * time.Minute},
		{"1 hour", "1h", time.Hour},
		{"empty fallback", "", 15 * time.Minute},
		{"invalid fallback", "garbage", 15 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &GlobalConfig{Docker: DockerConfig{DefaultTimeout: tt.timeout}}
			assert.Equal(t, tt.want, cfg.DefaultTimeoutDuration())
		})
	}
}

func TestValidateChannelConfig(t *testing.T) {
	path := "/fake/config.yaml"
	envPath := "/fake/.env"

	t.Run("telegram valid", func(t *testing.T) {
		err := validateChannelConfig("telegram", &TelegramConfig{BotToken: "tok"}, nil, path, nil, envPath)
		assert.NoError(t, err)
	})

	t.Run("telegram type with slack config", func(t *testing.T) {
		err := validateChannelConfig("telegram", nil, &SlackConfig{}, path, nil, envPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "slack section")
	})

	t.Run("telegram missing config", func(t *testing.T) {
		err := validateChannelConfig("telegram", nil, nil, path, nil, envPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "channel.telegram config is missing")
	})

	t.Run("telegram missing bot token", func(t *testing.T) {
		err := validateChannelConfig("telegram", &TelegramConfig{}, nil, path, nil, envPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "bot_token is required")
	})

	t.Run("telegram env var resolved from pylonEnv", func(t *testing.T) {
		pylonEnv := map[string]string{"MY_BOT_TOKEN": "secret"}
		err := validateChannelConfig("telegram", &TelegramConfig{BotToken: "${MY_BOT_TOKEN}"}, nil, path, pylonEnv, envPath)
		assert.NoError(t, err)
	})

	t.Run("telegram env var unset", func(t *testing.T) {
		err := validateChannelConfig("telegram", &TelegramConfig{BotToken: "${MISSING_TOKEN}"}, nil, path, nil, envPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "MISSING_TOKEN")
	})

	t.Run("slack valid", func(t *testing.T) {
		sl := &SlackConfig{BotToken: "xoxb-1", AppToken: "xapp-1", ChannelID: "C123"}
		err := validateChannelConfig("slack", nil, sl, path, nil, envPath)
		assert.NoError(t, err)
	})

	t.Run("slack type with telegram config", func(t *testing.T) {
		err := validateChannelConfig("slack", &TelegramConfig{}, nil, path, nil, envPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "telegram section")
	})

	t.Run("slack missing config", func(t *testing.T) {
		err := validateChannelConfig("slack", nil, nil, path, nil, envPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "channel.slack config is missing")
	})

	t.Run("slack missing bot token", func(t *testing.T) {
		err := validateChannelConfig("slack", nil, &SlackConfig{AppToken: "x", ChannelID: "C1"}, path, nil, envPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "bot_token is required")
	})

	t.Run("slack missing app token", func(t *testing.T) {
		err := validateChannelConfig("slack", nil, &SlackConfig{BotToken: "x", ChannelID: "C1"}, path, nil, envPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "app_token is required")
	})

	t.Run("slack missing channel id", func(t *testing.T) {
		err := validateChannelConfig("slack", nil, &SlackConfig{BotToken: "x", AppToken: "y"}, path, nil, envPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "channel_id is required")
	})

	t.Run("empty type passes", func(t *testing.T) {
		err := validateChannelConfig("", nil, nil, path, nil, envPath)
		assert.NoError(t, err)
	})
}

func TestGlobalConfigValidate(t *testing.T) {
	// Use a temp HOME so GlobalPath() / EnvPath() resolve to real paths
	t.Setenv("HOME", t.TempDir())

	t.Run("valid telegram", func(t *testing.T) {
		cfg := &GlobalConfig{
			Defaults: DefaultsConfig{
				Channel: ChannelDefaults{
					Type:     "telegram",
					Telegram: &TelegramConfig{BotToken: "tok"},
				},
			},
		}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("valid slack", func(t *testing.T) {
		cfg := &GlobalConfig{
			Defaults: DefaultsConfig{
				Channel: ChannelDefaults{
					Type:  "slack",
					Slack: &SlackConfig{BotToken: "x", AppToken: "y", ChannelID: "C1"},
				},
			},
		}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("unsupported channel type", func(t *testing.T) {
		cfg := &GlobalConfig{
			Defaults: DefaultsConfig{Channel: ChannelDefaults{Type: "discord"}},
		}
		assert.ErrorContains(t, cfg.Validate(), "unsupported channel type")
	})

	t.Run("unsupported agent type", func(t *testing.T) {
		cfg := &GlobalConfig{
			Defaults: DefaultsConfig{Agent: AgentDefaults{Type: "gemini"}},
		}
		assert.ErrorContains(t, cfg.Validate(), "unsupported agent type")
	})

	t.Run("invalid timezone", func(t *testing.T) {
		cfg := &GlobalConfig{
			Defaults: DefaultsConfig{Timezone: "Fake/Zone"},
		}
		assert.ErrorContains(t, cfg.Validate(), "invalid default timezone")
	})

	t.Run("valid timezone", func(t *testing.T) {
		cfg := &GlobalConfig{
			Defaults: DefaultsConfig{Timezone: "America/New_York"},
		}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("empty config valid", func(t *testing.T) {
		cfg := &GlobalConfig{}
		assert.NoError(t, cfg.Validate())
	})
}

func TestValidateVolume(t *testing.T) {
	tests := []struct {
		name    string
		volume  string
		wantErr string
	}{
		{"valid absolute", "/host:/container", ""},
		{"valid with ro", "/host:/container:ro", ""},
		{"valid with rw", "/host:/container:rw", ""},
		{"valid tilde", "~/config:/config:ro", ""},
		{"relative source", "relative:/path", "absolute or start with ~"},
		{"empty source", ":/path", "source path is empty"},
		{"empty target", "/path:", "target path is empty"},
		{"invalid mode", "/host:/container:xx", "ro or rw"},
		{"no colon", "/just-a-path", "source:target"},
		{"blocked root", "/:/container", "not allowed"},
		{"blocked etc", "/etc:/container", "not allowed"},
		{"blocked docker sock", "/var/run/docker.sock:/docker.sock", "not allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVolume(tt.volume)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"tilde slash", "~/foo", filepath.Join(home, "foo")},
		{"tilde only", "~", home},
		{"absolute", "/absolute/path", "/absolute/path"},
		{"relative", "relative", "relative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExpandHome(tt.in))
		})
	}
}

func TestExpandWithPylonEnv(t *testing.T) {
	t.Run("pylonEnv takes priority", func(t *testing.T) {
		t.Setenv("TESTVAR", "from-os")
		pylonEnv := map[string]string{"TESTVAR": "from-pylon"}
		assert.Equal(t, "from-pylon", ExpandWithPylonEnv("${TESTVAR}", pylonEnv))
	})

	t.Run("falls back to os env", func(t *testing.T) {
		t.Setenv("TESTVAR2", "from-os")
		assert.Equal(t, "from-os", ExpandWithPylonEnv("${TESTVAR2}", nil))
	})

	t.Run("no vars unchanged", func(t *testing.T) {
		assert.Equal(t, "literal string", ExpandWithPylonEnv("literal string", nil))
	})
}

func TestCheckMisplacedKeys(t *testing.T) {
	dir := t.TempDir()

	writeYAML := func(t *testing.T, content string) string {
		t.Helper()
		f, err := os.CreateTemp(dir, "*.yaml")
		require.NoError(t, err)
		_, err = f.WriteString(content)
		require.NoError(t, err)
		require.NoError(t, f.Close())
		return f.Name()
	}

	t.Run("telegram at top level", func(t *testing.T) {
		path := writeYAML(t, `
name: test
channel:
  type: telegram
telegram:
  bot_token: tok
`)
		err := CheckMisplacedKeys(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `found top-level "telegram" key`)
		assert.Contains(t, err.Error(), `"channel"`)
	})

	t.Run("prompt at top level", func(t *testing.T) {
		path := writeYAML(t, `
name: test
prompt: do stuff
`)
		err := CheckMisplacedKeys(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `found top-level "prompt" key`)
		assert.Contains(t, err.Error(), `"agent"`)
	})

	t.Run("cron at top level", func(t *testing.T) {
		path := writeYAML(t, `
name: test
cron: "0 3 * * *"
`)
		err := CheckMisplacedKeys(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `found top-level "cron" key`)
		assert.Contains(t, err.Error(), `"trigger"`)
	})

	t.Run("repo at top level", func(t *testing.T) {
		path := writeYAML(t, `
name: test
repo: git@github.com:foo/bar.git
`)
		err := CheckMisplacedKeys(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `found top-level "repo" key`)
		assert.Contains(t, err.Error(), `"workspace"`)
	})

	t.Run("unknown key without hint", func(t *testing.T) {
		path := writeYAML(t, `
name: test
foobar: baz
`)
		err := CheckMisplacedKeys(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown top-level key "foobar"`)
		assert.Contains(t, err.Error(), "check indentation")
	})

	t.Run("multiple misplaced keys", func(t *testing.T) {
		path := writeYAML(t, `
name: test
telegram:
  bot_token: tok
prompt: do stuff
`)
		err := CheckMisplacedKeys(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "telegram")
		assert.Contains(t, err.Error(), "prompt")
	})

	t.Run("no misplaced keys", func(t *testing.T) {
		path := writeYAML(t, `
name: test
channel:
  type: telegram
  telegram:
    bot_token: tok
`)
		err := CheckMisplacedKeys(path)
		assert.NoError(t, err)
	})

	t.Run("file not found", func(t *testing.T) {
		err := CheckMisplacedKeys("/nonexistent/path.yaml")
		assert.NoError(t, err) // silently ignored, normal loading surfaces the error
	})
}

func TestProviderEnvVar(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"openai", "OPENAI_API_KEY"},
		{"anthropic", "ANTHROPIC_API_KEY"},
		{"google", "GOOGLE_API_KEY"},
		{"custom", "CUSTOM_API_KEY"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			assert.Equal(t, tt.want, ProviderEnvVar(tt.provider))
		})
	}
}
