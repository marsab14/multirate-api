// Command api is the billing service HTTP entrypoint. It loads env
// config, opens the Postgres pool, builds the chi router, and serves
// on http.Server with sensible timeouts. SIGINT/SIGTERM triggers a
// graceful shutdown bounded by a 15s deadline.
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

	"billing-api/internal/app"
	"billing-api/internal/config"
	"billing-api/internal/db"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	env, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", slog.String("err", err.Error()))
		os.Exit(1)
	}

	database, err := db.Open(env.DatabaseURL)
	if err != nil {
		logger.Error("failed to open database", slog.String("err", err.Error()))
		os.Exit(1)
	}
	defer func() { _ = database.Close() }()

	// The signal context is threaded into app.New so the JWKS
	// background-refresh goroutine (spawned by keyfunc) shuts down
	// cleanly on SIGTERM alongside the HTTP server.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	router, err := app.New(ctx, app.Deps{
		Env:    env,
		DB:     database,
		Logger: logger,
	})
	if err != nil {
		logger.Error("failed to build router", slog.String("err", err.Error()))
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              ":" + env.Port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		logger.Info("server listening", slog.String("addr", srv.Addr), slog.String("env", env.Env))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", slog.String("err", err.Error()))
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", slog.String("err", err.Error()))
		os.Exit(1)
	}
	logger.Info("server stopped cleanly")
}
