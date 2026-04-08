package main

import (
	"embed"

	"github.com/pylonto/pylon/cmd"
	"github.com/pylonto/pylon/internal/agentimage"
)

//go:embed agent/claude/Dockerfile agent/claude/entrypoint.sh agent/opencode/Dockerfile agent/opencode/entrypoint.sh
var agentFS embed.FS

func main() {
	agentimage.SetFS(agentFS)
	cmd.Execute()
}
