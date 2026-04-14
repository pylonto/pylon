package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// GlobalConfig is the machine-level config at ~/.pylon/config.yaml.
type GlobalConfig struct {
	Version  int            `yaml:"version"`
	Server   ServerConfig   `yaml:"server"`
	Defaults DefaultsConfig `yaml:"defaults"`
	Docker   DockerConfig   `yaml:"docker"`
}

type ServerConfig struct {
	Port      int    `yaml:"port"`
	Host      string `yaml:"host"`
	PublicURL string `yaml:"public_url,omitempty"` // default base URL for webhook endpoints
}

type DefaultsConfig struct {
	Channel  ChannelDefaults `yaml:"channel"`
	Agent    AgentDefaults   `yaml:"agent"`
	Timezone string          `yaml:"timezone,omitempty"` // global default IANA timezone for cron triggers
}

type ChannelDefaults struct {
	Type     string          `yaml:"type"`
	Telegram *TelegramConfig `yaml:"telegram,omitempty"`
	Slack    *SlackConfig    `yaml:"slack,omitempty"`
}

type SlackConfig struct {
	BotToken     string   `yaml:"bot_token"`
	AppToken     string   `yaml:"app_token"`
	ChannelID    string   `yaml:"channel_id"`
	AllowedUsers []string `yaml:"allowed_users,omitempty"`
}

type TelegramConfig struct {
	BotToken     string  `yaml:"bot_token"`
	ChatID       int64   `yaml:"chat_id"`
	AllowedUsers []int64 `yaml:"allowed_users,omitempty"`
}

type AgentDefaults struct {
	Type     string            `yaml:"type"`
	Claude   *ClaudeDefaults   `yaml:"claude,omitempty"`
	OpenCode *OpenCodeDefaults `yaml:"opencode,omitempty"`
}

type ClaudeDefaults struct {
	Image     string `yaml:"image"`
	Auth      string `yaml:"auth"`
	OAuthPath string `yaml:"oauth_path,omitempty"`
}

type OpenCodeDefaults struct {
	Image    string `yaml:"image"`
	Auth     string `yaml:"auth"`     // "none" (Zen) or "api-key"
	Provider string `yaml:"provider"` // "anthropic", "openai", "google"
}

type DockerConfig struct {
	MaxConcurrent  int    `yaml:"max_concurrent"`
	DefaultTimeout string `yaml:"default_timeout"`
}

// Dir returns the pylon config directory (~/.pylon).
func Dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pylon")
}

// GlobalPath returns the path to the global config file.
func GlobalPath() string {
	return filepath.Join(Dir(), "config.yaml")
}

// PylonsDir returns the directory containing all pylon configs.
func PylonsDir() string {
	return filepath.Join(Dir(), "pylons")
}

var validChannelTypes = map[string]bool{
	"telegram": true, "slack": true, "webhook": true, "stdout": true, "": true,
}

var validAgentTypes = map[string]bool{
	"claude": true, "opencode": true, "": true,
}

// envUnset returns an error message if value looks like a ${VAR} reference
// that expanded to empty, or "" if the value is fine.
func envUnset(field, value string) string {
	expanded := os.ExpandEnv(value)
	if expanded != "" {
		return ""
	}
	// Extract the variable name from the raw value for a helpful message.
	name := strings.TrimPrefix(value, "${")
	name = strings.TrimSuffix(name, "}")
	return fmt.Sprintf("%s references ${%s} but it is not set -- add %s=<value> to %s",
		field, name, name, EnvPath())
}

// validateChannelConfig checks that the channel type matches its config section
// and that required fields are present.
func validateChannelConfig(typ string, tg *TelegramConfig, sl *SlackConfig, path string) error {
	hint := " -- update " + path + " or press e to edit"
	switch typ {
	case "telegram":
		if sl != nil && tg == nil {
			return fmt.Errorf("channel type is %q but config has a slack section -- replace with a telegram section"+hint, typ)
		}
		if tg == nil {
			return fmt.Errorf("channel type is %q but telegram config is missing. Add:\n\n  telegram:\n    bot_token: ${TELEGRAM_BOT_TOKEN}\n    chat_id: 123456\n\n"+hint, typ)
		}
		if tg.BotToken == "" {
			return errors.New("telegram.bot_token is required" + hint)
		}
		if strings.HasPrefix(tg.BotToken, "${") {
			if msg := envUnset("telegram.bot_token", tg.BotToken); msg != "" {
				return errors.New(msg)
			}
		}
		// chat_id 0 is valid -- means auto-detect on first inbound message.
	case "slack":
		if tg != nil && sl == nil {
			return fmt.Errorf("channel type is %q but config has a telegram section -- replace with a slack section"+hint, typ)
		}
		if sl == nil {
			return fmt.Errorf("channel type is %q but slack config is missing. Add:\n\n  slack:\n    bot_token: ${SLACK_BOT_TOKEN}\n    app_token: ${SLACK_APP_TOKEN}\n    channel_id: C1234567890\n\n"+hint, typ)
		}
		if sl.BotToken == "" {
			return errors.New("slack.bot_token is required" + hint)
		}
		if strings.HasPrefix(sl.BotToken, "${") {
			if msg := envUnset("slack.bot_token", sl.BotToken); msg != "" {
				return errors.New(msg)
			}
		}
		if sl.AppToken == "" {
			return errors.New("slack.app_token is required" + hint)
		}
		if strings.HasPrefix(sl.AppToken, "${") {
			if msg := envUnset("slack.app_token", sl.AppToken); msg != "" {
				return errors.New(msg)
			}
		}
		if sl.ChannelID == "" {
			return errors.New("slack.channel_id is required" + hint)
		}
	}
	return nil
}

