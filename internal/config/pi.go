package config

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pylonto/pylon/internal/store"
)

// PiConfig is an explicit allocation for one isolated subscription role. It has
// no API-key, environment, mount, model-fallback, or publication escape hatch.
type PiConfig struct {
	Image        string                   `yaml:"image" json:"image"`
	AuthDir      string                   `yaml:"auth_dir" json:"auth_dir"`
	AllowedPaths []string                 `yaml:"allowed_paths" json:"allowed_paths"`
	Limits       store.SubscriptionLimits `yaml:"limits" json:"limits"`
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
