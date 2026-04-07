package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	configPath := "pylon.yaml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	store := NewJobStore()
	mux := http.NewServeMux()

	RegisterPipelineRoutes(mux, cfg, store)
	RegisterCallbackRoute(mux, store)

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	server := &http.Server{Addr: addr, Handler: mux}

	// Graceful shutdown on SIGINT/SIGTERM.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("[pylon] received %v, shutting down...", sig)
		server.Close()
	}()

	log.Printf("[pylon] listening on %s", addr)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
	log.Println("[pylon] stopped")
}
