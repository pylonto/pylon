package config

import (
	"bufio"
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
	Tools    []ToolConfig   `yaml:"tools,omitempty"`
}

// ToolConfig defines a host CLI tool available to agent containers via the exec gateway.
type ToolConfig struct {
	Name    string `yaml:"name"`
	Path    string `yaml:"path"`
	Timeout string `yaml:"timeout,omitempty"` // default "30s"
}

// TimeoutDuration returns the parsed timeout, defaulting to 30s.
func (t *ToolConfig) TimeoutDuration() time.Duration {
	if t.Timeout != "" {
		if d, err := time.ParseDuration(t.Timeout); err == nil {
			return d
		}
	}
	return 30 * time.Second
}

// ToolByName returns the ToolConfig for a given tool name, or nil if not found.
func (c *GlobalConfig) ToolByName(name string) *ToolConfig {
	for i := range c.Tools {
		if c.Tools[i].Name == name {
			return &c.Tools[i]
		}
	}
	return nil
}

type ServerConfig struct {
	Port      int    `yaml:"port"`
	Host      string `yaml:"host"`
	PublicURL string `yaml:"public_url,omitempty"` // default base URL for webhook endpoints
}

type DefaultsConfig struct {
	Notifier NotifierDefaults `yaml:"notifier"`
	Agent    AgentDefaults    `yaml:"agent"`
}

type NotifierDefaults struct {
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

// EnvPath returns the path to the secrets env file.
func EnvPath() string {
	return filepath.Join(Dir(), ".env")
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
