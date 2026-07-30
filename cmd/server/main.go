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
	"github.com/OrcaxNet/vibe-forge/internal/logredact"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8787"
	}

	// Actionable hints for missing required configuration (criterion 4). The
	// server still starts so /api/health can report the not-ready state, but we
	// make the fix loud at the top of the log. The agent loop accepts either an
	// API key (ANTHROPIC_API_KEY) or a bearer token + base URL
	// (ANTHROPIC_AUTH_TOKEN + ANTHROPIC_BASE_URL, e.g. an Anthropic-compatible
	// platform proxy); both being absent is the "not ready" case.
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	authToken := os.Getenv("ANTHROPIC_AUTH_TOKEN")
	accessPassword := os.Getenv("APP_ACCESS_PASSWORD")
	sessionSecret := os.Getenv("APP_AUTH_SESSION_SECRET")
	if apiKey == "" && authToken == "" {
		log.Printf("WARNING: no model credentials configured (ANTHROPIC_API_KEY is empty and ANTHROPIC_AUTH_TOKEN is empty). " +
			"Copy .env.example to .env and set ANTHROPIC_API_KEY (or ANTHROPIC_AUTH_TOKEN + ANTHROPIC_BASE_URL for a bearer-token gateway). " +
			"The backend will start but /api/health reports model not_configured (503) and run creation will be rejected until configured.")
	}
	if authToken != "" && os.Getenv("ANTHROPIC_BASE_URL") == "" {
		log.Printf("WARNING: ANTHROPIC_AUTH_TOKEN is set but ANTHROPIC_BASE_URL is empty; a bearer token requires a base URL. Set ANTHROPIC_BASE_URL or use ANTHROPIC_API_KEY instead.")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv, err := api.New(ctx)
	if err != nil {
		// PRD-C: migration failure MUST stop startup (no silent skip).
		log.Fatalf("startup failed (refusing to serve): %v", err)
	}
	defer srv.Close()

	// Install a redacting logger for agent run-lifecycle lines. The live secret
	// values are passed in so any accidental leak in an upstream error string is
	// scrubbed to [REDACTED] before reaching container stdout (FLO-59: logs must
	// contain no keys, full prompts or generated code).
	srv.SetLogger(logredact.New(log.Default().Writer(), []string{
		apiKey,
		authToken,
		accessPassword,
		sessionSecret,
	}).Printf)

	// Reconcile runs left active by a prior crash: a queued/running run that did
	// not reach a terminal state is flipped to 'interrupted' so it is not stuck
	// "active" (C-FR-06/07). The user can retry an interrupted run.
	if n, err := srv.ReconcileInterruptedRuns(ctx); err != nil {
		log.Printf("WARNING: failed to reconcile interrupted runs: %v", err)
	} else if n > 0 {
		log.Printf("reconciled %d interrupted run(s) left active by a prior crash", n)
	}

	// Wire the agent loop (FLO-60) if the store and model are configured. With no
	// model key, run creation stays rejected and this is a no-op.
	if err := srv.InitLoop(); err != nil {
		log.Fatalf("agent loop init failed: %v", err)
	}

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
