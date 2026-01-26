package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/config"
)

// setupBenchmarkProxy creates a proxy with mock backends for benchmarking.
func setupBenchmarkProxy(b *testing.B, blueLatency, greenLatency time.Duration) (*Proxy, func()) {
	b.Helper()

	// Create mock blue backend
	blueServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if blueLatency > 0 {
			time.Sleep(blueLatency)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("blue"))
	}))

	// Create mock green backend
	greenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if greenLatency > 0 {
			time.Sleep(greenLatency)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("green"))
	}))

	cfg := &config.Config{
		Services: config.ServiceConfig{
			Blue: config.ServiceEndpoint{
				URL:        blueServer.URL,
				HealthPath: "/health",
			},
			Green: config.ServiceEndpoint{
				URL:        greenServer.URL,
				HealthPath: "/health",
			},
		},
		Proxy: config.ProxyConfig{
			ListenAddr:   ":8080",
			DrainTimeout: 5 * time.Second,
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	proxy, err := New(cfg, logger)
	if err != nil {
		b.Fatalf("failed to create proxy: %v", err)
	}

	cleanup := func() {
		blueServer.Close()
		greenServer.Close()
	}

	return proxy, cleanup
}

// BenchmarkProxyRouting measures the overhead of routing requests through the proxy.
func BenchmarkProxyRouting(b *testing.B) {
	proxy, cleanup := setupBenchmarkProxy(b, 0, 0)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/", nil)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("unexpected status code: %d", rec.Code)
		}
	}
}

// BenchmarkProxyRoutingWithCallback measures routing with metrics callback enabled.
func BenchmarkProxyRoutingWithCallback(b *testing.B) {
	blueServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("blue"))
	}))
	defer blueServer.Close()

	greenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("green"))
	}))
	defer greenServer.Close()

	cfg := &config.Config{
		Services: config.ServiceConfig{
			Blue:  config.ServiceEndpoint{URL: blueServer.URL, HealthPath: "/health"},
			Green: config.ServiceEndpoint{URL: greenServer.URL, HealthPath: "/health"},
		},
		Proxy: config.ProxyConfig{ListenAddr: ":8080", DrainTimeout: 5 * time.Second},
	}

	var requestCount atomic.Int64
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	proxy, err := New(cfg, logger,
		WithRequestCallback(func(target config.ServiceTarget, statusCode int, duration time.Duration) {
			requestCount.Add(1)
		}),
	)
	if err != nil {
		b.Fatalf("failed to create proxy: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
	}

	b.StopTimer()
	if requestCount.Load() != int64(b.N) {
		b.Errorf("callback count mismatch: got %d, want %d", requestCount.Load(), b.N)
	}
}

// BenchmarkProxyConcurrent measures proxy performance under concurrent load.
func BenchmarkProxyConcurrent(b *testing.B) {
	concurrencyLevels := []int{1, 10, 100, 1000}

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("concurrency-%d", concurrency), func(b *testing.B) {
			proxy, cleanup := setupBenchmarkProxy(b, 0, 0)
			defer cleanup()

			b.SetParallelism(concurrency)
			b.ResetTimer()
			b.ReportAllocs()

			b.RunParallel(func(pb *testing.PB) {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				for pb.Next() {
					rec := httptest.NewRecorder()
					proxy.ServeHTTP(rec, req)
				}
			})
		})
	}
}

// BenchmarkProxyWithLatency measures proxy overhead with simulated backend latency.
func BenchmarkProxyWithLatency(b *testing.B) {
	latencies := []time.Duration{0, 100 * time.Microsecond, 1 * time.Millisecond}

	for _, latency := range latencies {
		b.Run(fmt.Sprintf("latency-%v", latency), func(b *testing.B) {
			proxy, cleanup := setupBenchmarkProxy(b, latency, latency)
			defer cleanup()

			req := httptest.NewRequest(http.MethodGet, "/", nil)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				rec := httptest.NewRecorder()
				proxy.ServeHTTP(rec, req)
			}
		})
	}
}

// BenchmarkActiveTargetRead measures the cost of reading the active target atomically.
func BenchmarkActiveTargetRead(b *testing.B) {
	proxy, cleanup := setupBenchmarkProxy(b, 0, 0)
	defer cleanup()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = proxy.ActiveTarget()
	}
}

// BenchmarkActiveTargetReadConcurrent measures atomic target read under contention.
func BenchmarkActiveTargetReadConcurrent(b *testing.B) {
	proxy, cleanup := setupBenchmarkProxy(b, 0, 0)
	defer cleanup()

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = proxy.ActiveTarget()
		}
	})
}

// BenchmarkConnectionTracking measures the overhead of connection tracking.
func BenchmarkConnectionTracking(b *testing.B) {
	drainer := NewDrainer()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		drainer.activeConnections.Add(1)
		drainer.wg.Add(1)
		drainer.activeConnections.Add(-1)
		drainer.wg.Done()
	}
}

