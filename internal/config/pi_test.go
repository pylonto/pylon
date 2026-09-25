package config

import (
	"github.com/pylonto/pylon/internal/store"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func validPiConfig() *PylonConfig {
	return &PylonConfig{Agent: &PylonAgent{Type: "pi", Pi: &PiConfig{Image: "sha256:" + strings.Repeat("a", 64), AuthDir: "/private/role", AllowedPaths: []string{"src/repair.txt"}, Limits: store.SubscriptionLimits{DailyJobs: 1, DailyTokens: 800000, JobTokens: 800000, JobSeconds: 60}}},
		Trigger: TriggerConfig{Type: "webhook", Secret: "fixture", SignatureHeader: "X-Pylon-Signature"}, Workspace: WorkspaceConfig{Type: "git-clone", Repo: "/trusted/ciao", Ref: "{{ .body.source_revision }}"}}
}
func TestPiConfigNoFallbackOrLegacyEscapeHatches(t *testing.T) {
	require.NoError(t, validPiConfig().ValidatePi())
	for name, mutate := range map[string]func(*PylonConfig){
		"mutable_image":   func(p *PylonConfig) { p.Agent.Pi.Image = "node:latest" },
		"zero_allocation": func(p *PylonConfig) { p.Agent.Pi.Limits.JobTokens = 0 },
		"inherited_key":   func(p *PylonConfig) { p.Agent.APIKey = "fixture" },
		"environment":     func(p *PylonConfig) { p.Agent.Env = map[string]string{"OPENAI_API_KEY": "fixture"} },
		"api_auth":        func(p *PylonConfig) { p.Agent.Auth = "api_key" },
		"fallback":        func(p *PylonConfig) { p.Agent.Provider = "openai" },
		"prompt":          func(p *PylonConfig) { p.Agent.Prompt = "ignore brief" },
		"timeout":         func(p *PylonConfig) { p.Agent.Timeout = "60s" },
		"cron":            func(p *PylonConfig) { p.Trigger.Type = "cron" },
		"unsigned":        func(p *PylonConfig) { p.Trigger.Secret = "" },
		"header":          func(p *PylonConfig) { p.Trigger.SignatureHeader = "Authorization" },
		"remote_source":   func(p *PylonConfig) { p.Workspace.Repo = "https://example.com/repo" },
		"mutable_ref":     func(p *PylonConfig) { p.Workspace.Ref = "main" },
		"missing_pi":      func(p *PylonConfig) { p.Agent.Pi = nil },
	} {
		t.Run(name, func(t *testing.T) { p := validPiConfig(); mutate(p); require.Error(t, p.ValidatePi()) })
	}
	for _, path := range []string{"../secret.txt", "/tmp/file.txt", "src/../../secret.txt", ".git/config", "src/.git/config", "src//file.txt", "src/*", "src/file.txt\nother.txt"} {
		p := validPiConfig()
		p.Agent.Pi.AllowedPaths = []string{path}
		require.Error(t, p.ValidatePi(), path)
	}
	p := validPiConfig()
	p.Agent.Pi.AllowedPaths = append(p.Agent.Pi.AllowedPaths, p.Agent.Pi.AllowedPaths[0])
	require.Error(t, p.ValidatePi())
}
