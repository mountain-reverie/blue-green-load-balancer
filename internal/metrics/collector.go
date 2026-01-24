package metrics

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/config"
)

// Collector collects and manages metrics for the load balancer.
type Collector struct {
	startTime time.Time
	store     *Store

	// Counters
	totalRequests atomic.Int64
	blueRequests  atomic.Int64
	greenRequests atomic.Int64
	totalErrors   atomic.Int64

	// Current values
	activeConnections atomic.Int64

	// Last error
	mu        sync.RWMutex
	lastError string

	// Prometheus metrics
	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	errorsTotal     *prometheus.CounterVec
	activeConns     prometheus.Gauge
	switchTotal     *prometheus.CounterVec
}

// NewCollector creates a new metrics collector.
func NewCollector(cfg *config.Config) *Collector {
	c := &Collector{
		startTime: time.Now(),
		store:     NewStore(cfg.Metrics.HistoryDuration, cfg.Metrics.HistoryResolution),
	}

	c.initPrometheusMetrics()
	return c
}

// initPrometheusMetrics initializes Prometheus metrics.
func (c *Collector) initPrometheusMetrics() {
	c.requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bluegreen_requests_total",
			Help: "Total number of requests processed",
		},
		[]string{"backend", "status"},
	)

	c.requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "bluegreen_request_duration_seconds",
			Help:    "Request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"backend"},
	)

	c.errorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bluegreen_errors_total",
			Help: "Total number of errors",
		},
		[]string{"backend", "type"},
	)

	c.activeConns = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "bluegreen_active_connections",
			Help: "Number of active connections",
		},
	)

	c.switchTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bluegreen_switches_total",
			Help: "Total number of backend switches",
		},
		[]string{"from", "to", "trigger"},
	)
}

// Register registers all Prometheus metrics with the given registry.
func (c *Collector) Register(reg prometheus.Registerer) error {
	collectors := []prometheus.Collector{
		c.requestsTotal,
		c.requestDuration,
		c.errorsTotal,
		c.activeConns,
		c.switchTotal,
	}

	for _, col := range collectors {
		if err := reg.Register(col); err != nil {
			return err
		}
	}
	return nil
}

// RecordRequest records a completed request.
func (c *Collector) RecordRequest(target config.ServiceTarget, statusCode int, duration time.Duration) {
	c.totalRequests.Add(1)

	switch target {
	case config.ServiceBlue:
		c.blueRequests.Add(1)
	case config.ServiceGreen:
		c.greenRequests.Add(1)
	}

	// Prometheus metrics
	status := "success"
	if statusCode >= 400 {
		status = "error"
	}
	c.requestsTotal.WithLabelValues(string(target), status).Inc()
	c.requestDuration.WithLabelValues(string(target)).Observe(duration.Seconds())

	// Store for history
	c.store.RecordRequest(duration)
}

// RecordError records an error.
func (c *Collector) RecordError(target config.ServiceTarget, err error) {
	c.totalErrors.Add(1)

	c.mu.Lock()
	c.lastError = err.Error()
	c.mu.Unlock()

	c.errorsTotal.WithLabelValues(string(target), "proxy").Inc()
	c.store.RecordError()
}

// RecordSwitch records a backend switch.
func (c *Collector) RecordSwitch(from, to config.ServiceTarget, trigger string) {
	c.switchTotal.WithLabelValues(string(from), string(to), trigger).Inc()
}

// SetActiveConnections updates the active connection count.
func (c *Collector) SetActiveConnections(count int64) {
	c.activeConnections.Store(count)
	c.activeConns.Set(float64(count))
}

// IncrementConnections increments the active connection count.
func (c *Collector) IncrementConnections() {
	c.activeConnections.Add(1)
	c.activeConns.Inc()
}

// DecrementConnections decrements the active connection count.
func (c *Collector) DecrementConnections() {
	c.activeConnections.Add(-1)
	c.activeConns.Dec()
}

// Snapshot contains a point-in-time view of all metrics.
type Snapshot struct {
	TotalRequests     int64
	BlueRequests      int64
	GreenRequests     int64
	RequestsPerSecond float64
	TotalErrors       int64
	ErrorRate         float64
	ActiveConnections int64
	UptimeSeconds     int64
	LatencyP50        float64
	LatencyP90        float64
	LatencyP99        float64
	LatencyAvg        float64
	LastError         string
}

// Snapshot returns a point-in-time snapshot of all metrics.
func (c *Collector) Snapshot() Snapshot {
	c.mu.RLock()
	lastError := c.lastError
	c.mu.RUnlock()

	total := c.totalRequests.Load()
	uptime := time.Since(c.startTime).Seconds()
	rps := float64(total) / uptime
	if uptime < 1 {
		rps = float64(total)
	}

	errors := c.totalErrors.Load()
	var errorRate float64
	if total > 0 {
		errorRate = float64(errors) / float64(total)
	}

	latencies := c.store.GetLatencyPercentiles()

	return Snapshot{
		TotalRequests:     total,
		BlueRequests:      c.blueRequests.Load(),
		GreenRequests:     c.greenRequests.Load(),
		RequestsPerSecond: rps,
		TotalErrors:       errors,
		ErrorRate:         errorRate,
		ActiveConnections: c.activeConnections.Load(),
		UptimeSeconds:     int64(uptime),
		LatencyP50:        latencies.P50,
		LatencyP90:        latencies.P90,
		LatencyP99:        latencies.P99,
		LatencyAvg:        latencies.Avg,
		LastError:         lastError,
	}
}

// History returns historical data points.
func (c *Collector) History(duration, resolution time.Duration) []DataPoint {
	return c.store.GetHistory(duration, resolution)
}

// Store returns the underlying metrics store.
func (c *Collector) Store() *Store {
	return c.store
}
