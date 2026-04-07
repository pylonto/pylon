package main

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level structure matching pylon.yaml.
type Config struct {
	Server    ServerConfig              `yaml:"server"`
	Pipelines map[string]PipelineConfig `yaml:"pipelines"`
}

type ServerConfig struct {
	Port int `yaml:"port"`
}

type PipelineConfig struct {
	Trigger   TriggerConfig   `yaml:"trigger"`
	Workspace WorkspaceConfig `yaml:"workspace"`
	Agent     AgentConfig     `yaml:"agent"`
}

type TriggerConfig struct {
	Type string `yaml:"type"`
	Path string `yaml:"path"`
}

type WorkspaceConfig struct {
	Repo string `yaml:"repo"` // template: "{{ .body.repo }}"
	Ref  string `yaml:"ref"`  // template: "{{ .body.ref }}"
}

type AgentConfig struct {
	Image   string        `yaml:"image"`
	Prompt  string        `yaml:"prompt"` // template: "Investigate {{ .body.error }}"
	Timeout time.Duration `yaml:"timeout"`
	Auth    string        `yaml:"auth"` // "api_key" (default) or "oauth"
}

// LoadConfig reads and parses the YAML config file at the given path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}

	// Default auth mode to api_key if not specified.
	for name, p := range cfg.Pipelines {
		if p.Agent.Auth == "" {
			p.Agent.Auth = "api_key"
			cfg.Pipelines[name] = p
		}
	}

	return &cfg, nil
}
