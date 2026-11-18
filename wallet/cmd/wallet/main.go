// Command wallet is the credit wallet service.
//
// It listens on 0.0.0.0:PORT — not loopback — because under Docker the payment
// provider reaches it as host.docker.internal:8080, and a service bound to
// loopback is a service the webhooks never arrive at.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"wallet/internal/api"
	"wallet/internal/config"
	"wallet/internal/db"
	"wallet/internal/ingest"
	"wallet/internal/store"
)

const (
	// databaseWait is how long to wait for the database at startup. A container
	// that is listening is not yet a database that is ready, and failing fast
	// here would only move the flake into whoever starts us.
	databaseWait = 60 * time.Second

	// shutdownGrace lets in-flight work finish. A webhook that has committed
	// must get its 2xx out; one that has not must not be acknowledged.
	shutdownGrace = 10 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("exiting", "err", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	openCtx, cancelOpen := context.WithTimeout(ctx, databaseWait)
	defer cancelOpen()

	pool, err := db.Open(openCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if cfg.MigrateOnStart {
		if err := db.Migrate(ctx, pool); err != nil {
			return err
		}
		logger.Info("schema applied")
	}

	handler := api.New(
		store.New(pool),
		ingest.NewHandler(pool, cfg.WebhookSecret, cfg.WebhookTolerance),
		logger,
	)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr, "provider", cfg.ProviderBaseURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-serveErr
	}
}
