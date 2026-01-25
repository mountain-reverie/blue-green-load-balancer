package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/bgctl"
)

// Exit codes
const (
	exitSuccess         = 0
	exitUsageError      = 1
	exitConnectionError = 2
	exitAPIError        = 3
)

func main() {
	cmd := &cli.Command{
		Name:  "bgctl",
		Usage: "Blue/Green Load Balancer control CLI",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "hostname",
				Aliases: []string{"H"},
				Value:   "bluegreen-admin",
				Usage:   "Admin server hostname",
				Sources: cli.EnvVars("BGCTL_HOSTNAME"),
			},
			&cli.StringFlag{
				Name:    "control-url",
				Usage:   "Tailscale/Headscale control server URL",
				Sources: cli.EnvVars("BGCTL_CONTROL_URL"),
			},
			&cli.StringFlag{
				Name:    "auth-key",
				Usage:   "Tailscale auth key",
				Sources: cli.EnvVars("TS_AUTH_KEY"),
			},
			&cli.StringFlag{
				Name:    "state-dir",
				Value:   defaultStateDir(),
				Usage:   "Tailscale state directory",
				Sources: cli.EnvVars("BGCTL_STATE_DIR"),
			},
			&cli.StringFlag{
				Name:    "output",
				Aliases: []string{"o"},
				Value:   "text",
				Usage:   "Output format: json or text",
			},
			&cli.DurationFlag{
				Name:  "timeout",
				Value: 30 * time.Second,
				Usage: "Request timeout",
			},
			&cli.BoolFlag{
				Name:    "verbose",
				Aliases: []string{"v"},
				Usage:   "Enable verbose logging",
			},
		},
		Commands: []*cli.Command{
			{
				Name:   "status",
				Usage:  "Display current blue/green status",
				Action: runStatus,
			},
			{
				Name:   "metrics",
				Usage:  "Display current metrics snapshot",
				Action: runMetrics,
			},
			{
				Name:   "refresh",
				Usage:  "Trigger a git refresh",
				Action: runRefresh,
			},
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitUsageError)
	}
}

func defaultStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".bgctl/tailscale"
	}
	return filepath.Join(home, ".bgctl", "tailscale")
}

func createClient(ctx context.Context, cmd *cli.Command) (*bgctl.Client, error) {
	var logger *slog.Logger
	if cmd.Bool("verbose") {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	} else {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	}

	cfg := &bgctl.Config{
		Hostname:   cmd.String("hostname"),
		ControlURL: cmd.String("control-url"),
		AuthKey:    cmd.String("auth-key"),
		StateDir:   cmd.String("state-dir"),
		Timeout:    cmd.Duration("timeout"),
		Verbose:    cmd.Bool("verbose"),
		Logger:     logger,
	}

	client, err := bgctl.New(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connecting to Tailscale: %w", err)
	}

	return client, nil
}

func runStatus(ctx context.Context, cmd *cli.Command) error {
	client, err := createClient(ctx, cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitConnectionError)
		return nil
	}
	defer client.Close()

	status, err := client.GetStatus(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if isAPIError(err) {
			os.Exit(exitAPIError)
		} else {
			os.Exit(exitConnectionError)
		}
		return nil
	}

	formatter := bgctl.NewFormatter(cmd.String("output"))
	fmt.Print(formatter.FormatStatus(status))
	return nil
}

func runMetrics(ctx context.Context, cmd *cli.Command) error {
	client, err := createClient(ctx, cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitConnectionError)
		return nil
	}
	defer client.Close()

	metrics, err := client.GetMetrics(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if isAPIError(err) {
			os.Exit(exitAPIError)
		} else {
			os.Exit(exitConnectionError)
		}
		return nil
	}

	formatter := bgctl.NewFormatter(cmd.String("output"))
	fmt.Print(formatter.FormatMetrics(metrics))
	return nil
}

func runRefresh(ctx context.Context, cmd *cli.Command) error {
	client, err := createClient(ctx, cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitConnectionError)
		return nil
	}
	defer client.Close()

	refresh, err := client.TriggerRefresh(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if isAPIError(err) {
			os.Exit(exitAPIError)
		} else {
			os.Exit(exitConnectionError)
		}
		return nil
	}

	formatter := bgctl.NewFormatter(cmd.String("output"))
	fmt.Print(formatter.FormatRefresh(refresh))
	return nil
}

func isAPIError(err error) bool {
	_, ok := err.(*bgctl.APIError)
	return ok
}
