package main

import (
	"context"
	"fmt"
	"io"
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

	mux := http.NewServeMux()

	// Register a handler for each pipeline based on its trigger path.
	for name, pipeline := range cfg.Pipelines {
		name := name         // capture for closure
		pipeline := pipeline // capture for closure

		if pipeline.Trigger.Type != "webhook" {
			log.Printf("[pylon] skipping pipeline %q: unknown trigger type %q", name, pipeline.Trigger.Type)
			continue
		}

		log.Printf("[pylon] registered pipeline %q on POST %s", name, pipeline.Trigger.Path)

		mux.HandleFunc(pipeline.Trigger.Path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}

			// Read the full request body — this becomes the {{ .body }} template value.
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read body", http.StatusBadRequest)
				return
			}
			defer r.Body.Close()

			log.Printf("[pylon] pipeline %q triggered, payload: %s", name, string(body))

			// Resolve environment variable templates with the webhook payload.
			env := resolveEnv(pipeline.Env, string(body))

			// Build a context with the configured timeout so the container
			// gets killed if it runs too long.
			ctx := r.Context()
			if pipeline.Container.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, pipeline.Container.Timeout)
				defer cancel()
			}

			// Run the container in a goroutine so the HTTP response returns immediately.
			// The caller gets a 202 Accepted and the job runs in the background.
			go func() {
				if err := RunContainer(context.Background(), pipeline, env); err != nil {
					log.Printf("[pylon] pipeline %q failed: %v", name, err)
					return
				}
			}()

			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, "pipeline %q triggered\n", name)
		})
	}

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
