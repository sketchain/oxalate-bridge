// Command bridge runs the oxalate-bridge relay and panel server.
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

	"github.com/sketchain/oxalate-bridge/backend/internal/config"
	"github.com/sketchain/oxalate-bridge/backend/internal/server"
	"github.com/sketchain/oxalate-bridge/backend/internal/version"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()}))
	slog.SetDefault(log)
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		os.Stdout.WriteString(version.Version + "\n")
		return
	}
	cfg, err := config.Load()
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := server.New(ctx, cfg, log, server.Options{})
	if err != nil {
		log.Error("startup failed", "err", err)
		stop()
		os.Exit(1) //nolint:gocritic // stop already called
	}
	app.Start(ctx)

	srv := &http.Server{Addr: cfg.Listen, Handler: app.Handler(), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second}
	metricsSrv := &http.Server{Addr: cfg.MetricsListen, Handler: server.MetricsHandler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info("listening", "addr", cfg.Listen, "version", version.Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server", "err", err)
		}
	}()
	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	_ = metricsSrv.Shutdown(shutdownCtx)
	app.Close()
}

func logLevel() slog.Level {
	switch os.Getenv("BRIDGE_LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
