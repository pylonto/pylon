package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--setup" {
		RunSetup()
		return
	}

	configPath := "pylon.yaml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := NewJobStore()
	pending := NewPendingJobs()
	limiter := NewAgentLimiter()

	var notifier Notifier
	if cfg.Telegram != nil && cfg.Telegram.BotToken != "" {
		notifier = NewTelegramNotifier(ctx, cfg.Telegram.BotToken, cfg.Telegram.ChatID)
		RegisterApprovalHandler(notifier, pending, store, cfg.Server.Port, limiter)
		log.Println("[pylon] telegram notifications enabled")
	}

	mux := http.NewServeMux()
	RegisterPipelineRoutes(mux, cfg, store, notifier, pending, limiter)
	RegisterCallbackRoute(mux, store, notifier, pending)
	RegisterHooksRoute(mux, notifier, pending)

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	server := &http.Server{Addr: addr, Handler: mux}

	// Graceful shutdown on SIGINT/SIGTERM.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("[pylon] received %v, shutting down...", sig)
		cancel()
		server.Close()
	}()

	log.Printf("[pylon] listening on %s", addr)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
	log.Println("[pylon] stopped")
}
