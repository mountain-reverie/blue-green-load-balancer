package integration

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/config"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/health"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/proxy"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/switcher"
)

func TestProxyRouting(t *testing.T) {
	// Create mock backend servers
	blueServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "blue")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("blue response"))
	}))
	defer blueServer.Close()

	greenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "green")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("green response"))
	}))
	defer greenServer.Close()

	cfg := &config.Config{
		Services: config.ServiceConfig{
			Blue:  config.ServiceEndpoint{URL: blueServer.URL, HealthPath: "/health"},
			Green: config.ServiceEndpoint{URL: greenServer.URL, HealthPath: "/health"},
		},
		Proxy: config.ProxyConfig{
			ListenAddr:   ":8080",
			DrainTimeout: 5 * time.Second,
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := proxy.New(cfg, logger)
	require.NoError(t, err)

	// Test initial routing (should go to blue)
	t.Run("initial routing to blue", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()

		p.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "blue", rec.Header().Get("X-Backend"))
		assert.Equal(t, "blue response", rec.Body.String())
	})

	// Switch to green
	ctx := context.Background()
	err = p.Switch(ctx, config.ServiceGreen)
	require.NoError(t, err)

	// Test routing after switch
	t.Run("routing after switch to green", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()

		p.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "green", rec.Header().Get("X-Backend"))
		assert.Equal(t, "green response", rec.Body.String())
	})

	// Switch back to blue
	err = p.Switch(ctx, config.ServiceBlue)
	require.NoError(t, err)

	t.Run("routing after switch back to blue", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()

		p.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "blue", rec.Header().Get("X-Backend"))
	})
}

func TestProxyErrorHandling(t *testing.T) {
	cfg := &config.Config{
		Services: config.ServiceConfig{
			Blue:  config.ServiceEndpoint{URL: "http://localhost:59999", HealthPath: "/health"},
			Green: config.ServiceEndpoint{URL: "http://localhost:59998", HealthPath: "/health"},
		},
		Proxy: config.ProxyConfig{
			ListenAddr:   ":8080",
			DrainTimeout: 5 * time.Second,
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := proxy.New(cfg, logger)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	p.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestSwitcherWithHealthCheck(t *testing.T) {
	// Create mock backend servers
	var blueHealthy atomic.Bool
	blueHealthy.Store(true)
	blueServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			if blueHealthy.Load() {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		_, _ = w.Write([]byte("blue"))
	}))
	defer blueServer.Close()

	greenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte("green"))
	}))
	defer greenServer.Close()

	cfg := &config.Config{
		Services: config.ServiceConfig{
			Blue:  config.ServiceEndpoint{URL: blueServer.URL, HealthPath: "/health"},
			Green: config.ServiceEndpoint{URL: greenServer.URL, HealthPath: "/health"},
		},
		Proxy: config.ProxyConfig{
			ListenAddr:   ":8080",
			DrainTimeout: 5 * time.Second,
		},
		Health: config.HealthConfig{
			Interval: 100 * time.Millisecond,
			Timeout:  50 * time.Millisecond,
		},
	}

	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	p, err := proxy.New(cfg, logger)
	require.NoError(t, err)

	healthChecker := health.NewChecker(cfg, logger)
	healthChecker.Start(ctx)
	defer healthChecker.Stop()

	// Wait for initial health checks
	time.Sleep(200 * time.Millisecond)

	sw := switcher.NewSwitcher(cfg, logger, p, healthChecker)

	// Test switch to green (should succeed as green is healthy)
	t.Run("switch to healthy green", func(t *testing.T) {
		err := sw.Switch(ctx, config.ServiceGreen, switcher.TriggerGitTag)
		assert.NoError(t, err)
		assert.Equal(t, config.ServiceGreen, sw.ActiveTarget())
	})

	// Test switch to unhealthy blue (should fail)
	t.Run("switch to unhealthy blue", func(t *testing.T) {
		blueHealthy.Store(false)
		time.Sleep(200 * time.Millisecond) // Wait for health check

		err := sw.Switch(ctx, config.ServiceBlue, switcher.TriggerGitTag)
		assert.Error(t, err)
		assert.Equal(t, config.ServiceGreen, sw.ActiveTarget()) // Should still be green
	})

	// Make blue healthy again and switch
	t.Run("switch to recovered blue", func(t *testing.T) {
		blueHealthy.Store(true)
		time.Sleep(200 * time.Millisecond) // Wait for health check

		err := sw.Switch(ctx, config.ServiceBlue, switcher.TriggerGitTag)
		assert.NoError(t, err)
		assert.Equal(t, config.ServiceBlue, sw.ActiveTarget())
	})

	// Test no-op when target equals current (git tag hasn't changed)
	t.Run("no switch when target is already active", func(t *testing.T) {
		initialSwitchCount := sw.SwitchCount()

		// Try to switch to blue when already on blue
		err := sw.Switch(ctx, config.ServiceBlue, switcher.TriggerGitTag)
		assert.NoError(t, err, "switch to same target should not error")
		assert.Equal(t, config.ServiceBlue, sw.ActiveTarget(), "should remain on blue")
		assert.Equal(t, initialSwitchCount, sw.SwitchCount(), "switch count should not increment for no-op")
	})
}

func TestConcurrentRequests(t *testing.T) {
	var requestCount atomic.Int64
	blueServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		time.Sleep(10 * time.Millisecond) // Simulate some work
		_, _ = w.Write([]byte("blue"))
	}))
	defer blueServer.Close()

	greenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("green"))
	}))
	defer greenServer.Close()

	cfg := &config.Config{
		Services: config.ServiceConfig{
			Blue:  config.ServiceEndpoint{URL: blueServer.URL, HealthPath: "/health"},
			Green: config.ServiceEndpoint{URL: greenServer.URL, HealthPath: "/health"},
		},
		Proxy: config.ProxyConfig{
			ListenAddr:   ":8080",
			DrainTimeout: 5 * time.Second,
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := proxy.New(cfg, logger)
	require.NoError(t, err)

	// Start concurrent requests
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)
			done <- struct{}{}
		}()
	}

	// Wait for all requests
	for i := 0; i < 10; i++ {
		<-done
	}

	assert.Equal(t, int64(10), requestCount.Load())
}
