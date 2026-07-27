// Command server starts the Vibe Forge backend HTTP server.
//
// Stage 1: serves GET /api/health (structurally correct, 503 when dependencies
// are not configured) and 501 stubs for the remaining contract paths. Stage 2/3
// replace the stubs with real handlers.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/OrcaxNet/vibe-forge/internal/api"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8787"
	}

	// Actionable hints for missing required configuration (criterion 2). The
	// server still starts so /api/health can report the not-ready state, but we
	// make the fix loud at the top of the log.
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Printf("WARNING: ANTHROPIC_API_KEY is not set. Copy .env.example to .env and set it; the backend will start but /api/health reports model not_configured (503) and run creation will be rejected until it is provided.")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv, err := api.New(ctx)
	if err != nil {
		// PRD-C: migration failure MUST stop startup (no silent skip).
		log.Fatalf("startup failed (refusing to serve): %v", err)
	}
	defer srv.Close()

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("vibe-forge backend listening on :%s (contract %s, build %s)",
			port, api.ContractVersion(), api.Version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
