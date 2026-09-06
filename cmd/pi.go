package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/daemon"
	"github.com/pylonto/pylon/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	command := &cobra.Command{Use: "pi-worker serve|status|pause|resume NAME", Short: "Operate one isolated Pi subscription role (never the daily daemon)", Args: cobra.ExactArgs(2), RunE: runPiWorker}
	command.Flags().String("home", "", "Explicit private Pi-only HOME")
	rootCmd.AddCommand(command)
}

func piHome(home string) error {
	if !filepath.IsAbs(home) {
		return errors.New("pi_private_home_required")
	}
	resolved, err := filepath.EvalSymlinks(home)
	info, statErr := os.Stat(home)
	if err != nil || resolved != home || statErr != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("pi_private_home_required")
	}
	root := filepath.Join(home, ".pylon")
	if resolved, err := filepath.EvalSymlinks(root); err != nil || resolved != root {
		return errors.New("pi_private_config_required")
	}
	marker := filepath.Join(root, "pi-only")
	info, err = os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() != int64(len("pylon-pi-v1\n")) {
		return errors.New("pi_role_marker_required")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "pylon-pi-v1\n" {
		return errors.New("pi_role_marker_required")
	}
	return os.Setenv("HOME", home)
}

func runPiWorker(command *cobra.Command, args []string) error {
	if args[0] != "serve" && args[0] != "status" && args[0] != "pause" && args[0] != "resume" {
		return errors.New("pi_operation_invalid")
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`).MatchString(args[1]) {
		return errors.New("pi_name_invalid")
	}
	home, _ := command.Flags().GetString("home")
	if err := piHome(home); err != nil {
		return err
	}
	config.LoadEnv()
	global, err := config.LoadGlobal()
	if err != nil {
		return errors.New("pi_global_config_invalid")
	}
	pyl, err := config.LoadPylon(args[1])
	if err != nil || pyl.Name != args[1] || pyl.ValidatePi() != nil {
		return errors.New("pi_config_invalid")
	}
	// Auth cannot point outside this declared role, including through a symlink.
	if pyl.Agent.Pi.AuthDir != filepath.Join(home, ".pylon", "pi-auth") {
		return errors.New("pi_dedicated_auth_directory_required")
	}
	names, err := config.ListPylons()
	if err != nil || len(names) != 1 || names[0] != pyl.Name {
		return errors.New("pi_one_pylon_per_role_required")
	}
	st, err := store.Open(config.PylonDBPath(pyl.Name))
	if err != nil {
		return errors.New("pi_ledger_unavailable")
	}
	defer st.Close()
	if err := st.ConfigureSubscription(pyl.Agent.Pi.Limits, time.Now()); err != nil {
		return err
	}
	if args[0] != "serve" {
		switch args[0] {
		case "pause":
			err = st.PauseSubscription("operator", time.Now())
		case "resume":
			err = st.PauseSubscription("", time.Now())
		}
		if err != nil {
			return err
		}
		status, err := st.SubscriptionStatus(time.Now())
		if err != nil {
			return err
		}
		return json.NewEncoder(command.OutOrStdout()).Encode(status)
	}
	d, err := daemon.NewPi(global, pyl, st)
	if err != nil {
		return err
	}
	listener, err := openListener(net.JoinHostPort(global.Server.Host, strconv.Itoa(global.Server.Port)))
	if err != nil {
		return errors.New("pi_listener_unavailable")
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(command.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Handler: d.Mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	queueDone := make(chan struct{})
	go func() { defer close(queueDone); d.RunDeliveryQueue(ctx) }()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.Serve(listener)
	stop()
	<-queueDone
	d.WaitPiJobs()
	if !errors.Is(err, http.ErrServerClosed) {
		return errors.New("pi_receiver_failed")
	}
	return nil
}
