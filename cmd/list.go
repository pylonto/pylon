package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/runner"
	"github.com/pylonto/pylon/internal/store"
)

func init() {
	testCmd.Flags().String("payload", "", "Custom JSON payload")
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(editCmd)
	rootCmd.AddCommand(testCmd)
	rootCmd.AddCommand(destroyCmd)
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all constructed pylons",
	RunE:  runList,
}

var editCmd = &cobra.Command{
	Use:               "edit <name>",
	Short:             "Open pylon config in $EDITOR",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completePylonNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := config.PylonPath(args[0])
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("pylon %q not found", args[0])
		}
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		c := exec.Command(editor, path)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return c.Run()
	},
}

var testCmd = &cobra.Command{
	Use:               "test <name>",
	Short:             "Send a mock webhook to trigger the pipeline",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completePylonNames,
	RunE:              runTest,
}

var destroyCmd = &cobra.Command{
	Use:               "destroy <name>",
	Short:             "Delete a pylon and its config",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completePylonNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if _, err := config.LoadPylon(name); err != nil {
			return fmt.Errorf("pylon %q not found", name)
		}
		fmt.Printf("This will delete pylon %q and all its job history.\n", name)
		fmt.Print("Are you sure? [y/N] ")
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("Cancelled.")
			return nil
		}
		// Clean up job workspaces before removing the pylon directory.
		for _, id := range store.JobIDsFromDB(config.PylonDBPath(name)) {
			runner.CleanupWorkspace(id)
		}
		if err := config.DeletePylon(name); err != nil {
			return err
		}
		fmt.Printf("Pylon %q destroyed.\n", name)
		return nil
	},
}

func runList(cmd *cobra.Command, args []string) error {
	names, err := config.ListPylons()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Println("No pylons constructed. Run `pylon construct <name>` to create one.")
		return nil
	}

	fmt.Printf("%-28s %-12s %-30s %s\n", "NAME", "TRIGGER", "PATH", "DESCRIPTION")
	for _, name := range names {
		pyl, err := config.LoadPylon(name)
		if err != nil {
			fmt.Printf("%-28s %-12s %-30s %s\n", name, "?", "", "error loading config")
			continue
		}
		trigger := pyl.Trigger.Type
		path := pyl.Trigger.Path
		if pyl.Trigger.Cron != "" {
			path = pyl.Trigger.Cron + " (" + describeCron(pyl.Trigger.Cron) + ")"
		}
		fmt.Printf("%-28s %-12s %-30s %s\n", name, trigger, path, pyl.Description)
	}
	return nil
}

func runTest(cmd *cobra.Command, args []string) error {
	name := args[0]
	pyl, err := config.LoadPylon(name)
	if err != nil {
		return fmt.Errorf("pylon %q not found: %w", name, err)
	}

	global, err := config.LoadGlobal()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if pyl.Trigger.Type != "webhook" {
		return fmt.Errorf("pylon %q uses trigger type %q, not webhook", name, pyl.Trigger.Type)
	}

	customPayload, _ := cmd.Flags().GetString("payload")
	var payload []byte
	if customPayload != "" {
		payload = []byte(customPayload)
	} else {
		// Build mock payload using the pylon's own workspace config where possible
		repo := pyl.Workspace.Repo
		if repo == "" || strings.Contains(repo, "{{") {
			repo = "https://github.com/kelseyhightower/nocode"
		}
		ref := pyl.Workspace.Ref
		if ref == "" || strings.Contains(ref, "{{") {
			ref = "master"
		}
		mock := map[string]interface{}{
			"repo":  repo,
			"ref":   ref,
			"error": fmt.Sprintf("Test error from pylon test %s", name),
			"issue": map[string]interface{}{
				"title":   fmt.Sprintf("Test issue for %s", name),
				"culprit": "test.go:42",
			},
		}
		payload, _ = json.MarshalIndent(mock, "", "  ")
	}

	url := fmt.Sprintf("http://%s:%d%s", global.Server.Host, global.Server.Port, pyl.Trigger.Path)
	fmt.Printf("Sending test webhook to %s\n", url)
	fmt.Printf("Payload: %s\n\n", string(payload))

	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("request failed (is `pylon start` running?): %w", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Printf("Response [%d]: %s\n", resp.StatusCode, string(out))
	return nil
}
