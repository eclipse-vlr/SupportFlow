package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"supportflow/internal/config"
	"supportflow/internal/httpapi"
	"supportflow/internal/llm"
	"supportflow/internal/store"
	"supportflow/internal/worker"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	poolConfig.MaxConns = int32(cfg.Workers + 6)
	poolConfig.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("could not initialize database pool")
	}
	defer pool.Close()
	repository := store.New(pool, cfg.MaxQueued)
	startup, cancel := context.WithTimeout(ctx, 20*time.Second)
	err = repository.Ping(startup)
	if err == nil {
		err = repository.Migrate(startup)
	}
	cancel()
	if err != nil {
		return errors.New("database startup failed; check connection and schema permissions")
	}
	var analyzer llm.Analyzer = llm.Demo{}
	if cfg.Provider == "openai" {
		client := llm.New(cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.MaxOutputTokens, cfg.LLMTimeout)
		defer client.Close()
		analyzer = client
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("HTTP listener: %w", err)
	}
	server := &http.Server{Handler: httpapi.New(repository, log, cfg.Provider), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()
	workersDone := make(chan struct{})
	w := worker.Worker{Queue: repository, Analyzer: analyzer, Log: log, Timeout: cfg.LLMTimeout, MaxAttempts: cfg.MaxAttempts}
	go func() { defer close(workersDone); w.Run(workerCtx, cfg.Workers) }()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	log.Info("supportflow started", "address", listener.Addr().String(), "provider", cfg.Provider, "workers", cfg.Workers)
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-serverDone:
	}
	// Stop HTTP admission and drain requests before canceling background workers.
	drain, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
	shutdownErr := server.Shutdown(drain)
	drainCancel()
	if shutdownErr != nil {
		_ = server.Close()
	}
	cancelWorkers()
	<-workersDone
	log.Info("supportflow stopped")
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return shutdownErr
}
