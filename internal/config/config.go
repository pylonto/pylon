package config

import (
	"fmt"
	"os"
	"path/filepath"
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
	Port int    `yaml:"port"`
	Host string `yaml:"host"`
}

type DefaultsConfig struct {
	Notifier NotifierDefaults `yaml:"notifier"`
	Agent    AgentDefaults    `yaml:"agent"`
}

type NotifierDefaults struct {
	Type     string          `yaml:"type"`
	Telegram *TelegramConfig `yaml:"telegram,omitempty"`
}

type TelegramConfig struct {
	BotToken     string  `yaml:"bot_token"`
	ChatID       int64   `yaml:"chat_id"`
	AllowedUsers []int64 `yaml:"allowed_users,omitempty"`
}

type AgentDefaults struct {
	Type  string       `yaml:"type"`
	Claude *ClaudeDefaults `yaml:"claude,omitempty"`
}

type ClaudeDefaults struct {
	Image     string `yaml:"image"`
	Auth      string `yaml:"auth"`
	OAuthPath string `yaml:"oauth_path,omitempty"`
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
	data = []byte(os.ExpandEnv(string(data)))

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

// GlobalExists returns true if the global config file exists.
func GlobalExists() bool {
	_, err := os.Stat(GlobalPath())
	return err == nil
}
