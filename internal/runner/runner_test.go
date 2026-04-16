package runner

import (
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/mount"
	"github.com/pylonto/pylon/internal/channel"
	"github.com/stretchr/testify/assert"
)

func TestToSSHURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"github https", "https://github.com/user/repo", "git@github.com:user/repo.git"},
		{"github https with .git", "https://github.com/user/repo.git", "git@github.com:user/repo.git"},
		{"gitlab https", "https://gitlab.com/user/repo", "git@gitlab.com:user/repo.git"},
		{"already ssh", "git@github.com:user/repo.git", "git@github.com:user/repo.git"},
		{"bitbucket unchanged", "https://bitbucket.org/user/repo", "https://bitbucket.org/user/repo"},
		{"template unchanged", "https://github.com/{{ .body.org }}/repo", "https://github.com/{{ .body.org }}/repo"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ToSSHURL(tt.in))
		})
	}
}

func TestResolveTemplate(t *testing.T) {
	tests := []struct {
		name string
		tmpl string
		body map[string]interface{}
		want string
	}{
		{
			name: "simple substitution",
			tmpl: "Hello {{ .body.name }}",
			body: map[string]interface{}{"name": "world"},
			want: "Hello world",
		},
		{
			name: "nested key",
			tmpl: "{{ .body.issue.title }}",
			body: map[string]interface{}{
				"issue": map[string]interface{}{"title": "bug fix"},
			},
			want: "bug fix",
		},
		{
			name: "no placeholders",
			tmpl: "static text",
			body: map[string]interface{}{"key": "val"},
			want: "static text",
		},
		{
			name: "nil body",
			tmpl: "{{ .body.name }}",
			body: nil,
			want: "{{ .body.name }}",
		},
		{
			name: "non-string value marshaled",
			tmpl: "count={{ .body.count }}",
			body: map[string]interface{}{"count": float64(42)},
			want: "count=42",
		},
		{
			name: "multiple placeholders",
			tmpl: "{{ .body.repo }}@{{ .body.ref }}",
			body: map[string]interface{}{"repo": "myrepo", "ref": "main"},
			want: "myrepo@main",
		},
		{
			name: "empty template",
			tmpl: "",
			body: map[string]interface{}{"key": "val"},
			want: "",
		},
		{
			name: "missing nested key preserved as placeholder",
			tmpl: "{{ .body.missing.field }}",
			body: map[string]interface{}{"other": "val"},
			want: "{{ .body.missing.field }}",
		},
		{
			name: "non-map value in path preserved",
			tmpl: "{{ .body.name.nested }}",
			body: map[string]interface{}{"name": "not-a-map"},
			want: "{{ .body.name.nested }}",
		},
		{
			name: "boolean value marshaled",
			tmpl: "active={{ .body.active }}",
			body: map[string]interface{}{"active": true},
			want: "active=true",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ResolveTemplate(tt.tmpl, tt.body))
		})
	}
}

func TestFlattenMap(t *testing.T) {
	t.Run("nested maps", func(t *testing.T) {
		m := map[string]interface{}{
			"issue": map[string]interface{}{
				"title":  "bug",
				"labels": map[string]interface{}{"priority": "high"},
			},
			"repo": "myrepo",
		}
		out := make(map[string]interface{})
		flattenMap("", m, out)

		assert.Equal(t, "bug", out["issue.title"])
		assert.Equal(t, "high", out["issue.labels.priority"])
		assert.Equal(t, "myrepo", out["repo"])
		// Top-level nested maps also preserved
		assert.NotNil(t, out["issue"])
		assert.NotNil(t, out["issue.labels"])
	})

	t.Run("empty map", func(t *testing.T) {
		out := make(map[string]interface{})
		flattenMap("", map[string]interface{}{}, out)
		assert.Empty(t, out)
	})
}

func TestWorkDir(t *testing.T) {
	assert.Equal(t, filepath.Join(JobsDir, "abc-123"), WorkDir("abc-123"))
}

func TestLogPath(t *testing.T) {
	assert.Equal(t, filepath.Join(JobsDir, "abc-123.log"), LogPath("abc-123"))
}

func TestRepoHash(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		h1 := repoHash("git@github.com:user/repo.git")
		h2 := repoHash("git@github.com:user/repo.git")
		assert.Equal(t, h1, h2)
	})

	t.Run("different inputs differ", func(t *testing.T) {
		h1 := repoHash("git@github.com:user/repo1.git")
		h2 := repoHash("git@github.com:user/repo2.git")
		assert.NotEqual(t, h1, h2)
	})

	t.Run("small changes produce different hashes", func(t *testing.T) {
		h1 := repoHash("repo")
		h2 := repoHash("repx") // 1 char difference
		assert.NotEqual(t, h1, h2)
	})

	t.Run("valid hex of expected length", func(t *testing.T) {
		h := repoHash("test")
		assert.Len(t, h, 16) // 8 bytes = 16 hex chars
		// Verify it's actually valid hex
		_, err := hex.DecodeString(h)
		assert.NoError(t, err, "hash should be valid hex")
	})

	t.Run("not a trivial transformation", func(t *testing.T) {
		h := repoHash("test")
		assert.NotContains(t, h, "test", "hash should not contain the input")
	})
}

