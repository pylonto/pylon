package agentimage

import (
	"embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var agentFS embed.FS

// SetFS sets the embedded filesystem containing agent/ directories.
// Called from main or cmd package where the go:embed directive lives.
func SetFS(fs embed.FS) {
	agentFS = fs
}

// Build extracts the embedded Dockerfile and entrypoint for the given agent
// type to a temp directory and runs docker build.
func Build(agentType string) error {
	dir := filepath.Join("agent", agentType)
	entries, err := agentFS.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("no embedded agent %q", agentType)
	}

	tmpDir, err := os.MkdirTemp("", "pylon-agent-"+agentType+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	for _, e := range entries {
		data, err := agentFS.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		perm := os.FileMode(0644)
		if strings.HasSuffix(e.Name(), ".sh") {
			perm = 0755
		}
		if err := os.WriteFile(filepath.Join(tmpDir, e.Name()), data, perm); err != nil {
			return err
		}
	}

	image := "pylon/agent-" + agentType
	cmd := exec.Command("docker", "build", "-t", image, tmpDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build failed: %w", err)
	}
	return nil
}

// Rebuild rebuilds all known agent images.
func Rebuild() {
	for _, agentType := range []string{"claude", "opencode"} {
		image := "pylon/agent-" + agentType
		fmt.Printf("Rebuilding %s...\n", image)
		if err := Build(agentType); err != nil {
			log.Printf("Warning: failed to rebuild %s: %v", image, err)
		}
	}
}

// Ensure builds the agent image if it doesn't exist locally.
func Ensure(agentType string) {
	image := "pylon/agent-" + agentType
	if out, err := exec.Command("docker", "images", image, "-q").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		return
	}
	fmt.Printf("Building agent image %s...\n", image)
	if err := Build(agentType); err != nil {
		fmt.Printf("  Warning: %v\n", err)
		fmt.Printf("  Run manually: docker build -t %s agent/%s/\n", image, agentType)
	}
}
