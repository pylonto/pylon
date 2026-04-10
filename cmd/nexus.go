package cmd

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/pylonto/pylon/internal/tui"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(nexusCmd)
}

var nexusCmd = &cobra.Command{
	Use:   "nexus",
	Short: "Launch the TUI dashboard",
	Long:  "Interactive dashboard for managing pylons, running setup, and monitoring jobs.",
	RunE: func(cmd *cobra.Command, args []string) error {
		app := tui.NewApp(Version)
		p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
		_, err := p.Run()
		return err
	},
}
