package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	Short:             "Fire a pylon on demand (mock webhook for webhook pylons, immediate run for cron)",
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
		// Check existence on disk, not via LoadPylon -- a pylon with a broken
		// config (unset env vars, unsupported type) is exactly the kind we
		// most want to destroy. Using LoadPylon's validation here would wrap
		// every validation error as "not found" and block cleanup.
		if _, err := os.Stat(config.PylonPath(name)); err != nil {
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
	config.LoadEnv()
	pyl, err := resolveTestPylon(name)
	if err != nil {
		return err
	}

	global, err := config.LoadGlobal()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	customPayload, _ := cmd.Flags().GetString("payload")

	switch pyl.Trigger.Type {
	case "webhook":
		return fireWebhook(pyl, global, customPayload)
	case "cron":
		if customPayload != "" {
			fmt.Fprintln(os.Stderr, "Note: --payload is ignored for cron pylons.")
		}
		return fireCron(name, global)
	default:
		return fmt.Errorf("pylon %q uses unsupported trigger type %q", name, pyl.Trigger.Type)
	}
}

func fireWebhook(pyl *config.PylonConfig, global *config.GlobalConfig, customPayload string) error {
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
			"error": fmt.Sprintf("Test error from pylon test %s", pyl.Name),
			"issue": map[string]interface{}{
				"title":   fmt.Sprintf("Test issue for %s", pyl.Name),
				"culprit": "test.go:42",
			},
		}
		payload, _ = json.MarshalIndent(mock, "", "  ")
	}

	url := fmt.Sprintf("http://%s:%d%s", loopbackHost(global.Server.Host), global.Server.Port, pyl.Trigger.Path)
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

// fireCron triggers a cron pylon immediately by calling the daemon's generic
// /trigger/{name} endpoint (the same seam the TUI uses).
func fireCron(name string, global *config.GlobalConfig) error {
	url := fmt.Sprintf("http://%s:%d/trigger/%s", loopbackHost(global.Server.Host), global.Server.Port, name)
	fmt.Printf("Firing cron pylon %s via %s\n\n", name, url)

	resp, err := http.Post(url, "application/json", bytes.NewReader([]byte("{}")))
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

// resolveTestPylon loads a pylon by name and distinguishes "not found" from
// validation errors so the caller can report them accurately. Prior behavior
// wrapped every error as "pylon X not found", which was misleading when the
// pylon existed but had an unset env var in its channel config.
func resolveTestPylon(name string) (*config.PylonConfig, error) {
	pyl, err := config.LoadPylon(name)
	if err == nil {
		return pyl, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("pylon %q not found", name)
	}
	return nil, err
}
