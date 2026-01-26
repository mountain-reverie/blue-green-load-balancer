package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/app"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/config"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/logging"
)

func main() {
	configPath := flag.String("config", "config/config.yaml", "Path to configuration file")
	localAdmin := flag.String("local-admin", "", "Run admin server locally on this address (e.g., :8081) instead of Tailscale")
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	flag.Parse()

	// Set up logging
	var level slog.Level
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	// Use journald handler when running under systemd, otherwise JSON
	var handler slog.Handler
	if logging.IsUnderSystemd() {
		handler = logging.NewJournaldHandler(level)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	logger := slog.New(handler)

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	logger.Info("starting blue/green load balancer",
		"blue_url", cfg.Services.Blue.URL,
		"green_url", cfg.Services.Green.URL,
		"listen_addr", cfg.Proxy.ListenAddr,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create application with all components
	application, err := app.New(cfg,
		app.WithLogger(logger),
		app.WithPrometheusRegistry(prometheus.DefaultRegisterer),
		app.WithConfigPath(*configPath),
	)
	if err != nil {
		logger.Error("failed to create application", "error", err)
		os.Exit(1)
	}

	// Start application components (health checker, SSE updates, git watcher)
	if err := application.Start(ctx); err != nil {
		logger.Error("failed to start application", "error", err)
		os.Exit(1)
	}
	defer application.Stop()

	// Start admin server
	if *localAdmin != "" {
		if err := application.StartAdminServer(ctx, *localAdmin); err != nil {
			logger.Error("failed to start local admin server", "error", err)
			os.Exit(1)
		}
		logger.Info("admin server started locally", "addr", *localAdmin)
	} else {
		if err := application.Admin.Start(ctx); err != nil {
			logger.Error("failed to start admin server", "error", err)
			os.Exit(1)
		}
	}

	// Start proxy server
	proxyErrCh := application.StartProxyServer()

	// Notify systemd that we're ready
	if err := logging.NotifyReady(); err != nil {
		logger.Debug("failed to notify systemd ready", "error", err)
	}
	if err := logging.NotifyStatus("Running"); err != nil {
		logger.Debug("failed to set systemd status", "error", err)
	}

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	for {
		select {
		case sig := <-sigCh:
			switch sig {
			case syscall.SIGHUP:
				// Reload configuration
				logger.Info("received SIGHUP, reloading configuration")

				// Notify systemd we're reloading
				if err := logging.NotifyReloading(); err != nil {
					logger.Debug("failed to notify systemd reloading", "error", err)
				}
				if err := logging.NotifyStatus("Reloading configuration..."); err != nil {
					logger.Debug("failed to set systemd status", "error", err)
				}

				// Perform reload
				if err := application.Reload(); err != nil {
					logger.Error("failed to reload configuration", "error", err)
				}

				// Notify systemd we're ready again
				if err := logging.NotifyReady(); err != nil {
					logger.Debug("failed to notify systemd ready", "error", err)
				}
				if err := logging.NotifyStatus("Running"); err != nil {
					logger.Debug("failed to set systemd status", "error", err)
				}

			case syscall.SIGINT, syscall.SIGTERM:
				logger.Info("received shutdown signal", "signal", sig)

				// Notify systemd we're stopping
				if err := logging.NotifyStopping(); err != nil {
					logger.Debug("failed to notify systemd stopping", "error", err)
				}
				if err := logging.NotifyStatus("Shutting down..."); err != nil {
					logger.Debug("failed to set systemd status", "error", err)
				}

				// Graceful shutdown (handled by defer application.Stop())
				logger.Info("shutting down...")
				return
			}

		case err := <-proxyErrCh:
			if err != nil {
				logger.Error("proxy server error", "error", err)
			}
			return

		case <-ctx.Done():
			logger.Info("context cancelled")
			return
		}
	}
}

func init() {
	// Print version info
	fmt.Println("Blue/Green Load Balancer v0.1.0")
}
