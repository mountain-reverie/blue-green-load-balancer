// Package app provides the application server that can be used both in production and tests.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

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
	Config   *config.Config
	Logger   *slog.Logger
	Proxy    *proxy.Proxy
	Health   *health.Checker
	Switcher *switcher.Switcher
	Metrics  *metrics.Collector
	Admin    *admin.Server
	UI       *ui.Handlers

	// prometheusReg is stored to apply after Metrics is initialized
	prometheusReg prometheus.Registerer

	mu          sync.Mutex
	gitWatcher  *switcher.GitWatcher
	proxyServer *http.Server
	ctx         context.Context
	cancel      context.CancelFunc
	started     bool
}

// Option configures the Application.
type Option func(*Application)

// WithLogger sets the logger for the application.
func WithLogger(logger *slog.Logger) Option {
	return func(a *Application) {
		a.Logger = logger
	}
}

// WithPrometheusRegistry registers metrics with a Prometheus registry.
// The registration happens after all components are initialized.
func WithPrometheusRegistry(reg prometheus.Registerer) Option {
	return func(a *Application) {
		a.prometheusReg = reg
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

	// Register metrics with Prometheus if configured
	if app.prometheusReg != nil {
		if err := app.Metrics.Register(app.prometheusReg); err != nil {
			app.Logger.Warn("failed to register prometheus metrics", "error", err)
		}
	}

	return app, nil
}

// Start starts all application components.
// This method is not safe to call multiple times without calling Stop() first.
func (a *Application) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.started {
		return errors.New("application already started")
	}

	a.ctx, a.cancel = context.WithCancel(ctx)
	a.started = true

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
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}

	if a.proxyServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = a.proxyServer.Shutdown(ctx)
		cancel()
		a.proxyServer = nil
	}

	if a.gitWatcher != nil {
		a.gitWatcher.Stop()
		a.gitWatcher = nil
	}

	if a.UI != nil {
		a.UI.StopUpdates()
	}
	if a.Health != nil {
		a.Health.Stop()
	}
	if a.Admin != nil {
		_ = a.Admin.Stop()
	}

	a.started = false
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

// StartProxyServer starts the proxy server on the configured address.
// Returns a channel that receives any error from ListenAndServe (or nil on clean shutdown).
func (a *Application) StartProxyServer() <-chan error {
	errCh := make(chan error, 1)

	a.mu.Lock()
	a.proxyServer = &http.Server{
		Addr:         a.Config.Proxy.ListenAddr,
		Handler:      a.Proxy,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	a.mu.Unlock()

	go func() {
		a.Logger.Info("proxy server starting", "addr", a.Config.Proxy.ListenAddr)
		if err := a.proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		} else {
			errCh <- nil
		}
		close(errCh)
	}()

	return errCh
}
