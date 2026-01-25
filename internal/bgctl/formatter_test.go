package bgctl

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTextFormatterStatus(t *testing.T) {
	status := &StatusResponse{
		ActiveService: "blue",
		SwitchCount:   5,
		LastSwitch:    time.Date(2026, 1, 24, 10, 30, 0, 0, time.UTC),
		Blue: ServiceStatus{
			URL:        "http://localhost:3001",
			HealthPath: "/health",
			Healthy:    true,
			Latency:    5 * time.Millisecond,
		},
		Green: ServiceStatus{
			URL:        "http://localhost:3002",
			HealthPath: "/health",
			Healthy:    false,
			Error:      "connection refused",
			Latency:    0,
		},
		Git: GitStatus{
			BlueCommit:   "abc1234567890",
			GreenCommit:  "def5678901234",
			ActiveCommit: "abc1234567890",
			LastFetch:    time.Date(2026, 1, 24, 10, 30, 0, 0, time.UTC),
		},
	}

	formatter := NewFormatter("text")
	output := formatter.FormatStatus(status)

	// Verify key elements are present
	tests := []struct {
		name     string
		contains string
	}{
		{"active service", "Active Service: blue"},
		{"switch count", "Switch Count:   5"},
		{"blue healthy", "Blue : healthy"},
		{"green unhealthy", "Green: unhealthy"},
		{"blue url", "http://localhost:3001"},
		{"green url", "http://localhost:3002"},
		{"connection refused", "connection refused"},
		{"blue commit truncated", "abc1234"},
		{"green commit truncated", "def5678"},
		{"git section", "Git:"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(output, tc.contains) {
				t.Errorf("expected output to contain %q, got:\n%s", tc.contains, output)
			}
		})
	}
}

func TestTextFormatterMetrics(t *testing.T) {
	metrics := &MetricsResponse{
		Requests: RequestMetrics{
			Total:     10000,
			PerSecond: 15.5,
			Blue:      5000,
			Green:     5000,
		},
		Latency: LatencyMetrics{
			P50: 10.5,
			P90: 25.0,
			P99: 100.0,
			Avg: 15.2,
		},
		Errors: ErrorMetrics{
			Total:     10,
			Rate:      0.001,
			LastError: "timeout",
		},
		ActiveConnections: 42,
		UptimeSeconds:     86400,
	}

	formatter := NewFormatter("text")
	output := formatter.FormatMetrics(metrics)

	tests := []struct {
		name     string
		contains string
	}{
		{"requests section", "Requests:"},
		{"total requests", "Total:      10000"},
		{"per second", "Per Second: 15.50"},
		{"latency section", "Latency:"},
		{"p50", "P50: 10.50ms"},
		{"p99", "P99: 100.00ms"},
		{"errors section", "Errors:"},
		{"last error", "Last:  timeout"},
		{"active connections", "Active Connections: 42"},
		{"uptime", "Uptime: 1d 0h 0m"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(output, tc.contains) {
				t.Errorf("expected output to contain %q, got:\n%s", tc.contains, output)
			}
		})
	}
}

func TestTextFormatterRefresh(t *testing.T) {
	refresh := &RefreshResponse{
		Refreshed:    true,
		BlueCommit:   "abc1234567890",
		GreenCommit:  "def5678901234",
		ActiveCommit: "abc1234567890",
		LastFetch:    time.Date(2026, 1, 24, 10, 30, 0, 0, time.UTC),
	}

	formatter := NewFormatter("text")
	output := formatter.FormatRefresh(refresh)

	tests := []struct {
		name     string
		contains string
	}{
		{"refresh success", "Refresh: success"},
		{"blue commit", "Blue Commit:   abc1234"},
		{"green commit", "Green Commit:  def5678"},
		{"last fetch", "Last Fetch:"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(output, tc.contains) {
				t.Errorf("expected output to contain %q, got:\n%s", tc.contains, output)
			}
		})
	}
}

func TestTextFormatterRefreshFailed(t *testing.T) {
	refresh := &RefreshResponse{
		Refreshed: false,
		Error:     "git fetch failed",
	}

	formatter := NewFormatter("text")
	output := formatter.FormatRefresh(refresh)

	if !strings.Contains(output, "Refresh: failed") {
		t.Errorf("expected failed refresh message, got:\n%s", output)
	}
	if !strings.Contains(output, "git fetch failed") {
		t.Errorf("expected error message, got:\n%s", output)
	}
}

