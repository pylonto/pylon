package config

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/pylonto/pylon/internal/agentimage"
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
// pylonEnv is checked first (may be nil), then the process environment.
// envPath is the file path shown in the error hint.
func envUnset(field, value string, pylonEnv map[string]string, envPath string) string {
	// Extract the variable name from the raw value.
	name := strings.TrimPrefix(value, "${")
	name = strings.TrimSuffix(name, "}")
	// Check per-pylon env first.
	if v, ok := pylonEnv[name]; ok && v != "" {
		return ""
	}
	// Fall back to process environment (includes global .env loaded at startup).
	if os.Getenv(name) != "" {
		return ""
	}
	return fmt.Sprintf("%s references ${%s} but it is not set -- add %s=<value> to %s",
		field, name, name, envPath)
}

// validateChannelConfig checks that the channel type matches its config section
// and that required fields are present. pylonEnv holds per-pylon env vars
// (may be nil for global config validation). envPath is the env file shown in
// error hints for unset variables.
func validateChannelConfig(typ string, tg *TelegramConfig, sl *SlackConfig, path string, pylonEnv map[string]string, envPath string) error {
	hint := " -- update " + path + " or press e to edit"
	switch typ {
	case "telegram":
		if sl != nil && tg == nil {
			return fmt.Errorf("channel type is %q but config has a slack section -- replace with a telegram section"+hint, typ)
		}
		if tg == nil {
			return fmt.Errorf("channel type is %q but channel.telegram config is missing. Add:\n\n  channel:\n    type: telegram\n    telegram:\n      bot_token: ${TELEGRAM_BOT_TOKEN}\n      chat_id: 123456\n\n"+hint, typ)
		}
		if tg.BotToken == "" {
			return errors.New("telegram.bot_token is required" + hint)
		}
		if strings.HasPrefix(tg.BotToken, "${") {
			if msg := envUnset("telegram.bot_token", tg.BotToken, pylonEnv, envPath); msg != "" {
				return errors.New(msg)
			}
		}
		// chat_id 0 is valid -- means auto-detect on first inbound message.
	case "slack":
		if tg != nil && sl == nil {
			return fmt.Errorf("channel type is %q but config has a telegram section -- replace with a slack section"+hint, typ)
		}
		if sl == nil {
			return fmt.Errorf("channel type is %q but channel.slack config is missing. Add:\n\n  channel:\n    type: slack\n    slack:\n      bot_token: ${SLACK_BOT_TOKEN}\n      app_token: ${SLACK_APP_TOKEN}\n      channel_id: C1234567890\n\n"+hint, typ)
		}
		if sl.BotToken == "" {
			return errors.New("slack.bot_token is required" + hint)
		}
		if strings.HasPrefix(sl.BotToken, "${") {
			if msg := envUnset("slack.bot_token", sl.BotToken, pylonEnv, envPath); msg != "" {
				return errors.New(msg)
			}
		}
		if sl.AppToken == "" {
			return errors.New("slack.app_token is required" + hint)
		}
		if strings.HasPrefix(sl.AppToken, "${") {
			if msg := envUnset("slack.app_token", sl.AppToken, pylonEnv, envPath); msg != "" {
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
	if err := validateChannelConfig(c.Defaults.Channel.Type, c.Defaults.Channel.Telegram, c.Defaults.Channel.Slack, path, nil, EnvPath()); err != nil {
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
	if cfg.migrate() {
		if err := SaveGlobal(&cfg); err != nil {
			log.Printf("pylon: migrated stale agent image defaults in memory; could not persist to %s: %v", GlobalPath(), err)
		} else {
			log.Printf("pylon: migrated stale agent image defaults in %s", GlobalPath())
		}
	}
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

// staleAgentImages maps obsolete image literals to their current replacements.
// Populated lazily to avoid an init-time dependency on agentimage.
func staleAgentImages() map[string]string {
	return map[string]string{
		"pylon/agent-claude":   agentimage.ImageName("claude"),
		"pylon/agent-opencode": agentimage.ImageName("opencode"),
	}
}

// isStaleAgentImage reports whether img is a known-obsolete literal default
// that should be treated as unset.
func isStaleAgentImage(img string) bool {
	_, ok := staleAgentImages()[img]
	return ok
}

// migrate rewrites known-obsolete literal values in place. Returns true if
// anything changed, so the caller can persist.
func (c *GlobalConfig) migrate() bool {
	stale := staleAgentImages()
	changed := false
	if c.Defaults.Agent.Claude != nil {
		if repl, ok := stale[c.Defaults.Agent.Claude.Image]; ok {
			c.Defaults.Agent.Claude.Image = repl
			changed = true
		}
	}
	if c.Defaults.Agent.OpenCode != nil {
		if repl, ok := stale[c.Defaults.Agent.OpenCode.Image]; ok {
			c.Defaults.Agent.OpenCode.Image = repl
			changed = true
		}
	}
	return changed
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
	return saveEnvVarAt(EnvPath(), key, value)
}

// SavePylonEnvVar appends or updates a key=value pair in ~/.pylon/pylons/<name>/.env.
// Creates the pylon directory if it doesn't exist yet.
func SavePylonEnvVar(name, key, value string) error {
	return saveEnvVarAt(PylonEnvPath(name), key, value)
}

// SavePylonSecrets writes each k=v pair to the per-pylon .env file so the
// pylon is self-contained. Runtime validation checks per-pylon .env first
// and falls back to the global .env / process env.
func SavePylonSecrets(name string, secrets map[string]string) error {
	for k, v := range secrets {
		if err := SavePylonEnvVar(name, k, v); err != nil {
			return fmt.Errorf("writing %s: %w", k, err)
		}
	}
	return nil
}

func saveEnvVarAt(envPath, key, value string) error {
	os.MkdirAll(filepath.Dir(envPath), 0755)

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
