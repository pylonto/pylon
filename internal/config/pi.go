package config

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pylonto/pylon/internal/pidebug"
	"github.com/pylonto/pylon/internal/store"
	"gopkg.in/yaml.v3"
)

// PiConfig is an explicit allocation for one isolated subscription role. It has
// no API-key, environment, mount, model-fallback, or publication escape hatch.
type PiConfig struct {
	Thinking     PiThinking               `yaml:"thinking,omitempty" json:"thinking,omitempty"`
	Image        string                   `yaml:"image" json:"image"`
	AuthDir      string                   `yaml:"auth_dir" json:"auth_dir"`
	DebugDir     string                   `yaml:"debug_dir,omitempty" json:"debug_dir,omitempty"`
	AllowedPaths []string                 `yaml:"allowed_paths" json:"allowed_paths"`
	Limits       store.SubscriptionLimits `yaml:"limits" json:"limits"`
}

// PiThinking is the operator's per-job selection, never a brief or global default.
type PiThinking string

const (
	PiThinkingMax    PiThinking = "max"
	PiThinkingMedium PiThinking = "medium"
)

func (t PiThinking) Valid() bool { return t == PiThinkingMax || t == PiThinkingMedium }

// The zero value preserves existing Go configurations. On the wire it is always
// resolved; neither the runtime nor a receipt may omit or invent the selection.
func (c PiConfig) WorkerThinking() PiThinking {
	if c.Thinking == "" {
		return PiThinkingMax
	}
	return c.Thinking
}

func (c *PiConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain PiConfig
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	// Resolve YAML merges too: an explicit null/empty is not an absent setting.
	var fields map[string]yaml.Node
	if err := node.Decode(&fields); err != nil {
		return err
	}
	if thinking, present := fields["thinking"]; present && (thinking.Tag != "!!str" || !decoded.Thinking.Valid()) {
		return errors.New("pi_thinking_invalid")
	}
	*c = PiConfig(decoded)
	c.Thinking = c.WorkerThinking()
	return nil
}

var immutableImage = regexp.MustCompile(`^(?:sha256:|[a-zA-Z0-9./_-]+@sha256:)[a-f0-9]{64}$`)
var piPath = regexp.MustCompile(`^[A-Za-z0-9_/-]+\.[A-Za-z0-9]+$`)

// ValidatePi checks the transport/configuration boundary. Ciao's prepared_patch
// remains the authoritative scope and mode policy; this is not patch acceptance.
func (p *PylonConfig) ValidatePi() error {
	if p.Agent == nil || p.Agent.Type != "pi" || p.Agent.Pi == nil {
		return errors.New("pi_explicit_configuration_required")
	}
	a, c := p.Agent, p.Agent.Pi
	if err := c.Validate(); err != nil {
		return err
	}
	if a.Auth != "" && a.Auth != "oauth" || a.Provider != "" && a.Provider != "openai-codex" ||
		a.APIKey != "" || len(a.Env) != 0 || len(a.Volumes) != 0 || a.Timeout != "" || a.Prompt != "" {
		return errors.New("pi_requires_isolated_oauth_and_signed_brief_only")
	}
	if p.Trigger.Type != "webhook" || p.Trigger.Secret == "" || p.Trigger.SignatureHeader != "X-Pylon-Signature" ||
		p.Workspace.Type != "git-clone" || !filepath.IsAbs(p.Workspace.Repo) || p.Workspace.Ref != "{{ .body.source_revision }}" || p.Workspace.Path != "" ||
		p.Channel != nil || p.Control != nil {
		return errors.New("pi_requires_signed_delivery_and_fixed_local_trusted_repository")
	}
	return nil
}

func (c PiConfig) Validate() error {
	if !c.WorkerThinking().Valid() {
		return errors.New("pi_thinking_invalid")
	}
	if c.DebugDir != "" && pidebug.CheckLocation(c.DebugDir, c.AuthDir) != nil {
		return errors.New("pi_debug_unsafe_destination")
	}
	if !immutableImage.MatchString(c.Image) || !filepath.IsAbs(c.AuthDir) || filepath.Clean(c.AuthDir) != c.AuthDir ||
		c.Limits.Validate() != nil || c.Limits.JobSeconds < 30 || len(c.AllowedPaths) == 0 || len(c.AllowedPaths) > 32 {
		return errors.New("pi_image_role_or_allocation_invalid")
	}
	seen := make(map[string]bool)
	for _, path := range c.AllowedPaths {
		if !piPath.MatchString(path) || strings.HasPrefix(path, "/") || strings.Contains(path, "//") ||
			strings.Contains(path, "/.") || filepath.Clean(path) != path || seen[path] {
			return errors.New("pi_explicit_text_paths_required")
		}
		seen[path] = true
	}
	return nil
}
