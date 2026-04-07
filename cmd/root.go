package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags.
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:   "pylon",
	Short: "Pylon -- power your AI coding agents",
	Long:  "Self-hosted daemon that listens for events, spins up isolated AI coding agents in Docker, and reports results back to chat.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("pylon %s\n", Version)
	},
}

// comingSoon prints a standard "coming soon" message for unimplemented features.
func comingSoon(name string) {
	fmt.Printf("[%s] support coming soon. Follow https://github.com/pylonto/pylon for updates.\n", name)
}
