package health

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/config"
)

// Status represents the health status of a service.
type Status struct {
	Healthy     bool          `json:"healthy"`
	LastCheck   time.Time     `json:"last_check"`
	LastHealthy time.Time     `json:"last_healthy,omitempty"`
	Latency     time.Duration `json:"latency"`
	Error       string        `json:"error,omitempty"`
}

// Checker performs periodic health checks on backend services.
type Checker struct {
	cfg    *config.Config
	logger *slog.Logger
	client *http.Client

	mu     sync.RWMutex
	status map[config.ServiceTarget]*Status

	onStatusChange func(target config.ServiceTarget, status Status)

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// CheckerOption configures the health checker.
type CheckerOption func(*Checker)

// WithStatusChangeCallback sets a callback for status changes.
func WithStatusChangeCallback(fn func(target config.ServiceTarget, status Status)) CheckerOption {
	return func(c *Checker) {
		c.onStatusChange = fn
	}
}

// NewChecker creates a new health checker.
func NewChecker(cfg *config.Config, logger *slog.Logger, opts ...CheckerOption) *Checker {
	c := &Checker{
		cfg:    cfg,
		logger: logger,
		client: &http.Client{
			Timeout: cfg.Health.Timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		status: make(map[config.ServiceTarget]*Status),
	}

	// Initialize status for both services
	c.status[config.ServiceBlue] = &Status{}
	c.status[config.ServiceGreen] = &Status{}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Start begins the health check loop.
func (c *Checker) Start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)

	c.wg.Add(2)
	go c.checkLoop(ctx, config.ServiceBlue)
	go c.checkLoop(ctx, config.ServiceGreen)
}

// Stop stops the health check loop.
func (c *Checker) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
}

// checkLoop runs the health check loop for a single service.
func (c *Checker) checkLoop(ctx context.Context, target config.ServiceTarget) {
	defer c.wg.Done()

	ticker := time.NewTicker(c.cfg.Health.Interval)
	defer ticker.Stop()

	// Initial check
	c.check(ctx, target)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.check(ctx, target)
		}
	}
}

// check performs a single health check.
func (c *Checker) check(ctx context.Context, target config.ServiceTarget) {
	endpoint := c.cfg.GetServiceEndpoint(target)
	url := endpoint.URL + endpoint.HealthPath

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		c.updateStatus(target, Status{
			Healthy:   false,
			LastCheck: time.Now(),
			Latency:   time.Since(start),
			Error:     err.Error(),
		})
		return
	}

	resp, err := c.client.Do(req)
	latency := time.Since(start)

	if err != nil {
		c.updateStatus(target, Status{
			Healthy:   false,
			LastCheck: time.Now(),
			Latency:   latency,
			Error:     err.Error(),
		})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	healthy := resp.StatusCode >= 200 && resp.StatusCode < 300
	status := Status{
		Healthy:   healthy,
		LastCheck: time.Now(),
		Latency:   latency,
	}
	if healthy {
		status.LastHealthy = time.Now()
	}
	if !healthy {
		status.Error = http.StatusText(resp.StatusCode)
	}

	c.updateStatus(target, status)
}

// updateStatus updates the status for a service and triggers callbacks.
func (c *Checker) updateStatus(target config.ServiceTarget, status Status) {
	c.mu.Lock()
	prev := c.status[target]
	wasHealthy := prev != nil && prev.Healthy

	// Preserve last healthy time if still healthy or if we have previous data
	if prev != nil && !status.LastHealthy.IsZero() {
		status.LastHealthy = prev.LastHealthy
	}
	if status.Healthy {
		status.LastHealthy = status.LastCheck
	}

	c.status[target] = &status
	c.mu.Unlock()

	// Log status changes
	if wasHealthy != status.Healthy {
		if status.Healthy {
			c.logger.Info("service became healthy",
				"target", target,
				"latency", status.Latency,
			)
		} else {
			c.logger.Warn("service became unhealthy",
				"target", target,
				"error", status.Error,
			)
		}
	}

	if c.onStatusChange != nil {
		c.onStatusChange(target, status)
	}
}

// GetStatus returns the current health status for a service.
func (c *Checker) GetStatus(target config.ServiceTarget) Status {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if s, ok := c.status[target]; ok && s != nil {
		return *s
	}
	return Status{}
}

// GetAllStatus returns health status for all services.
func (c *Checker) GetAllStatus() map[config.ServiceTarget]Status {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[config.ServiceTarget]Status)
	for k, v := range c.status {
		if v != nil {
			result[k] = *v
		}
	}
	return result
}

// IsHealthy returns whether a specific service is healthy.
func (c *Checker) IsHealthy(target config.ServiceTarget) bool {
	return c.GetStatus(target).Healthy
}

// CheckNow performs an immediate health check for all services.
func (c *Checker) CheckNow(ctx context.Context) {
	c.check(ctx, config.ServiceBlue)
	c.check(ctx, config.ServiceGreen)
}
