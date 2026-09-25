package cmd

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/pidebug"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Operator configs may be 0644 inside the private HOME; role markers may not.
// Capture files have a different owner/lifecycle and stay exclusively 0600.
func readPiOperatorFile(path string, maximum int, private bool) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(path)
	before, statErr := os.Lstat(path)
	if err != nil || resolved != path || statErr != nil || !before.Mode().IsRegular() {
		return nil, errors.New("pi_config_invalid")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("pi_config_invalid")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !os.SameFile(before, info) || !info.Mode().IsRegular() || info.Size() > int64(maximum) {
		return nil, errors.New("pi_config_invalid")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	mask := os.FileMode(0022)
	if private {
		mask = 0077
	}
	if !ok || owner.Uid != uint32(os.Geteuid()) || owner.Nlink != 1 || info.Mode().Perm()&mask != 0 {
		return nil, errors.New("pi_config_invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(maximum)+1))
	if err != nil || len(raw) > maximum {
		return nil, errors.New("pi_config_invalid")
	}
	return raw, nil
}

func inspectPiJob(command *cobra.Command, home, name string) error {
	job, _ := command.Flags().GetString("job")
	if !pidebug.ValidJob(job) {
		return errors.New("pi_debug_job_required")
	}
	raw, err := readPiOperatorFile(config.PylonPath(name), pidebug.MaxEventBytes, false)
	var pyl config.PylonConfig
	if err != nil || yaml.Unmarshal(raw, &pyl) != nil || pyl.Name != name || pyl.Agent == nil || pyl.Agent.Type != "pi" || pyl.Agent.Pi == nil {
		return errors.New("pi_config_invalid")
	}
	p := pyl.Agent.Pi
	if p.DebugDir == "" {
		return errors.New("pi_debug_disabled")
	}
	if p.AuthDir != filepath.Join(home, ".pylon", "pi-auth") {
		return errors.New("pi_dedicated_auth_directory_required")
	}
	evidence := filepath.Join(home, ".pylon", "pi-patches")
	context := pidebug.Context(name, pyl.Workspace.Repo, p.AuthDir, evidence)
	inspection, err := pidebug.Inspect(p.DebugDir, name, job, context, pyl.Workspace.Repo, p.AuthDir, evidence)
	if err != nil {
		return err
	}
	return json.NewEncoder(command.OutOrStdout()).Encode(inspection)
}
