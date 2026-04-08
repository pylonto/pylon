package cmd

import (
	"github.com/spf13/cobra"

	"github.com/pylonto/pylon/internal/config"
)

// completePylonNames provides tab-completion for commands that take a pylon name.
func completePylonNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names, err := config.ListPylons()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