func TestBuildWorkspaceMounts(t *testing.T) {
	workDir := "/tmp/pylon-test-workspace"
	repo := "https://github.com/user/repo"
	expectedBareDir := filepath.Join(ReposDir, repoHash(ToSSHURL(repo)))

	tests := []struct {
		name      string
		params    RunParams
		wantCount int
		checkBare bool // expect bare repo mount
		checkVols []mount.Mount
	}{
		{
			name: "git-worktree with repo adds bare repo mount",
			params: RunParams{
				WorkspaceType: "git-worktree",
				Repo:          repo,
			},
			wantCount: 2,
			checkBare: true,
		},
		{
			name: "git-clone does not add bare repo mount",
			params: RunParams{
				WorkspaceType: "git-clone",
				Repo:          repo,
			},
			wantCount: 1,
		},
		{
			name: "git-worktree without repo skips bare repo mount",
			params: RunParams{
				WorkspaceType: "git-worktree",
				Repo:          "",
			},
			wantCount: 1,
		},
		{
			name: "none workspace",
			params: RunParams{
				WorkspaceType: "none",
			},
			wantCount: 1,
		},
		{
			name: "empty workspace type",
			params: RunParams{
				WorkspaceType: "",
				Repo:          repo,
			},
			wantCount: 1,
		},
		{
			name: "worktree with user volumes",
			params: RunParams{
				WorkspaceType: "git-worktree",
				Repo:          repo,
				Volumes:       []string{"/usr/bin/gh:/usr/local/bin/gh:ro", "/data:/data:rw"},
			},
			wantCount: 4,
			checkBare: true,
			checkVols: []mount.Mount{
				{Type: mount.TypeBind, Source: "/usr/bin/gh", Target: "/usr/local/bin/gh", ReadOnly: true},
				{Type: mount.TypeBind, Source: "/data", Target: "/data", ReadOnly: false},
			},
		},
		{
			name: "clone with user volumes",
			params: RunParams{
				WorkspaceType: "git-clone",
				Volumes:       []string{"/usr/bin/gh:/usr/local/bin/gh:ro"},
			},
			wantCount: 2,
			checkVols: []mount.Mount{
				{Type: mount.TypeBind, Source: "/usr/bin/gh", Target: "/usr/local/bin/gh", ReadOnly: true},
			},
		},
		{
			name: "malformed volume is skipped",
			params: RunParams{
				WorkspaceType: "git-clone",
				Volumes:       []string{"no-colon-here"},
			},
			wantCount: 1,
		},
		{
			name: "volume defaults to read-only",
			params: RunParams{
				WorkspaceType: "git-clone",
				Volumes:       []string{"/a:/b"},
			},
			wantCount: 2,
			checkVols: []mount.Mount{
				{Type: mount.TypeBind, Source: "/a", Target: "/b", ReadOnly: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mounts := BuildWorkspaceMounts(tt.params, workDir)
			assert.Len(t, mounts, tt.wantCount)

			// First mount is always the workspace
			assert.Equal(t, workDir, mounts[0].Source)
			assert.Equal(t, "/workspace", mounts[0].Target)
			assert.Equal(t, mount.TypeBind, mounts[0].Type)

			if tt.checkBare {
				// Second mount should be the bare repo, read-only, same-path
				bare := mounts[1]
				assert.Equal(t, expectedBareDir, bare.Source)
				assert.Equal(t, bare.Source, bare.Target, "bare repo must be mounted at same host path")
				assert.True(t, bare.ReadOnly, "bare repo mount must be read-only")
				assert.Equal(t, mount.TypeBind, bare.Type)
			}

			if tt.checkVols != nil {
				// User volumes come after workspace (and optionally bare repo)
				offset := tt.wantCount - len(tt.checkVols)
				for i, want := range tt.checkVols {
					got := mounts[offset+i]
					assert.Equal(t, want.Source, got.Source)
					assert.Equal(t, want.Target, got.Target)
					assert.Equal(t, want.ReadOnly, got.ReadOnly)
					assert.Equal(t, mount.TypeBind, got.Type)
				}
			}
		})
	}
}

// stubChannel is a minimal channel.Channel for testing env construction.
type stubChannel struct{}

func (c *stubChannel) Ready() bool                                          { return true }
func (c *stubChannel) CreateTopic(string) (string, error)                   { return "", nil }
func (c *stubChannel) SendMessage(string, string) (string, error)           { return "", nil }
func (c *stubChannel) ReplyMessage(string, string, string) (string, error)  { return "", nil }
func (c *stubChannel) SendApproval(string, string, string) (string, error)  { return "", nil }
func (c *stubChannel) EditMessage(string, string, string) error             { return nil }
func (c *stubChannel) FormatText(s string) string                           { return s }
func (c *stubChannel) SendTyping(string) error                              { return nil }
func (c *stubChannel) CloseTopic(string) error                              { return nil }
func (c *stubChannel) OnAction(func(string, string))                        {}
func (c *stubChannel) OnMessage(func(string, string, string))               {}
func (c *stubChannel) Commands() []channel.Command                          { return nil }

func envContains(envList []string, key string) (string, bool) {
	for _, e := range envList {
		if len(e) > len(key) && e[:len(key)+1] == key+"=" {
			return e[len(key)+1:], true
		}
	}
	return "", false
}

func TestBuildAgentEnv(t *testing.T) {
	workDir := t.TempDir()

	t.Run("base env always present", func(t *testing.T) {
		envList, _ := BuildAgentEnv(RunParams{
			Prompt:      "do stuff",
			JobID:       "job-123",
			CallbackURL: "http://localhost:8080/callback/job-123",
		}, workDir)

		v, ok := envContains(envList, "PROMPT")
		assert.True(t, ok)
		assert.Equal(t, "do stuff", v)

		v, ok = envContains(envList, "JOB_ID")
		assert.True(t, ok)
		assert.Equal(t, "job-123", v)

		v, ok = envContains(envList, "CALLBACK_URL")
		assert.True(t, ok)
		assert.Equal(t, "http://localhost:8080/callback/job-123", v)
	})

	t.Run("SESSION_ID included when set", func(t *testing.T) {
		envList, _ := BuildAgentEnv(RunParams{
			Prompt:      "follow up",
			JobID:       "job-456",
			CallbackURL: "http://localhost:8080/callback/job-456",
			SessionID:   "sess-abc",
		}, workDir)

		v, ok := envContains(envList, "SESSION_ID")
		assert.True(t, ok)
		assert.Equal(t, "sess-abc", v)
	})

	t.Run("SESSION_ID omitted when empty", func(t *testing.T) {
		envList, _ := BuildAgentEnv(RunParams{
			Prompt:      "new task",
			JobID:       "job-789",
			CallbackURL: "http://localhost:8080/callback/job-789",
		}, workDir)

		_, ok := envContains(envList, "SESSION_ID")
		assert.False(t, ok)
	})

	t.Run("HOOKS_URL set when channel present", func(t *testing.T) {
		envList, _ := BuildAgentEnv(RunParams{
			Prompt:      "with hooks",
			JobID:       "job-hooks",
			CallbackURL: "http://localhost:8080/callback/job-hooks",
			Channel:     &stubChannel{},
			TopicID:     "topic-1",
		}, workDir)

		v, ok := envContains(envList, "HOOKS_URL")
		assert.True(t, ok)
		assert.Equal(t, "http://localhost:8080/hooks/job-hooks", v)
	})

	t.Run("HOOKS_URL omitted without channel", func(t *testing.T) {
		envList, _ := BuildAgentEnv(RunParams{
			Prompt:      "no channel",
			JobID:       "job-nohook",
			CallbackURL: "http://localhost:8080/callback/job-nohook",
		}, workDir)

		_, ok := envContains(envList, "HOOKS_URL")
		assert.False(t, ok)
	})

	t.Run("HOOKS_URL omitted without topic", func(t *testing.T) {
		envList, _ := BuildAgentEnv(RunParams{
			Prompt:      "channel but no topic",
			JobID:       "job-notopic",
			CallbackURL: "http://localhost:8080/callback/job-notopic",
			Channel:     &stubChannel{},
		}, workDir)

		_, ok := envContains(envList, "HOOKS_URL")
		assert.False(t, ok)
	})

	t.Run("HOOKS_URL set for all agent types", func(t *testing.T) {
		for _, agentType := range []string{"claude", "opencode", "custom-future-agent"} {
			t.Run(agentType, func(t *testing.T) {
				envList, _ := BuildAgentEnv(RunParams{
					AgentType:   agentType,
					Prompt:      "test",
					JobID:       "job-" + agentType,
					CallbackURL: "http://localhost:8080/callback/job-" + agentType,
					Channel:     &stubChannel{},
					TopicID:     "topic-1",
				}, workDir)

				v, ok := envContains(envList, "HOOKS_URL")
				assert.True(t, ok, "HOOKS_URL should be set for agent type %q", agentType)
				assert.Contains(t, v, "/hooks/")
			})
		}
	})
}
