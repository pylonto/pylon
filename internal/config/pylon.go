package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pylonto/pylon/internal/agentimage"
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
	Volumes  []string          `yaml:"volumes,omitempty"` // e.g. "~/.config/gcloud:/home/pylon/.config/gcloud:ro"
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

// knownTopLevelKeys lists every valid top-level YAML key in pylon.yaml.
// Anything else is either misplaced (indentation error) or a typo.
var knownTopLevelKeys = map[string]bool{
	"name": true, "description": true, "disabled": true, "created": true,
	"trigger": true, "channel": true, "workspace": true, "agent": true,
}

// misplacedKeyHints maps sub-keys to the section they likely belong under.
var misplacedKeyHints = map[string]string{
	// channel sub-keys
	"telegram": "channel", "slack": "channel",
	"topic": "channel", "message": "channel", "approval": "channel",
	// trigger sub-keys
	"cron": "trigger", "timezone": "trigger", "secret": "trigger",
	"signature_header": "trigger", "public_url": "trigger",
	// agent sub-keys
	"prompt": "agent", "volumes": "agent", "timeout": "agent",
	"auth": "agent", "api_key": "agent", "provider": "agent",
	// workspace sub-keys
	"repo": "workspace", "ref": "workspace",
}

var validTriggerTypes = map[string]bool{
	"webhook": true, "cron": true, "": true,
}

var validWorkspaceTypes = map[string]bool{
	"git-clone": true, "git-worktree": true, "local": true, "none": true, "": true,
}

// blockedVolumeSources lists host paths that must never be mounted into containers.
var blockedVolumeSources = []string{
	"/",
	"/etc",
	"/root",
	"/var/run/docker.sock",
}

// ExpandHome replaces a leading ~ with the user's home directory.
func ExpandHome(path string) string {
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[1:])
	}
	return path
}

// validateVolume checks that a volume string has the form source:target[:ro|rw].
func validateVolume(v string) error {
	parts := strings.SplitN(v, ":", 3)
	if len(parts) < 2 {
		return fmt.Errorf("expected source:target[:ro|rw]")
	}
	source := parts[0]
	if source == "" {
		return fmt.Errorf("source path is empty")
	}
	if !strings.HasPrefix(source, "/") && !strings.HasPrefix(source, "~") {
		return fmt.Errorf("source path must be absolute or start with ~")
	}
	if parts[1] == "" {
		return fmt.Errorf("target path is empty")
	}
	if len(parts) == 3 {
		mode := parts[2]
		if mode != "ro" && mode != "rw" {
			return fmt.Errorf("mode must be ro or rw, got %q", mode)
		}
	}

	// Check against blocklist
	expanded := ExpandHome(source)
	expanded = filepath.Clean(expanded)
	for _, blocked := range blockedVolumeSources {
		if expanded == blocked {
			return fmt.Errorf("mounting %s is not allowed for security reasons", blocked)
		}
	}
	return nil
}

// CheckMisplacedKeys reads the raw YAML at path and reports any top-level
// keys that don't belong there (likely indentation errors). For keys that
// match a known sub-section, the error tells the user which section to nest
// them under. Returns nil when all top-level keys are valid.
func CheckMisplacedKeys(path string) error {
	// Errors here are intentionally ignored -- normal loading surfaces
	// read/parse failures; this function only adds misplaced-key hints.
	data, _ := os.ReadFile(path)
	if len(data) == 0 {
		return nil
	}
	var raw map[string]interface{}
	_ = yaml.Unmarshal(data, &raw)
	if len(raw) == 0 {
		return nil
	}

	var msgs []string
	for key := range raw {
		if knownTopLevelKeys[key] {
			continue
		}
		if parent, ok := misplacedKeyHints[key]; ok {
			msgs = append(msgs, fmt.Sprintf("found top-level %q key -- it should be nested under %q", key, parent))
		} else {
			msgs = append(msgs, fmt.Sprintf("unknown top-level key %q -- check indentation", key))
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	sort.Strings(msgs)
	return fmt.Errorf("%s -- update %s or press e to edit", strings.Join(msgs, "; "), path)
}

// Validate checks the pylon config for invalid values.
// loadedFrom is the file path the config was loaded from (used in error messages).
func (p *PylonConfig) Validate(loadedFrom string) error {
	path := loadedFrom
	if path == "" {
		path = PylonPath(p.Name)
	}
	if err := CheckMisplacedKeys(path); err != nil {
		return err
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
			return fmt.Errorf("invalid cron expression %q: %w -- update %s or press e to edit", p.Trigger.Cron, err, path)
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
		pylonEnv := LoadPylonEnvFile(p.Name)
		if err := validateChannelConfig(p.Channel.Type, p.Channel.Telegram, p.Channel.Slack, path, pylonEnv, PylonEnvPath(p.Name)); err != nil {
			return err
		}
	}
	if p.Agent != nil && p.Agent.Type != "" && !validAgentTypes[p.Agent.Type] {
		return fmt.Errorf("unsupported agent type %q (supported: claude, opencode) -- update %s or press e to edit", p.Agent.Type, path)
	}
	if p.Agent != nil {
		for _, v := range p.Agent.Volumes {
			if err := validateVolume(v); err != nil {
				return fmt.Errorf("invalid volume %q: %w -- update %s or press e to edit", v, err, path)
			}
		}
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
		return agentimage.ImageName("claude")
	case "opencode":
		if global.Defaults.Agent.OpenCode != nil && global.Defaults.Agent.OpenCode.Image != "" {
			return global.Defaults.Agent.OpenCode.Image
		}
		return agentimage.ImageName("opencode")
	default:
		return agentimage.ImageName(p.ResolveAgentType(global))
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