// Validate checks the global config for invalid values.
func (c *GlobalConfig) Validate() error {
	path := GlobalPath()
	if !validChannelTypes[c.Defaults.Channel.Type] {
		return fmt.Errorf("unsupported channel type %q (supported: telegram, slack, webhook, stdout) -- update %s", c.Defaults.Channel.Type, path)
	}
	if err := validateChannelConfig(c.Defaults.Channel.Type, c.Defaults.Channel.Telegram, c.Defaults.Channel.Slack, path); err != nil {
		return err
	}
	if !validAgentTypes[c.Defaults.Agent.Type] {
		return fmt.Errorf("unsupported agent type %q (supported: claude, opencode) -- update %s", c.Defaults.Agent.Type, path)
	}
	if c.Defaults.Timezone != "" {
		if _, err := time.LoadLocation(c.Defaults.Timezone); err != nil {
			return fmt.Errorf("invalid default timezone %q -- update %s", c.Defaults.Timezone, path)
		}
	}
	return nil
}

// LoadGlobal loads the global config from ~/.pylon/config.yaml.
func LoadGlobal() (*GlobalConfig, error) {
	data, err := os.ReadFile(GlobalPath())
	if err != nil {
		return nil, fmt.Errorf("reading global config: %w", err)
	}
	var cfg GlobalConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing global config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid global config: %w", err)
	}
	return &cfg, nil
}

func (c *GlobalConfig) applyDefaults() {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8080
	}
	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	if c.Docker.MaxConcurrent == 0 {
		c.Docker.MaxConcurrent = 3
	}
	if c.Docker.DefaultTimeout == "" {
		c.Docker.DefaultTimeout = "15m"
	}
}

// DefaultTimeout parses the default timeout duration.
func (c *GlobalConfig) DefaultTimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.Docker.DefaultTimeout)
	if err != nil {
		return 15 * time.Minute
	}
	return d
}

// SaveGlobal writes the global config to ~/.pylon/config.yaml.
func SaveGlobal(cfg *GlobalConfig) error {
	os.MkdirAll(Dir(), 0755)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	return os.WriteFile(GlobalPath(), data, 0644)
}

// EnvPath returns the path to the global secrets env file.
func EnvPath() string {
	return filepath.Join(Dir(), ".env")
}

// PylonEnvPath returns the path to a per-pylon secrets env file.
func PylonEnvPath(name string) string {
	return filepath.Join(PylonDir(name), ".env")
}

// LoadPylonEnvFile reads ~/.pylon/pylons/<name>/.env into a map.
// Returns an empty map if the file does not exist.
func LoadPylonEnvFile(name string) map[string]string {
	m := make(map[string]string)
	f, err := os.Open(PylonEnvPath(name))
	if err != nil {
		return m
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		m[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return m
}

// ExpandWithPylonEnv expands ${VAR} references in s, checking pylonEnv first,
// then falling back to the process environment.
func ExpandWithPylonEnv(s string, pylonEnv map[string]string) string {
	return os.Expand(s, func(key string) string {
		if v, ok := pylonEnv[key]; ok {
			return v
		}
		return os.Getenv(key)
	})
}

// LoadEnv reads ~/.pylon/.env and sets any variables not already in the environment.
// Format: KEY=VALUE (one per line, # comments, no export prefix).
func LoadEnv() {
	f, err := os.Open(EnvPath())
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		// Don't override existing env vars
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

// SaveEnvVar appends or updates a key=value pair in ~/.pylon/.env.
func SaveEnvVar(key, value string) error {
	os.MkdirAll(Dir(), 0755)
	envPath := EnvPath()

	// Read existing
	existing := make(map[string]string)
	var order []string
	if f, err := os.Open(envPath); err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || line[0] == '#' {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				k := strings.TrimSpace(parts[0])
				existing[k] = strings.TrimSpace(parts[1])
				order = append(order, k)
			}
		}
		f.Close()
	}

	// Update or add
	if _, ok := existing[key]; !ok {
		order = append(order, key)
	}
	existing[key] = value

	// Write back
	var b strings.Builder
	for _, k := range order {
		fmt.Fprintf(&b, "%s=%s\n", k, existing[k])
	}
	return os.WriteFile(envPath, []byte(b.String()), 0600)
}

// GlobalExists returns true if the global config file exists.
func GlobalExists() bool {
	_, err := os.Stat(GlobalPath())
	return err == nil
}
