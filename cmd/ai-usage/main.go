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
	"syscall"
	"time"

	"github.com/tbshfr/ai-usage/internal/api"
	"github.com/tbshfr/ai-usage/internal/config"
	"github.com/tbshfr/ai-usage/internal/ingest"
	"github.com/tbshfr/ai-usage/internal/storage"
	"google.golang.org/grpc"
)

const shutdownGrace = 10 * time.Second

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
	logger.Info("ai-usage starting",
		"dashboard", cfg.HTTPAddr,
		"otlp_http", cfg.OTLPHTTPAddr,
		"otlp_grpc", cfg.OTLPGRPCAddr,
		"database", cfg.DatabasePath,
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

	pipeline := ingest.NewPipeline(db, logger)
	dashboardSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.New(db, logger, pipeline.Stats),
		ReadHeaderTimeout: 10 * time.Second,
	}
	otlpSrv := &http.Server{
		Addr:              cfg.OTLPHTTPAddr,
		Handler:           ingest.NewReceiver(pipeline, logger).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 3)
	servers := []*http.Server{dashboardSrv, otlpSrv}
	addrs := []string{cfg.HTTPAddr, cfg.OTLPHTTPAddr}
	for i, srv := range servers {
		go func(srv *http.Server, addr string) {
			logger.Info("http listener started", "addr", addr)
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("listen %s: %w", addr, err)
			}
		}(srv, addrs[i])
	}

	var grpcServer *grpc.Server
	if cfg.OTLPGRPCAddr != "" {
		grpcServer = ingest.NewGRPCServer(pipeline, logger)
		ln, err := ingest.ServeGRPC(grpcServer, cfg.OTLPGRPCAddr)
		if err != nil {
			return err
		}
		logger.Info("grpc listener started", "addr", ln.Addr().String())
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-stop:
		logger.Info("shutdown started", "signal", sig.String())
	case err := <-errCh:
		return err
	}

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
	logger.Info("shutdown complete")
	return nil
}
