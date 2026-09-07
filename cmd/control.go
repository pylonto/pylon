package cmd

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/pylonto/pylon/internal/channel"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/daemon"
	"github.com/pylonto/pylon/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	command := &cobra.Command{Use: "control NAME", Short: "Run an isolated outbound-only Telegram control receiver (no agents or poller)", Args: cobra.ExactArgs(1), RunE: runControl}
	command.Flags().String("home", "", "Explicit private control-only HOME; never the daily daemon configuration")
	rootCmd.AddCommand(command)
}

func controlHome(home string) error {
	if !filepath.IsAbs(home) {
		return errors.New("explicit private control HOME required")
	}
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("private control HOME required")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(home, ".pylon"))
	if err != nil || resolved != filepath.Join(home, ".pylon") {
		return errors.New("control configuration symlink refused")
	}
	file, err := os.Open(filepath.Join(home, ".pylon", "control-only"))
	if err != nil {
		return errors.New("control-only configuration marker required")
	}
	defer file.Close()
	markerInfo, err := file.Stat()
	if err != nil || !markerInfo.Mode().IsRegular() || markerInfo.Mode().Perm()&0077 != 0 {
		return errors.New("private control marker required")
	}
	marker, err := io.ReadAll(io.LimitReader(file, 32))
	if err != nil || string(marker) != "pylon-control-v1\n" {
		return errors.New("control-only configuration marker required")
	}
	return os.Setenv("HOME", home)
}

func controlChannel(global *config.GlobalConfig, pyl *config.PylonConfig) (channel.Channel, error) {
	ip := net.ParseIP(global.Server.Host)
	if ip == nil || !ip.IsLoopback() || global.Server.Port < 1024 || global.Server.Port > 65535 {
		return nil, errors.New("control receiver requires an unprivileged loopback listener")
	}
	if pyl.Disabled || pyl.Control == nil || pyl.Control.TopicID == "" || pyl.Agent != nil || pyl.Trigger.Type != "webhook" ||
		pyl.Trigger.SignatureHeader != "X-Pylon-Signature" || len(os.ExpandEnv(pyl.Trigger.Secret)) < 32 {
		return nil, errors.New("enabled signed control-only pylon required; agents refused")
	}
	kind, tg, _ := pyl.ResolveChannel(global)
	if kind != "telegram" || tg == nil || tg.ChatID <= 0 || len(tg.AllowedUsers) != 1 || tg.AllowedUsers[0] != tg.ChatID || pyl.Control.TopicID != "0" {
		return nil, errors.New("explicitly verified private Telegram chat and owner required")
	}
	token := config.ExpandWithPylonEnv(tg.BotToken, config.LoadPylonEnvFile(pyl.Name))
	return channel.NewTelegramSender(token, tg.ChatID)
}

func runControl(command *cobra.Command, args []string) error {
	home, _ := command.Flags().GetString("home")
	if err := controlHome(home); err != nil {
		return err
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`).MatchString(args[0]) {
		return errors.New("invalid control pylon name")
	}
	config.LoadEnv()
	global, err := config.LoadGlobal()
	if err != nil {
		return errors.New("control global configuration unavailable or invalid")
	}
	pyl, err := config.LoadPylon(args[0])
	if err != nil || pyl.Name != args[0] {
		return errors.New("control pylon configuration unavailable or invalid")
	}
	ch, err := controlChannel(global, pyl)
	if err != nil {
		return err
	}
	// Binding fails closed. In particular, do not call runDaemonForeground: even its
	// startup/shutdown pruning can touch containers belonging to the daily daemon.
	listener, err := openListener(net.JoinHostPort(global.Server.Host, strconv.Itoa(global.Server.Port)))
	if err != nil {
		return errors.New("control listener unavailable; existing services untouched")
	}
	defer listener.Close()
	st, err := store.Open(config.PylonDBPath(pyl.Name))
	if err != nil {
		return errors.New("control ledger unavailable")
	}
	defer st.Close()
	d := daemon.NewControl(map[string]*config.PylonConfig{pyl.Name: pyl}, store.NewMulti(map[string]*store.Store{pyl.Name: st}), map[string]channel.Channel{pyl.Name: ch})
	server := &http.Server{Handler: d.Mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(command.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.Serve(listener)
	stop()
	<-done
	if !errors.Is(err, http.ErrServerClosed) {
		return errors.New("control receiver stopped unexpectedly")
	}
	return nil
}
