package proxy

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/config"
)

// Proxy is a reverse proxy that can switch between blue and green backends.
type Proxy struct {
	cfg     *config.Config
	logger  *slog.Logger
	drainer *Drainer

	// Current active backend
	activeTarget atomic.Pointer[config.ServiceTarget]

	// Reverse proxies for each backend
	blueProxy  *httputil.ReverseProxy
	greenProxy *httputil.ReverseProxy

	// Metrics callbacks
	onRequest func(target config.ServiceTarget, statusCode int, duration time.Duration)
	onError   func(target config.ServiceTarget, err error)
}

// ProxyOption configures the proxy.
type ProxyOption func(*Proxy)

// WithRequestCallback sets a callback for completed requests.
func WithRequestCallback(fn func(target config.ServiceTarget, statusCode int, duration time.Duration)) ProxyOption {
	return func(p *Proxy) {
		p.onRequest = fn
	}
}

// WithErrorCallback sets a callback for proxy errors.
func WithErrorCallback(fn func(target config.ServiceTarget, err error)) ProxyOption {
	return func(p *Proxy) {
		p.onError = fn
	}
}

// New creates a new proxy instance.
func New(cfg *config.Config, logger *slog.Logger, opts ...ProxyOption) (*Proxy, error) {
	blueURL, err := url.Parse(cfg.Services.Blue.URL)
	if err != nil {
		return nil, fmt.Errorf("parsing blue URL: %w", err)
	}

	greenURL, err := url.Parse(cfg.Services.Green.URL)
	if err != nil {
		return nil, fmt.Errorf("parsing green URL: %w", err)
	}

	p := &Proxy{
		cfg:     cfg,
		logger:  logger,
		drainer: NewDrainer(),
	}

	// Apply options
	for _, opt := range opts {
		opt(p)
	}

	// Create reverse proxies
	p.blueProxy = p.createReverseProxy(blueURL, config.ServiceBlue)
	p.greenProxy = p.createReverseProxy(greenURL, config.ServiceGreen)

	// Set initial target to blue
	initialTarget := config.ServiceBlue
	p.activeTarget.Store(&initialTarget)

	return p, nil
}

// createReverseProxy creates a configured reverse proxy for a backend.
func (p *Proxy) createReverseProxy(target *url.URL, service config.ServiceTarget) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Custom director to preserve the original host header option
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Header.Set("X-Forwarded-Host", req.Host)
		req.Header.Set("X-Backend", string(service))
	}

	// Error handler
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		p.logger.Error("proxy error",
			"target", service,
			"error", err,
			"path", r.URL.Path,
		)
		if p.onError != nil {
			p.onError(service, err)
		}
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	// Modify response to capture status code
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("X-Backend", string(service))
		return nil
	}

	return proxy
}

// ServeHTTP implements http.Handler.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Atomically check draining and track connection.
	// This prevents the race between checking IsDraining() and adding to WaitGroup.
	release := p.drainer.Track()
	if release == nil {
		http.Error(w, "Service is draining", http.StatusServiceUnavailable)
		return
	}
	defer release()

	// Get current target
	target := p.ActiveTarget()

	// Wrap response writer to capture status code
	rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

	// Route to appropriate backend
	switch target {
	case config.ServiceBlue:
		p.blueProxy.ServeHTTP(rw, r)
	case config.ServiceGreen:
		p.greenProxy.ServeHTTP(rw, r)
	default:
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Call metrics callback
	if p.onRequest != nil {
		p.onRequest(target, rw.statusCode, time.Since(start))
	}
}

// ActiveTarget returns the current active backend target.
func (p *Proxy) ActiveTarget() config.ServiceTarget {
	target := p.activeTarget.Load()
	if target == nil {
		return config.ServiceBlue
	}
	return *target
}

// Switch changes the active backend to the specified target.
// It gracefully drains existing connections before switching.
func (p *Proxy) Switch(ctx context.Context, target config.ServiceTarget) error {
	current := p.ActiveTarget()
	if current == target {
		p.logger.Info("already on target", "target", target)
		return nil
	}

	p.logger.Info("starting switch",
		"from", current,
		"to", target,
	)

	// Start draining
	if err := p.drainer.Drain(ctx, p.cfg.Proxy.DrainTimeout); err != nil {
		p.logger.Warn("drain timeout reached, switching anyway",
			"error", err,
			"active_connections", p.drainer.ActiveConnections(),
		)
	}

	// Switch target
	p.activeTarget.Store(&target)

	// Reset drainer for next switch
	p.drainer.Reset()

	p.logger.Info("switch complete",
		"from", current,
		"to", target,
	)

	return nil
}

// Drainer returns the connection drainer.
func (p *Proxy) Drainer() *Drainer {
	return p.drainer
}

// ActiveConnections returns the current number of active connections.
func (p *Proxy) ActiveConnections() int64 {
	return p.drainer.ActiveConnections()
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.statusCode = http.StatusOK
		rw.written = true
	}
	return rw.ResponseWriter.Write(b)
}

// Flush implements http.Flusher.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker for websocket support.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("response writer does not support hijacking")
}
