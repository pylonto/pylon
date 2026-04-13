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

const Registry = "ghcr.io/pylonto"

// ImageName returns the fully qualified image name for an agent type.
func ImageName(agentType string) string {
	return Registry + "/agent-" + agentType
}

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

	image := ImageName(agentType)
	cmd := exec.Command("docker", "build", "-t", image, tmpDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		os.Stderr.Write(output)
		return fmt.Errorf("docker build failed: %w", err)
	}
	return nil
}

// pull attempts to docker pull the image. Returns true on success.
func pull(image string) bool {
	cmd := exec.Command("docker", "pull", image)
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = output
		return false
	}
	return true
}

// imageExists checks whether a Docker image exists locally.
func imageExists(image string) bool {
	out, err := exec.Command("docker", "images", image, "-q").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// Rebuild pulls the latest agent images from the registry, falling back
// to building from embedded Dockerfiles. Only updates images that already
// exist locally (i.e., ones the user has previously chosen).
func Rebuild() {
	for _, agentType := range []string{"claude", "opencode"} {
		image := ImageName(agentType)
		if !imageExists(image) {
			continue
		}
		fmt.Printf("Updating %s...\n", image)
		if pull(image) {
			continue
		}
		fmt.Printf("  Pull failed, rebuilding from embedded Dockerfile...\n")
		if err := Build(agentType); err != nil {
			log.Printf("Warning: failed to rebuild %s: %v", image, err)
		}
	}
}

// Ensure makes sure the agent image is available locally. It first checks
// for a local copy, then tries pulling from the registry, and falls back
// to building from the embedded Dockerfile.
func Ensure(agentType string) {
	image := ImageName(agentType)
	if imageExists(image) {
		return
	}
	fmt.Printf("Pulling agent image %s...\n", image)
	if pull(image) {
		return
	}
	fmt.Printf("  Pull failed, building from embedded Dockerfile...\n")
	if err := Build(agentType); err != nil {
		fmt.Printf("  Warning: %v\n", err)
		fmt.Printf("  Run manually: docker pull %s\n", image)
	}
}
