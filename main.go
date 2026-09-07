package main

import (
	"embed"

	"github.com/pylonto/pylon/cmd"
	"github.com/pylonto/pylon/internal/agentimage"
)

//go:embed agent/claude/Dockerfile agent/claude/entrypoint.sh agent/opencode/Dockerfile agent/opencode/entrypoint.sh agent/pi/Dockerfile agent/pi/.dockerignore agent/pi/package.json agent/pi/package-lock.json agent/pi/meter.mjs agent/pi/stream.mjs agent/pi/catalog.mjs agent/pi/worker.mjs agent/pi/tool.mjs agent/pi/sandbox.mjs agent/pi/debug.mjs agent/pi/role-auth.mjs agent/pi/fixture.mjs
var agentFS embed.FS

func main() {
	agentimage.SetFS(agentFS)
	cmd.Execute()
}
