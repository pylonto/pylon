package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"

	"github.com/spf13/cobra"
)

func init() {
	serviceCmd.AddCommand(serviceInstallCmd)
	serviceCmd.AddCommand(serviceUninstallCmd)
	serviceCmd.AddCommand(serviceStatusCmd)
	rootCmd.AddCommand(serviceCmd)
}

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage systemd service",
}

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install systemd unit file",
	RunE: func(cmd *cobra.Command, args []string) error {
		u, _ := user.Current()
		binPath, _ := exec.LookPath("pylon")
		if binPath == "" {
			binPath = "/usr/local/bin/pylon"
		}

		// Try user-level first if no root
		if os.Geteuid() != 0 {
			unit := fmt.Sprintf("[Unit]\nDescription=Pylon Agent Daemon\nAfter=network-online.target\n\n[Service]\nType=simple\nExecStart=%s start\nRestart=on-failure\nRestartSec=5\nEnvironment=HOME=%s\n\n[Install]\nWantedBy=default.target\n",
				binPath, u.HomeDir)
			userDir := filepath.Join(u.HomeDir, ".config", "systemd", "user")
			os.MkdirAll(userDir, 0755)
			unitPath := filepath.Join(userDir, "pylon.service")
			if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
				return fmt.Errorf("writing unit file: %w", err)
			}
			exec.Command("systemctl", "--user", "daemon-reload").Run()
			exec.Command("systemctl", "--user", "enable", "pylon").Run()
			fmt.Printf("Installed user service: %s\n", unitPath)
			fmt.Println("Start with: systemctl --user start pylon")
			return nil
		}

		unit := fmt.Sprintf("[Unit]\nDescription=Pylon Agent Daemon\nAfter=network-online.target\n\n[Service]\nType=simple\nUser=%s\nExecStart=%s start\nRestart=on-failure\nRestartSec=5\nEnvironment=HOME=%s\n\n[Install]\nWantedBy=multi-user.target\n",
			u.Username, binPath, u.HomeDir)
		unitPath := "/etc/systemd/system/pylon.service"
		if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
			return fmt.Errorf("writing unit file: %w", err)
		}
		exec.Command("systemctl", "daemon-reload").Run()
		exec.Command("systemctl", "enable", "pylon").Run()
		fmt.Printf("Installed system service: %s\n", unitPath)
		fmt.Println("Start with: systemctl start pylon")
		return nil
	},
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove systemd unit file",
	RunE: func(cmd *cobra.Command, args []string) error {
		paths := []string{
			"/etc/systemd/system/pylon.service",
		}
		u, _ := user.Current()
		paths = append(paths, filepath.Join(u.HomeDir, ".config", "systemd", "user", "pylon.service"))

		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				exec.Command("systemctl", "stop", "pylon").Run()
				exec.Command("systemctl", "disable", "pylon").Run()
				os.Remove(p)
				exec.Command("systemctl", "daemon-reload").Run()
				fmt.Printf("Removed: %s\n", p)
				return nil
			}
		}
		fmt.Println("No pylon service found.")
		return nil
	},
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check systemd service status",
	Run: func(cmd *cobra.Command, args []string) {
		c := exec.Command("systemctl", "status", "pylon")
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			// Try user-level
			c2 := exec.Command("systemctl", "--user", "status", "pylon")
			c2.Stdout, c2.Stderr = os.Stdout, os.Stderr
			c2.Run()
		}
	},
}
