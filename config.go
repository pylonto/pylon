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
	Trigger   TriggerConfig     `yaml:"trigger"`
	Container ContainerConfig   `yaml:"container"`
	Env       map[string]string `yaml:"env"`
}

type TriggerConfig struct {
	Type string `yaml:"type"`
	Path string `yaml:"path"`
}

type ContainerConfig struct {
	Image   string        `yaml:"image"`
	Command []string      `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
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

	return &cfg, nil
}
