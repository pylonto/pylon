package runner

import (
	"encoding/hex"
	"path/filepath"
	"testing"

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
