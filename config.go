package main

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig              `yaml:"server"`
	Telegram  *TelegramConfig           `yaml:"telegram,omitempty"`
	Pipelines map[string]PipelineConfig `yaml:"pipelines"`
}

type ServerConfig struct {
	Port     int    `yaml:"port"`
	Database string `yaml:"database"`
}

type TelegramConfig struct {
	BotToken     string  `yaml:"bot_token"`
	ChatID       int64   `yaml:"chat_id"`
	AllowedUsers []int64 `yaml:"allowed_users"`
}

type PipelineConfig struct {
	Trigger   TriggerConfig   `yaml:"trigger"`
	Notify    *NotifyConfig   `yaml:"notify,omitempty"`
	Workspace WorkspaceConfig `yaml:"workspace"`
	Agent     AgentConfig     `yaml:"agent"`
}

type TriggerConfig struct {
	Type            string `yaml:"type"`
	Path            string `yaml:"path"`
	Secret          string `yaml:"secret"`
	SignatureHeader string `yaml:"signature_header"`
}

type WorkspaceConfig struct {
	Repo string `yaml:"repo"`
	Ref  string `yaml:"ref"`
}

type AgentConfig struct {
	Image     string        `yaml:"image"`
	Prompt    string        `yaml:"prompt"`
	Timeout   time.Duration `yaml:"timeout"`
	Auth      string        `yaml:"auth"`
	MaxAgents int           `yaml:"max_agents"`
}

type NotifyConfig struct {
	Message string        `yaml:"message"`
	Actions ActionsConfig `yaml:"actions"`
}

type ActionsConfig struct {
	Investigate bool `yaml:"investigate"`
	Auto        bool `yaml:"auto"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	data = []byte(os.ExpandEnv(string(data)))

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.Database == "" {
		cfg.Server.Database = "pylon.db"
	}
	for name, p := range cfg.Pipelines {
		if p.Agent.Auth == "" {
			p.Agent.Auth = "api_key"
			cfg.Pipelines[name] = p
		}
	}
	return &cfg, nil
}
