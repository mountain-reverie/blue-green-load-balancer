// Package app provides the application server that can be used both in production and tests.
package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/admin"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/config"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/health"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/metrics"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/proxy"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/switcher"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/ui"
)

// Application encapsulates all server components and provides a unified interface
// for running the blue/green load balancer. It can be used in production (main.go)
// or in integration tests.
type Application struct {
	Config  *config.Config
	Logger  *slog.Logger
	Proxy   *proxy.Proxy
	Health  *health.Checker
	Switcher *switcher.Switcher
	Metrics *metrics.Collector
	Admin   *admin.Server
	UI      *ui.Handlers

	gitWatcher *switcher.GitWatcher
	ctx        context.Context
	cancel     context.CancelFunc
}

// Option configures the Application.
type Option func(*Application)

// WithLogger sets the logger for the application.
func WithLogger(logger *slog.Logger) Option {
	return func(a *Application) {
		a.Logger = logger
	}
}

// WithPrometheusRegistry registers metrics with a custom Prometheus registry.
func WithPrometheusRegistry(reg prometheus.Registerer) Option {
	return func(a *Application) {
		if a.Metrics != nil && reg != nil {
			a.Metrics.Register(reg)
		}
	}
}

// New creates a new Application with all components initialized.
func New(cfg *config.Config, opts ...Option) (*Application, error) {
	app := &Application{
		Config: cfg,
		Logger: slog.Default(),
	}

	// Apply options first to get logger
	for _, opt := range opts {
		opt(app)
	}

	var err error

	// Initialize metrics collector
	app.Metrics = metrics.NewCollector(cfg)

	// Initialize reverse proxy with callbacks
	app.Proxy, err = proxy.New(cfg, app.Logger,
		proxy.WithRequestCallback(func(target config.ServiceTarget, statusCode int, duration time.Duration) {
			app.Metrics.RecordRequest(target, statusCode, duration)
		}),
		proxy.WithErrorCallback(func(target config.ServiceTarget, err error) {
			app.Metrics.RecordError(target, err)
		}),
	)
	if err != nil {
		return nil, err
	}

	// Initialize health checker
	app.Health = health.NewChecker(cfg, app.Logger)

	// Initialize switcher
	app.Switcher = switcher.NewSwitcher(cfg, app.Logger, app.Proxy, app.Health,
		switcher.WithSwitchCallback(func(event switcher.SwitchEvent) {
			app.Metrics.RecordSwitch(event.From, event.To, string(event.Trigger))
		}),
	)

	// Initialize admin server (includes Huma API)
	app.Admin = admin.NewServer(cfg, app.Logger, app.Switcher, app.Metrics)

	// Initialize UI handlers and register routes
	app.UI = ui.NewHandlers(app.Switcher, app.Metrics)
	app.UI.RegisterRoutes(app.Admin.Router())

	// Add Prometheus metrics endpoint
	app.Admin.Router().Handle("/metrics", promhttp.Handler())

	return app, nil
}

// Start starts all application components.
func (a *Application) Start(ctx context.Context) error {
	a.ctx, a.cancel = context.WithCancel(ctx)

	// Start health checker
	a.Health.Start(a.ctx)

	// Start UI updates (SSE)
	a.UI.StartUpdates()

	// Initialize git watcher if configured
	if a.Config.Git.RepoURL != "" {
		a.gitWatcher = switcher.NewGitWatcher(a.Config, a.Logger,
			switcher.WithTagChangeCallback(a.Switcher.HandleTagChange),
		)
		a.Switcher.SetGitWatcher(a.gitWatcher)
		a.gitWatcher.Start(a.ctx)
	}

	return nil
}

// Stop stops all application components gracefully.
func (a *Application) Stop() {
	if a.cancel != nil {
		a.cancel()
	}

	if a.gitWatcher != nil {
		a.gitWatcher.Stop()
	}

	a.UI.StopUpdates()
	a.Health.Stop()
	a.Admin.Stop()
}

// Router returns the HTTP router for the admin/API server.
// Use this to create an httptest.Server for integration tests.
func (a *Application) Router() chi.Router {
	return a.Admin.Router()
}

// Handler returns the HTTP handler for the admin/API server.
func (a *Application) Handler() http.Handler {
	return a.Admin.Router()
}

// ProxyHandler returns the HTTP handler for the reverse proxy.
func (a *Application) ProxyHandler() http.Handler {
	return a.Proxy
}

// StartAdminServer starts the admin server on a local address.
// For Tailscale, use Admin.Start() directly.
func (a *Application) StartAdminServer(ctx context.Context, addr string) error {
	return a.Admin.LocalServer(ctx, addr)
}
