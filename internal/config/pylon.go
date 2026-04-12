package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pylonto/pylon/internal/cron"
	"gopkg.in/yaml.v3"
)

// PylonConfig is the per-pylon config at ~/.pylon/pylons/<name>/pylon.yaml.
type PylonConfig struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description,omitempty"`
	Disabled    bool      `yaml:"disabled,omitempty"`
	Created     time.Time `yaml:"created"`

	Trigger   TriggerConfig   `yaml:"trigger"`
	Channel   *PylonChannel   `yaml:"channel,omitempty"`
	Workspace WorkspaceConfig `yaml:"workspace"`
	Agent     *PylonAgent     `yaml:"agent,omitempty"`
}

type TriggerConfig struct {
	Type            string `yaml:"type"`
	Path            string `yaml:"path,omitempty"`
	Cron            string `yaml:"cron,omitempty"`
	Timezone        string `yaml:"timezone,omitempty"` // IANA timezone, e.g. "America/New_York"
	Secret          string `yaml:"secret,omitempty"`
	SignatureHeader string `yaml:"signature_header,omitempty"`
	PublicURL       string `yaml:"public_url,omitempty"` // overrides global server.public_url
}

// ResolvePublicURL returns the full public webhook URL for this pylon.
// Falls back to global public_url, then to http://host:port.
func (p *PylonConfig) ResolvePublicURL(global *GlobalConfig) string {
	base := p.Trigger.PublicURL
	if base == "" {
		base = global.Server.PublicURL
	}
	if base == "" {
		base = fmt.Sprintf("http://%s:%d", global.Server.Host, global.Server.Port)
	}
	base = strings.TrimRight(base, "/")
	return base + p.Trigger.Path
}

// ResolveTimezone returns the effective timezone for this pylon's cron schedule.
// Per-pylon timezone takes priority, then global default, then UTC.
func (p *PylonConfig) ResolveTimezone(global *GlobalConfig) *time.Location {
	if p.Trigger.Timezone != "" {
		if loc, err := time.LoadLocation(p.Trigger.Timezone); err == nil {
			return loc
		}
	}
	if global.Defaults.Timezone != "" {
		if loc, err := time.LoadLocation(global.Defaults.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

type PylonChannel struct {
	Type     string          `yaml:"type,omitempty"`
	Telegram *TelegramConfig `yaml:"telegram,omitempty"`
	Slack    *SlackConfig    `yaml:"slack,omitempty"`
	Topic    string          `yaml:"topic,omitempty"`
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
	Tools    []ToolConfig      `yaml:"tools,omitempty"` // per-pylon tool definitions
}

// ResolveTools returns the ToolConfigs available to this pylon.
// Per-pylon tools take priority. If none are defined, falls back to global tools.
// An empty list in both means no tools are available.
func (p *PylonConfig) ResolveTools(global *GlobalConfig) []ToolConfig {
	if p.Agent != nil && len(p.Agent.Tools) > 0 {
		return p.Agent.Tools
	}
	return global.Tools
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

var validTriggerTypes = map[string]bool{
	"webhook": true, "cron": true, "": true,
}

var validWorkspaceTypes = map[string]bool{
	"git-clone": true, "git-worktree": true, "local": true, "none": true, "": true,
}

// Validate checks the pylon config for invalid values.
// loadedFrom is the file path the config was loaded from (used in error messages).
func (p *PylonConfig) Validate(loadedFrom string) error {
	path := loadedFrom
	if path == "" {
		path = PylonPath(p.Name)
	}
	if p.Name == "" {
		return fmt.Errorf("pylon name is required -- update %s or press e to edit", path)
	}
	if !validTriggerTypes[p.Trigger.Type] {
		return fmt.Errorf("unsupported trigger type %q (supported: webhook, cron) -- update %s or press e to edit", p.Trigger.Type, path)
	}
	if p.Trigger.Type == "cron" && p.Trigger.Cron == "" {
		return fmt.Errorf("cron expression is required for trigger type %q -- update %s or press e to edit", p.Trigger.Type, path)
	}
	if p.Trigger.Cron != "" {
		if err := cron.Validate(p.Trigger.Cron); err != nil {
			return fmt.Errorf("invalid cron expression %q: %v -- update %s or press e to edit", p.Trigger.Cron, err, path)
		}
	}
	if p.Trigger.Timezone != "" {
		if _, err := time.LoadLocation(p.Trigger.Timezone); err != nil {
			return fmt.Errorf("invalid timezone %q -- update %s or press e to edit", p.Trigger.Timezone, path)
		}
	}
	if !validWorkspaceTypes[p.Workspace.Type] {
		return fmt.Errorf("unsupported workspace type %q (supported: git-clone, git-worktree, local, none) -- update %s or press e to edit", p.Workspace.Type, path)
	}
	if p.Channel != nil && p.Channel.Type != "" && !validChannelTypes[p.Channel.Type] {
		return fmt.Errorf("unsupported channel type %q (supported: telegram, slack, webhook, stdout) -- update %s or press e to edit", p.Channel.Type, path)
	}
	if p.Channel != nil {
		if err := validateChannelConfig(p.Channel.Type, p.Channel.Telegram, p.Channel.Slack, path); err != nil {
			return err
		}
	}
	if p.Agent != nil && p.Agent.Type != "" && !validAgentTypes[p.Agent.Type] {
		return fmt.Errorf("unsupported agent type %q (supported: claude, opencode) -- update %s or press e to edit", p.Agent.Type, path)
	}
	return nil
}

// LoadPylon loads a single pylon config by name and validates it.
func LoadPylon(name string) (*PylonConfig, error) {
	cfg, err := LoadPylonRaw(name)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(PylonPath(name)); err != nil {
		return nil, fmt.Errorf("pylon %q: %w", name, err)
	}
	return cfg, nil
}

// LoadPylonRaw loads a pylon config without running validation.
// Use this when you need to read or modify the config regardless of its validity.
func LoadPylonRaw(name string) (*PylonConfig, error) {
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
// Returns empty string if no provider is explicitly configured (e.g., using OpenCode Zen).
func (p *PylonConfig) ResolveProvider(global *GlobalConfig) string {
	if p.Agent != nil && p.Agent.Provider != "" {
		return p.Agent.Provider
	}
	if global.Defaults.Agent.OpenCode != nil && global.Defaults.Agent.OpenCode.Provider != "" {
		return global.Defaults.Agent.OpenCode.Provider
	}
	return ""
}

// ResolveChannel returns the effective channel config for this pylon.
// Per-pylon channel takes priority; falls back to global defaults.
func (p *PylonConfig) ResolveChannel(global *GlobalConfig) (string, *TelegramConfig, *SlackConfig) {
	if p.Channel != nil && p.Channel.Type != "" {
		return p.Channel.Type, p.Channel.Telegram, p.Channel.Slack
	}
	return global.Defaults.Channel.Type, global.Defaults.Channel.Telegram, global.Defaults.Channel.Slack
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
