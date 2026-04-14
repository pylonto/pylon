package agentimage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestImageName(t *testing.T) {
	tests := []struct {
		agentType string
		want      string
	}{
		{"claude", "ghcr.io/pylonto/agent-claude"},
		{"opencode", "ghcr.io/pylonto/agent-opencode"},
		{"custom", "ghcr.io/pylonto/agent-custom"},
	}
	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			assert.Equal(t, tt.want, ImageName(tt.agentType))
		})
	}
}
