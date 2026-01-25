package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"tailscale.com/tsnet"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/config"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/metrics"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/switcher"
)

// Server is the admin server that runs on the Tailscale network.
type Server struct {
	cfg      *config.Config
	logger   *slog.Logger
	switcher *switcher.Switcher
	metrics  *metrics.Collector

	tsServer *tsnet.Server
	router   chi.Router
	api      huma.API

	mu       sync.Mutex
	listener net.Listener
}

// NewServer creates a new admin server.
func NewServer(
	cfg *config.Config,
	logger *slog.Logger,
	sw *switcher.Switcher,
	m *metrics.Collector,
) *Server {
	s := &Server{
		cfg:      cfg,
		logger:   logger,
		switcher: sw,
		metrics:  m,
	}

	s.setupRouter()
	return s
}

// setupRouter configures the HTTP router and Huma API.
func (s *Server) setupRouter() {
	s.router = chi.NewRouter()

	// Middleware
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.RealIP)
	s.router.Use(middleware.Recoverer)
	s.router.Use(middleware.Logger)

	// Create Huma API
	humaConfig := huma.DefaultConfig("Blue/Green Load Balancer Admin", "1.0.0")
	humaConfig.Info.Description = "Admin API for the Blue/Green Load Balancer"
	humaConfig.DocsPath = "/api/docs"
	humaConfig.OpenAPIPath = "/api/openapi"

	s.api = humachi.New(s.router, humaConfig)

	// Register API operations
	RegisterAPI(s.api, s)
	RegisterWebhookAPI(s.api, s, s.cfg.Admin.WebhookKey)
}

// Start starts the admin server on the Tailscale network.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Initialize tsnet server
	s.tsServer = &tsnet.Server{
		Hostname:  s.cfg.Admin.Hostname,
		Dir:       s.cfg.Admin.StateDir,
		Ephemeral: s.cfg.Admin.Ephemeral,
		Logf:      func(format string, args ...any) { s.logger.Debug(fmt.Sprintf(format, args...)) },
	}

	// Set custom control server URL if provided (e.g., Headscale)
	if s.cfg.Admin.ControlURL != "" {
		s.tsServer.ControlURL = s.cfg.Admin.ControlURL
	}

	// Set auth key if provided
	if s.cfg.Admin.AuthKey != "" {
		s.tsServer.AuthKey = s.cfg.Admin.AuthKey
	}

	// Start the Tailscale server
	if err := s.tsServer.Start(); err != nil {
		return fmt.Errorf("starting tsnet server: %w", err)
	}

	// Get listener on port 80
	ln, err := s.tsServer.Listen("tcp", ":80")
	if err != nil {
		_ = s.tsServer.Close()
		return fmt.Errorf("listening on tsnet: %w", err)
	}
	s.listener = ln

	// Start HTTP server
	go func() {
		server := &http.Server{
			Handler: s.router,
		}
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logger.Error("admin server error", "error", err)
		}
	}()

	s.logger.Info("admin server started",
		"hostname", s.cfg.Admin.Hostname,
	)

	return nil
}

// Stop stops the admin server.
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.tsServer != nil {
		_ = s.tsServer.Close()
	}
	return nil
}

// Router returns the HTTP router for adding additional routes.
func (s *Server) Router() chi.Router {
	return s.router
}

// API returns the Huma API for adding additional operations.
func (s *Server) API() huma.API {
	return s.api
}

// LocalServer starts a local HTTP server for development/testing.
// This bypasses Tailscale and listens on a local address.
func (s *Server) LocalServer(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	go func() {
		server := &http.Server{
			Handler: s.router,
		}
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logger.Error("local admin server error", "error", err)
		}
	}()

	s.logger.Info("local admin server started", "addr", addr)
	return nil
}
