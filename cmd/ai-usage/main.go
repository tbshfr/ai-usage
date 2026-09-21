// Command ai-usage receives GenAI usage telemetry via OTLP, normalizes it
// into Generation records, stores them in SQLite, and serves the local
// dashboard + JSON API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tbshfr/ai-usage/internal/api"
	"github.com/tbshfr/ai-usage/internal/auth"
	"github.com/tbshfr/ai-usage/internal/backup"
	"github.com/tbshfr/ai-usage/internal/config"
	"github.com/tbshfr/ai-usage/internal/ingest"
	"github.com/tbshfr/ai-usage/internal/live"
	"github.com/tbshfr/ai-usage/internal/pricing"
	"github.com/tbshfr/ai-usage/internal/storage"
	"google.golang.org/grpc"
)

const shutdownGrace = 10 * time.Second

// statsSaveInterval is how often the day's ingestion counters are
// persisted to SQLite; they are also saved once on shutdown.
const statsSaveInterval = time.Minute

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfg, err := config.LoadOS()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	if err := run(cfg, logger); err != nil {
		logger.Error("startup failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(cfg *config.Config, logger *slog.Logger) error {
	printBanner(cfg)
	logger.Info("ai-usage starting",
		"dashboard", cfg.HTTPAddr,
		"otlp_http", cfg.OTLPHTTPAddr,
		"otlp_grpc", cfg.OTLPGRPCAddr,
		"database", cfg.DatabasePath,
		"dashboard_auth", cfg.DashboardAuthEnabled(),
		"otlp_auth", cfg.OTLPToken != "",
	)

	db, err := storage.Open(context.Background(), cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	logger.Info("database opened", "path", cfg.DatabasePath)
	if err := storage.Migrate(db, logger); err != nil {
		return err
	}

	hub := live.New()
	backupCtx, stopBackup := context.WithCancel(context.Background())
	worker, err := backup.New(backupCtx, cfg, db, version, logger, hub.Notify)
	if err != nil {
		stopBackup()
		return err
	}
	backupDone := make(chan struct{})
	if worker != nil {
		go func() { defer close(backupDone); worker.Run(backupCtx) }()
	} else {
		close(backupDone)
	}
	defer func() { stopBackup(); <-backupDone }()
	pipeline := ingest.NewPipeline(db, logger, hub)
	prices := pricing.New(db, logger, hub.Notify)
	pricingCtx, stopPricing := context.WithCancel(context.Background())
	pricingDone := make(chan struct{})
	go func() { defer close(pricingDone); prices.Run(pricingCtx) }()
	defer func() { stopPricing(); <-pricingDone }()
	pipeline.AfterCommit = prices.Notify
	// Continue today's persisted counters across restarts and keep them
	// saved periodically; a final save happens on shutdown. A failed
	// restore is fatal: running with a zero base would let the next save
	// overwrite today's persisted counters with session-only values.
	if err := pipeline.RestoreBase(context.Background()); err != nil {
		return fmt.Errorf("restore stats base: %w", err)
	}
	saverCtx, stopSaver := context.WithCancel(context.Background())
	defer stopSaver()
	pipeline.StartSaver(saverCtx, statsSaveInterval)
	// Empty addresses disable the corresponding listener (config supports
	// this; see internal/config). Non-loopback binds without credentials
	// are rejected by config validation before we get here.
	var sessions *auth.Sessions
	var dash *auth.Dashboard
	if cfg.DashboardAuthEnabled() {
		sessions, err = auth.NewSessions()
		if err != nil {
			return fmt.Errorf("create session secret: %w", err)
		}
		dash = auth.NewDashboard(cfg.DashboardUser, cfg.DashboardPassword, sessions)
	}
	var servers []*http.Server
	if cfg.HTTPAddr != "" {
		srv := &http.Server{
			Addr:              cfg.HTTPAddr,
			Handler:           api.NewWithAuth(db, logger, pipeline.Stats, pipeline.ReasonCounts, hub, version, dash, worker.Status),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		// End the dashboard's SSE streams when shutdown begins; Shutdown
		// waits for active connections and would otherwise always burn
		// the whole grace period while a dashboard tab is open.
		srv.RegisterOnShutdown(hub.InterruptStreams)
		servers = append(servers, srv)
	}
	if cfg.OTLPHTTPAddr != "" {
		var h http.Handler = ingest.NewReceiver(pipeline, logger).Handler()
		if cfg.OTLPToken != "" {
			// Count 401s under the fixed http_reject enum: unauthenticated
			// traffic never reaches the pipeline, so this only bumps
			// integer counters on fixed rows (no flood-amplification).
			h = auth.BearerWithHook(logger, cfg.OTLPToken, h, func() {
				pipeline.BumpHTTPReject(ingest.ReasonUnauthorized)
			})
		}
		servers = append(servers, &http.Server{
			Addr:              cfg.OTLPHTTPAddr,
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
		})
	}

	errCh := make(chan error, 3)
	for _, srv := range servers {
		go func(srv *http.Server) {
			logger.Info("http listener started", "addr", srv.Addr)
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("listen %s: %w", srv.Addr, err)
			}
		}(srv)
	}

	var grpcServer *grpc.Server
	if cfg.OTLPGRPCAddr != "" {
		grpcServer = ingest.NewGRPCServer(pipeline, logger, cfg.OTLPToken, func() {
			pipeline.BumpHTTPReject(ingest.ReasonGRPCUnauthorized)
		})
		ln, err := ingest.ServeGRPC(grpcServer, cfg.OTLPGRPCAddr)
		if err != nil {
			return err
		}
		logger.Info("grpc listener started", "addr", ln.Addr().String())
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	select {
	case sig := <-stop:
		logger.Info("shutdown started", "signal", sig.String())
	case err := <-errCh:
		// A listener died; persist the final snapshot before the process
		// exits so the error path loses no more than the counters added
		// since the last periodic save. Double stopSaver with the defer
		// is harmless (context cancel is idempotent).
		stopBackup()
		logger.Error("shutdown started", "reason", "listener error", "error", err.Error())
		stopSaver()
		if saveErr := pipeline.Save(); saveErr != nil {
			logger.Error("stats save failed", "error", saveErr.Error())
		}
		return err
	}

	stopBackup()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("http shutdown failed", "error", err.Error())
		}
	}
	if grpcServer != nil {
		done := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-shutdownCtx.Done():
			grpcServer.Stop()
		}
	}
	// Persist the final counter snapshot while the DB is still open; the
	// saver goroutine is stopped first so it cannot race the last write.
	stopSaver()
	if err := pipeline.Save(); err != nil {
		logger.Error("stats save failed", "error", err.Error())
	}
	<-backupDone
	logger.Info("shutdown complete")
	return nil
}

func printBanner(cfg *config.Config) {
	dbPath := cfg.DatabasePath
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, dbPath); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			dbPath = filepath.Join("~", rel)
		}
	}
	grpc := cfg.OTLPGRPCAddr
	if grpc == "" {
		grpc = "(disabled)"
	}
	authState := "off"
	switch {
	case cfg.DashboardAuthEnabled() && cfg.OTLPToken != "":
		authState = "dashboard + otlp"
	case cfg.DashboardAuthEnabled():
		authState = "dashboard"
	case cfg.OTLPToken != "":
		authState = "otlp"
	}
	fmt.Printf(`
AI Usage Dashboard (%s)

Dashboard: %s
OTLP HTTP: %s
OTLP gRPC: %s
Auth:      %s
Database:  %s

`, version, httpURL(cfg.HTTPAddr), httpURL(cfg.OTLPHTTPAddr), grpc, authState, dbPath)
}

func httpURL(addr string) string {
	if addr == "" {
		return "(disabled)"
	}
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}
