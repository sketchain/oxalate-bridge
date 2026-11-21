// Command bridge runs the oxalate-bridge relay and panel server.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // BRIDGE_TIMEZONE works in images without a zoneinfo database

	"github.com/sketchain/oxalate-bridge/backend/internal/config"
	"github.com/sketchain/oxalate-bridge/backend/internal/demo"
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
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		// For container health checks (the image has no shell or curl):
		// exits 0 when /readyz answers 200.
		os.Exit(healthcheck(os.Args[2:]))
	}
	cfg, err := config.Load()
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) > 1 && os.Args[1] == "seed-demo" {
		if err := seedDemo(ctx, cfg, log, os.Args[2:]); err != nil {
			log.Error("seed-demo failed", "err", err)
			stop()
			os.Exit(1) //nolint:gocritic // stop already called
		}
		return
	}

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

// seedDemo fills the database with demonstration data (see internal/demo).
// It needs the bootstrap administrator and a running cmd/fakecpa.
func seedDemo(ctx context.Context, cfg config.Config, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("seed-demo", flag.ContinueOnError)
	fake := fs.String("fakecpa", "http://127.0.0.1:18400", "URL of the first cmd/fakecpa instance")
	n := fs.Int("accounts", 4, "number of fake instances to import")
	password := fs.String("password", "demo-pass-123", "password of the demo users")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.BootstrapAdminUsername == "" {
		return errors.New("BRIDGE_BOOTSTRAP_ADMIN_USERNAME and BRIDGE_BOOTSTRAP_ADMIN_PASSWORD are required")
	}
	app, err := server.New(ctx, cfg, log, server.Options{InstanceID: "seed-demo"})
	if err != nil {
		return err
	}
	defer app.Close()
	app.Start(ctx)
	err = demo.Seed(ctx, app, log, demo.Options{FakeCPA: *fake, Accounts: *n, AdminUsername: cfg.BootstrapAdminUsername,
		AdminPassword: cfg.BootstrapAdminPassword, UserPassword: *password})
	if errors.Is(err, demo.ErrAlreadySeeded) {
		log.Info("demo data already present; nothing to do")
		return nil
	}
	if err == nil {
		log.Info("demo data ready", "users", demo.Users, "password", *password, "admin", cfg.BootstrapAdminUsername)
	}
	return err
}

// healthcheck GETs the given URL (default: /readyz on BRIDGE_LISTEN's port)
// and returns the process exit code.
func healthcheck(args []string) int {
	url := ""
	if len(args) > 0 {
		url = args[0]
	} else {
		listen := os.Getenv("BRIDGE_LISTEN")
		if listen == "" {
			listen = ":8080"
		}
		_, port, err := net.SplitHostPort(listen)
		if err != nil || port == "" {
			port = "8080"
		}
		url = "http://127.0.0.1:" + port + "/readyz"
	}
	hc := &http.Client{Timeout: 3 * time.Second}
	resp, err := hc.Get(url) //nolint:noctx // one-shot CLI probe with a client timeout
	if err != nil {
		os.Stderr.WriteString("healthcheck: " + err.Error() + "\n")
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		os.Stderr.WriteString("healthcheck: " + resp.Status + "\n")
		return 1
	}
	return 0
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