func TestJSONFormatterStatus(t *testing.T) {
	status := &StatusResponse{
		ActiveService: "blue",
		SwitchCount:   5,
		Blue: ServiceStatus{
			URL:     "http://localhost:3001",
			Healthy: true,
		},
		Green: ServiceStatus{
			URL:     "http://localhost:3002",
			Healthy: false,
		},
	}

	formatter := NewFormatter("json")
	output := formatter.FormatStatus(status)

	// Verify it's valid JSON
	var parsed StatusResponse
	err := json.Unmarshal([]byte(output), &parsed)
	if err != nil {
		t.Fatalf("invalid JSON output: %v\nOutput:\n%s", err, output)
	}

	// Verify values
	if parsed.ActiveService != "blue" {
		t.Errorf("expected active_service=blue, got %s", parsed.ActiveService)
	}
	if parsed.SwitchCount != 5 {
		t.Errorf("expected switch_count=5, got %d", parsed.SwitchCount)
	}
	if !parsed.Blue.Healthy {
		t.Errorf("expected blue.healthy=true")
	}
	if parsed.Green.Healthy {
		t.Errorf("expected green.healthy=false")
	}
}

func TestJSONFormatterMetrics(t *testing.T) {
	metrics := &MetricsResponse{
		Requests: RequestMetrics{
			Total: 1000,
		},
		ActiveConnections: 42,
		UptimeSeconds:     3600,
	}

	formatter := NewFormatter("json")
	output := formatter.FormatMetrics(metrics)

	// Verify it's valid JSON
	var parsed MetricsResponse
	err := json.Unmarshal([]byte(output), &parsed)
	if err != nil {
		t.Fatalf("invalid JSON output: %v\nOutput:\n%s", err, output)
	}

	if parsed.Requests.Total != 1000 {
		t.Errorf("expected requests.total=1000, got %d", parsed.Requests.Total)
	}
	if parsed.ActiveConnections != 42 {
		t.Errorf("expected active_connections=42, got %d", parsed.ActiveConnections)
	}
}

func TestJSONFormatterRefresh(t *testing.T) {
	refresh := &RefreshResponse{
		Refreshed:   true,
		BlueCommit:  "abc123",
		GreenCommit: "def456",
	}

	formatter := NewFormatter("json")
	output := formatter.FormatRefresh(refresh)

	// Verify it's valid JSON
	var parsed RefreshResponse
	err := json.Unmarshal([]byte(output), &parsed)
	if err != nil {
		t.Fatalf("invalid JSON output: %v\nOutput:\n%s", err, output)
	}

	if !parsed.Refreshed {
		t.Errorf("expected refreshed=true")
	}
	if parsed.BlueCommit != "abc123" {
		t.Errorf("expected blue_commit=abc123, got %s", parsed.BlueCommit)
	}
}

func TestNewFormatterDefault(t *testing.T) {
	// Empty string should default to text
	formatter := NewFormatter("")
	_, isText := formatter.(*textFormatter)
	if !isText {
		t.Error("empty format string should create text formatter")
	}

	// Unknown format should default to text
	formatter = NewFormatter("unknown")
	_, isText = formatter.(*textFormatter)
	if !isText {
		t.Error("unknown format should create text formatter")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{500 * time.Microsecond, "500μs"},
		{5 * time.Millisecond, "5ms"},
		{100 * time.Millisecond, "100ms"},
		{1500 * time.Millisecond, "1.50s"},
		{5 * time.Second, "5.00s"},
	}

	for _, tc := range tests {
		t.Run(tc.expected, func(t *testing.T) {
			result := formatDuration(tc.duration)
			if result != tc.expected {
				t.Errorf("formatDuration(%v) = %q, expected %q", tc.duration, result, tc.expected)
			}
		})
	}
}

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		seconds  int64
		expected string
	}{
		{60, "1m"},
		{3600, "1h 0m"},
		{3660, "1h 1m"},
		{86400, "1d 0h 0m"},
		{90061, "1d 1h 1m"},
	}

	for _, tc := range tests {
		t.Run(tc.expected, func(t *testing.T) {
			result := formatUptime(tc.seconds)
			if result != tc.expected {
				t.Errorf("formatUptime(%d) = %q, expected %q", tc.seconds, result, tc.expected)
			}
		})
	}
}

func TestTruncateCommit(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"abc1234567890", "abc1234"},
		{"abc123", "abc123"},
		{"ab", "ab"},
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := truncateCommit(tc.input)
			if result != tc.expected {
				t.Errorf("truncateCommit(%q) = %q, expected %q", tc.input, result, tc.expected)
			}
		})
	}
}
