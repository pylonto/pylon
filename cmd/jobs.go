package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/runner"
	"github.com/pylonto/pylon/internal/store"
)

func init() {
	rootCmd.AddCommand(jobsCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(attachCmd)
	rootCmd.AddCommand(retryCmd)
	rootCmd.AddCommand(killCmd)
}

var jobsCmd = &cobra.Command{
	Use:   "jobs [pylon-name]",
	Short: "List recent jobs",
	RunE:  runJobs,
}

var logsCmd = &cobra.Command{
	Use:   "logs <job-id>",
	Short: "Show logs for a job",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return dockerLogs(args[0])
	},
}

var attachCmd = &cobra.Command{
	Use:   "attach <job-id>",
	Short: "Exec into a running agent container",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := exec.Command("docker", "exec", "-it", args[0], "/bin/sh")
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return c.Run()
	},
}

var retryCmd = &cobra.Command{
	Use:   "retry <job-id>",
	Short: "Re-run a failed job",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Retry is available when the daemon is running. Use the Telegram UI or re-trigger the webhook.")
	},
}

var killCmd = &cobra.Command{
	Use:   "kill <job-id>",
	Short: "Kill a running job",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := exec.Command("docker", "kill", args[0])
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		return c.Run()
	},
}

func runJobs(cmd *cobra.Command, args []string) error {
	pylonNames, err := config.ListPylons()
	if err != nil {
		return err
	}

	var filterPylon string
	if len(args) > 0 {
		filterPylon = args[0]
	}

	// Open DB(s) and query
	var allJobs []*store.Job
	for _, name := range pylonNames {
		if filterPylon != "" && name != filterPylon {
			continue
		}
		st, err := store.Open(config.PylonDBPath(name))
		if err != nil {
			continue
		}
		jobs, err := st.RecentJobs("", 20)
		st.Close()
		if err != nil {
			continue
		}
		allJobs = append(allJobs, jobs...)
	}

	// Also try global DB
	globalDB := config.Dir() + "/pylon.db"
	if _, err := os.Stat(globalDB); err == nil {
		st, err := store.Open(globalDB)
		if err == nil {
			jobs, _ := st.RecentJobs(filterPylon, 50)
			st.Close()
			allJobs = append(allJobs, jobs...)
		}
	}

	if len(allJobs) == 0 {
		fmt.Println("No jobs found.")
		return nil
	}

	// Deduplicate by ID
	seen := make(map[string]bool)
	var unique []*store.Job
	for _, j := range allJobs {
		if !seen[j.ID] {
			seen[j.ID] = true
			unique = append(unique, j)
		}
	}

	fmt.Printf("%-10s %-24s %-14s %-20s %s\n", "ID", "PYLON", "STATUS", "TRIGGERED", "DURATION")
	for _, j := range unique {
		id := j.ID
		if len(id) > 8 {
			id = id[:8]
		}
		ago := timeAgo(j.CreatedAt)
		dur := "--"
		if j.CompletedAt != nil {
			start := j.CreatedAt
			if j.StartedAt != nil {
				start = *j.StartedAt
			}
			dur = j.CompletedAt.Sub(start).Round(time.Second).String()
		}

		status := j.Status
		fmt.Printf("%-10s %-24s %-14s %-20s %s\n", id, j.PylonName, status, ago, dur)
	}

	// Check for queued jobs
	queued := 0
	for _, j := range unique {
		if j.Status == "queued" || j.Status == "pending" {
			queued++
		}
	}
	if queued > 0 {
		fmt.Printf("\nYou must construct additional pylons. (%d queued)\n", queued)
	}

	return nil
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d sec ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

func dockerLogs(jobID string) error {
	// Try to find container by label
	out, err := exec.Command("docker", "ps", "-a", "--filter",
		fmt.Sprintf("label=pylon.job=%s", jobID), "--format", "{{.ID}}").Output()
	if err != nil {
		return fmt.Errorf("finding container: %w", err)
	}
	containerID := strings.TrimSpace(string(out))
	if containerID == "" {
		// Try workspace logs
		logDir := runner.WorkDir(jobID)
		fmt.Printf("No running container found. Workspace: %s\n", logDir)
		return nil
	}
	c := exec.Command("docker", "logs", "-f", containerID)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}
