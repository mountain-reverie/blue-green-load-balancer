package bgctl

import "time"

// StatusResponse represents the response from GET /api/status.
type StatusResponse struct {
	ActiveService string        `json:"active_service"`
	Blue          ServiceStatus `json:"blue"`
	Green         ServiceStatus `json:"green"`
	LastSwitch    time.Time     `json:"last_switch"`
	SwitchCount   int64         `json:"switch_count"`
	Git           GitStatus     `json:"git"`
}

// ServiceStatus represents the status of a backend service.
type ServiceStatus struct {
	URL        string        `json:"url"`
	HealthPath string        `json:"health_path"`
	Healthy    bool          `json:"healthy"`
	LastCheck  time.Time     `json:"last_check"`
	Latency    time.Duration `json:"latency"`
	Error      string        `json:"error,omitempty"`
}

// GitStatus represents git repository status.
type GitStatus struct {
	LastFetch    time.Time `json:"last_fetch"`
	LastTag      string    `json:"last_tag,omitempty"`
	BlueCommit   string    `json:"blue_commit,omitempty"`
	GreenCommit  string    `json:"green_commit,omitempty"`
	ActiveCommit string    `json:"active_commit,omitempty"`
	Error        string    `json:"error,omitempty"`
}

// MetricsResponse represents the response from GET /api/metrics.
type MetricsResponse struct {
	Requests          RequestMetrics `json:"requests"`
	Latency           LatencyMetrics `json:"latency"`
	Errors            ErrorMetrics   `json:"errors"`
	ActiveConnections int64          `json:"active_connections"`
	UptimeSeconds     int64          `json:"uptime_seconds"`
}

// RequestMetrics contains request statistics.
type RequestMetrics struct {
	Total     int64   `json:"total"`
	PerSecond float64 `json:"per_second"`
	Blue      int64   `json:"blue"`
	Green     int64   `json:"green"`
}

// LatencyMetrics contains latency statistics.
type LatencyMetrics struct {
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
	P99 float64 `json:"p99"`
	Avg float64 `json:"avg"`
}

// ErrorMetrics contains error statistics.
type ErrorMetrics struct {
	Total     int64   `json:"total"`
	Rate      float64 `json:"rate"`
	LastError string  `json:"last_error,omitempty"`
}

// RefreshResponse represents the response from POST /api/webhook/refresh.
type RefreshResponse struct {
	Refreshed    bool      `json:"refreshed"`
	Error        string    `json:"error,omitempty"`
	BlueCommit   string    `json:"blue_commit,omitempty"`
	GreenCommit  string    `json:"green_commit,omitempty"`
	ActiveCommit string    `json:"active_commit,omitempty"`
	LastFetch    time.Time `json:"last_fetch"`
}
