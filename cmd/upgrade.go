package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/pylonto/pylon/internal/agentimage"
)

func init() {
	rootCmd.AddCommand(upgradeCmd)
}

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade pylon to the latest version",
	RunE:  runUpgrade,
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	arch := runtime.GOARCH
	goos := runtime.GOOS
	binary := fmt.Sprintf("pylon-%s-%s", goos, arch)
	url := fmt.Sprintf("https://github.com/pylonto/pylon/releases/latest/download/%s", binary)

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding current binary: %w", err)
	}

	fmt.Printf("Current: pylon %s\n", Version)
	fmt.Printf("Downloading latest from %s...\n", url)

	tmp := self + ".new"
	dl := exec.Command("curl", "-fsSL", "-o", tmp, url)
	dl.Stderr = os.Stderr
	if err := dl.Run(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("download failed: %w", err)
	}

	os.Chmod(tmp, 0755)

	// Verify the new binary runs
	out, err := exec.Command(tmp, "version").Output()
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("new binary failed verification: %w", err)
	}

	if err := os.Rename(tmp, self); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replacing binary: %w (try with sudo)", err)
	}

	fmt.Printf("Upgraded: %s\n", string(out))
	agentimage.Rebuild()
	return nil
}