// BenchmarkConnectionTrackingConcurrent measures connection tracking under concurrent load.
func BenchmarkConnectionTrackingConcurrent(b *testing.B) {
	drainer := NewDrainer()

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			drainer.activeConnections.Add(1)
			drainer.wg.Add(1)
			drainer.activeConnections.Add(-1)
			drainer.wg.Done()
		}
	})
}

// BenchmarkIsDraining measures the cost of checking drain status.
func BenchmarkIsDraining(b *testing.B) {
	drainer := NewDrainer()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = drainer.IsDraining()
	}
}

// BenchmarkResponseWriter measures the overhead of the response writer wrapper.
func BenchmarkResponseWriter(b *testing.B) {
	responseSizes := []int{0, 1024, 65536, 1048576} // 0, 1KB, 64KB, 1MB

	for _, size := range responseSizes {
		b.Run(fmt.Sprintf("size-%d", size), func(b *testing.B) {
			data := make([]byte, size)
			for i := range data {
				data[i] = 'x'
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				rec := httptest.NewRecorder()
				rw := &responseWriter{ResponseWriter: rec, statusCode: http.StatusOK}
				rw.WriteHeader(http.StatusOK)
				_, _ = rw.Write(data)
			}
		})
	}
}

// BenchmarkSwitch measures the time to switch backends with no active connections.
func BenchmarkSwitch(b *testing.B) {
	proxy, cleanup := setupBenchmarkProxy(b, 0, 0)
	defer cleanup()

	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		target := config.ServiceBlue
		if i%2 == 0 {
			target = config.ServiceGreen
		}
		_ = proxy.Switch(ctx, target)
	}
}

// BenchmarkSwitchDuringLoad measures switch performance with concurrent requests.
func BenchmarkSwitchDuringLoad(b *testing.B) {
	concurrencyLevels := []int{10, 100, 500}

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("concurrent-%d", concurrency), func(b *testing.B) {
			proxy, cleanup := setupBenchmarkProxy(b, 1*time.Millisecond, 1*time.Millisecond)
			defer cleanup()

			ctx := context.Background()
			var wg sync.WaitGroup
			stopCh := make(chan struct{})

			// Start concurrent request workers
			var requestCount atomic.Int64
			for i := 0; i < concurrency; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					req := httptest.NewRequest(http.MethodGet, "/", nil)
					for {
						select {
						case <-stopCh:
							return
						default:
							rec := httptest.NewRecorder()
							proxy.ServeHTTP(rec, req)
							requestCount.Add(1)
						}
					}
				}()
			}

			// Let workers start
			time.Sleep(10 * time.Millisecond)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				target := config.ServiceBlue
				if i%2 == 0 {
					target = config.ServiceGreen
				}
				_ = proxy.Switch(ctx, target)
			}

			b.StopTimer()
			close(stopCh)
			wg.Wait()

			b.ReportMetric(float64(requestCount.Load())/float64(b.N), "requests/switch")
		})
	}
}

// BenchmarkSwitchLatency measures detailed switch timing including drain phase.
func BenchmarkSwitchLatency(b *testing.B) {
	drainTimeouts := []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 1 * time.Second}

	for _, timeout := range drainTimeouts {
		b.Run(fmt.Sprintf("drain-%v", timeout), func(b *testing.B) {
			blueServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(10 * time.Millisecond) // Simulate work
				w.WriteHeader(http.StatusOK)
			}))
			defer blueServer.Close()

			greenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(10 * time.Millisecond) // Simulate work
				w.WriteHeader(http.StatusOK)
			}))
			defer greenServer.Close()

			cfg := &config.Config{
				Services: config.ServiceConfig{
					Blue:  config.ServiceEndpoint{URL: blueServer.URL, HealthPath: "/health"},
					Green: config.ServiceEndpoint{URL: greenServer.URL, HealthPath: "/health"},
				},
				Proxy: config.ProxyConfig{ListenAddr: ":8080", DrainTimeout: timeout},
			}

			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			proxy, err := New(cfg, logger)
			if err != nil {
				b.Fatalf("failed to create proxy: %v", err)
			}

			ctx := context.Background()
			var wg sync.WaitGroup
			stopCh := make(chan struct{})

			// Start some concurrent requests to simulate load
			for i := 0; i < 10; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					req := httptest.NewRequest(http.MethodGet, "/", nil)
					for {
						select {
						case <-stopCh:
							return
						default:
							rec := httptest.NewRecorder()
							proxy.ServeHTTP(rec, req)
						}
					}
				}()
			}

			// Let workers start
			time.Sleep(50 * time.Millisecond)

			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				target := config.ServiceBlue
				if i%2 == 0 {
					target = config.ServiceGreen
				}
				start := time.Now()
				_ = proxy.Switch(ctx, target)
				b.ReportMetric(float64(time.Since(start).Microseconds()), "switch-μs")
			}

			b.StopTimer()
			close(stopCh)
			wg.Wait()
		})
	}
}

