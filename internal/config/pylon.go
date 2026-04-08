package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// PylonConfig is the per-pylon config at ~/.pylon/pylons/<name>/pylon.yaml.
type PylonConfig struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description,omitempty"`
	Created     time.Time `yaml:"created"`

	Trigger   TriggerConfig   `yaml:"trigger"`
	Notify    *PylonNotify    `yaml:"notify,omitempty"`
	Workspace WorkspaceConfig `yaml:"workspace"`
	Agent     *PylonAgent     `yaml:"agent,omitempty"`
}

type TriggerConfig struct {
	Type            string `yaml:"type"`
	Path            string `yaml:"path,omitempty"`
	Cron            string `yaml:"cron,omitempty"`
	Secret          string `yaml:"secret,omitempty"`
	SignatureHeader string `yaml:"signature_header,omitempty"`
}

type PylonNotify struct {
	Type     string          `yaml:"type,omitempty"`
	Telegram *TelegramConfig `yaml:"telegram,omitempty"`
	Message  string          `yaml:"message,omitempty"`
	Approval bool            `yaml:"approval,omitempty"`
}

type WorkspaceConfig struct {
	Type string `yaml:"type"`
	Repo string `yaml:"repo,omitempty"`
	Ref  string `yaml:"ref,omitempty"`
	Path string `yaml:"path,omitempty"`
}

type PylonAgent struct {
	Type     string            `yaml:"type,omitempty"`
	Auth     string            `yaml:"auth,omitempty"`
	APIKey   string            `yaml:"api_key,omitempty"` // e.g. "${ANTHROPIC_API_KEY_B}"
	Provider string            `yaml:"provider,omitempty"`
	Env      map[string]string `yaml:"env,omitempty"`
	Prompt   string            `yaml:"prompt"`
	Timeout  string            `yaml:"timeout,omitempty"`
}

// PylonDir returns the directory for a named pylon.
func PylonDir(name string) string {
	return filepath.Join(PylonsDir(), name)
}

// PylonPath returns the config file path for a named pylon.
func PylonPath(name string) string {
	return filepath.Join(PylonDir(name), "pylon.yaml")
}

// PylonDBPath returns the SQLite database path for a named pylon.
func PylonDBPath(name string) string {
	return filepath.Join(PylonDir(name), "jobs.db")
}

// LoadPylon loads a single pylon config by name.
func LoadPylon(name string) (*PylonConfig, error) {
	data, err := os.ReadFile(PylonPath(name))
	if err != nil {
		return nil, fmt.Errorf("reading pylon config %q: %w", name, err)
	}
	var cfg PylonConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing pylon config %q: %w", name, err)
	}
	return &cfg, nil
}

// SavePylon writes a pylon config to its directory.
func SavePylon(cfg *PylonConfig) error {
	dir := PylonDir(cfg.Name)
	os.MkdirAll(dir, 0755)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling pylon config: %w", err)
	}
	return os.WriteFile(PylonPath(cfg.Name), data, 0644)
}

// ListPylons returns the names of all constructed pylons.
func ListPylons() ([]string, error) {
	entries, err := os.ReadDir(PylonsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(PylonPath(e.Name())); err == nil {
				names = append(names, e.Name())
			}
		}
	}
	return names, nil
}

// DeletePylon removes a pylon's directory and all its data.
func DeletePylon(name string) error {
	return os.RemoveAll(PylonDir(name))
}

// ResolveTimeout returns the effective timeout for a pylon, falling back to global default.
func (p *PylonConfig) ResolveTimeout(global *GlobalConfig) time.Duration {
	if p.Agent != nil && p.Agent.Timeout != "" {
		if d, err := time.ParseDuration(p.Agent.Timeout); err == nil {
			return d
		}
	}
	return global.DefaultTimeoutDuration()
}

// ResolveAgentType returns the effective agent type.
func (p *PylonConfig) ResolveAgentType(global *GlobalConfig) string {
	if p.Agent != nil && p.Agent.Type != "" {
		return p.Agent.Type
	}
	if global.Defaults.Agent.Type != "" {
		return global.Defaults.Agent.Type
	}
	return "claude"
}

// ResolveAgentImage returns the effective agent image.
func (p *PylonConfig) ResolveAgentImage(global *GlobalConfig) string {
	switch p.ResolveAgentType(global) {
	case "claude":
		if global.Defaults.Agent.Claude != nil && global.Defaults.Agent.Claude.Image != "" {
			return global.Defaults.Agent.Claude.Image
		}
		return "pylon/agent-claude"
	case "opencode":
		if global.Defaults.Agent.OpenCode != nil && global.Defaults.Agent.OpenCode.Image != "" {
			return global.Defaults.Agent.OpenCode.Image
		}
		return "pylon/agent-opencode"
	default:
		return "pylon/agent-" + p.ResolveAgentType(global)
	}
}

// ResolveAuth returns the effective auth method.
func (p *PylonConfig) ResolveAuth(global *GlobalConfig) string {
	if p.Agent != nil && p.Agent.Auth != "" {
		return p.Agent.Auth
	}
	switch p.ResolveAgentType(global) {
	case "claude":
		if global.Defaults.Agent.Claude != nil && global.Defaults.Agent.Claude.Auth != "" {
			return global.Defaults.Agent.Claude.Auth
		}
		return "oauth"
	default:
		return "api-key"
	}
}

// ResolveProvider returns the effective LLM provider (for multi-provider agents like OpenCode).
func (p *PylonConfig) ResolveProvider(global *GlobalConfig) string {
	if p.Agent != nil && p.Agent.Provider != "" {
		return p.Agent.Provider
	}
	if global.Defaults.Agent.OpenCode != nil && global.Defaults.Agent.OpenCode.Provider != "" {
		return global.Defaults.Agent.OpenCode.Provider
	}
	return "anthropic"
}

// ProviderEnvVar maps a provider name to its API key environment variable.
func ProviderEnvVar(provider string) string {
	switch provider {
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "google":
		return "GOOGLE_API_KEY"
	default:
		return strings.ToUpper(provider) + "_API_KEY"
	}
}
