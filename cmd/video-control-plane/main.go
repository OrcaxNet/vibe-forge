// Command video-control-plane starts the isolated AI video control-plane
// bootstrap service. It does not alter the existing Vibe Forge backend.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/OrcaxNet/vibe-forge/internal/videopipeline/controlplane"
	"github.com/OrcaxNet/vibe-forge/internal/videopipeline/runtimeconfig"
)

func main() {
	cfg, err := runtimeconfig.LoadControlPlane()
	if err != nil {
		log.Fatalf("invalid video control-plane configuration: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	server := &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           controlplane.New(cfg).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("video control plane listening on %s (version=%s)", cfg.HTTPAddress, cfg.Version)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve video control plane: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("video control-plane shutdown: %v", err)
	}
}