// BenchmarkDrainWithActiveConnections measures drain time with varying connection counts.
func BenchmarkDrainWithActiveConnections(b *testing.B) {
	connectionCounts := []int{0, 10, 100, 1000}

	for _, count := range connectionCounts {
		b.Run(fmt.Sprintf("connections-%d", count), func(b *testing.B) {
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				drainer := NewDrainer()
				var wg sync.WaitGroup

				// Simulate active connections that complete quickly
				for j := 0; j < count; j++ {
					drainer.activeConnections.Add(1)
					drainer.wg.Add(1)
					wg.Add(1)
					go func() {
						defer wg.Done()
						time.Sleep(1 * time.Millisecond) // Simulate request processing
						drainer.activeConnections.Add(-1)
						drainer.wg.Done()
					}()
				}

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				start := time.Now()
				_ = drainer.Drain(ctx, 5*time.Second)
				b.ReportMetric(float64(time.Since(start).Microseconds()), "drain-μs")
				cancel()
				wg.Wait()
			}
		})
	}
}

// BenchmarkFullRequestCycle measures a complete request cycle including all overhead.
func BenchmarkFullRequestCycle(b *testing.B) {
	blueServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","backend":"blue"}`))
	}))
	defer blueServer.Close()

	greenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","backend":"green"}`))
	}))
	defer greenServer.Close()

	cfg := &config.Config{
		Services: config.ServiceConfig{
			Blue:  config.ServiceEndpoint{URL: blueServer.URL, HealthPath: "/health"},
			Green: config.ServiceEndpoint{URL: greenServer.URL, HealthPath: "/health"},
		},
		Proxy: config.ProxyConfig{ListenAddr: ":8080", DrainTimeout: 5 * time.Second},
	}

	var totalRequests atomic.Int64
	var totalDuration atomic.Int64

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	proxy, err := New(cfg, logger,
		WithRequestCallback(func(target config.ServiceTarget, statusCode int, duration time.Duration) {
			totalRequests.Add(1)
			totalDuration.Add(int64(duration))
		}),
		WithErrorCallback(func(target config.ServiceTarget, err error) {
			// Track errors if needed
		}),
	)
	if err != nil {
		b.Fatalf("failed to create proxy: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "benchmark/1.0")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("unexpected status: %d", rec.Code)
		}
	}

	b.StopTimer()
	if totalRequests.Load() > 0 {
		avgDuration := time.Duration(totalDuration.Load() / totalRequests.Load())
		b.ReportMetric(float64(avgDuration.Microseconds()), "avg-μs")
	}
}

// BenchmarkSwitchWithMetrics measures switch performance with full metrics tracking.
func BenchmarkSwitchWithMetrics(b *testing.B) {
	blueServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer blueServer.Close()

	greenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer greenServer.Close()

	cfg := &config.Config{
		Services: config.ServiceConfig{
			Blue:  config.ServiceEndpoint{URL: blueServer.URL, HealthPath: "/health"},
			Green: config.ServiceEndpoint{URL: greenServer.URL, HealthPath: "/health"},
		},
		Proxy: config.ProxyConfig{ListenAddr: ":8080", DrainTimeout: 100 * time.Millisecond},
	}

	var requestsBlue, requestsGreen atomic.Int64
	var switchCount atomic.Int64

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	proxy, err := New(cfg, logger,
		WithRequestCallback(func(target config.ServiceTarget, statusCode int, duration time.Duration) {
			if target == config.ServiceBlue {
				requestsBlue.Add(1)
			} else {
				requestsGreen.Add(1)
			}
		}),
	)
	if err != nil {
		b.Fatalf("failed to create proxy: %v", err)
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	stopCh := make(chan struct{})

	// Start concurrent request workers
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for {
				select {
				case <-stopCh:
					return
				default:
					rec := httptest.NewRecorder()
					proxy.ServeHTTP(rec, req)
				}
			}
		}()
	}

	// Let workers start
	time.Sleep(20 * time.Millisecond)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		target := config.ServiceBlue
		if i%2 == 0 {
			target = config.ServiceGreen
		}
		_ = proxy.Switch(ctx, target)
		switchCount.Add(1)
	}

	b.StopTimer()
	close(stopCh)
	wg.Wait()

	totalRequests := requestsBlue.Load() + requestsGreen.Load()
	b.ReportMetric(float64(totalRequests)/float64(switchCount.Load()), "requests/switch")
	b.ReportMetric(float64(requestsBlue.Load()), "blue-requests")
	b.ReportMetric(float64(requestsGreen.Load()), "green-requests")
}
