package bgctl

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Formatter formats API responses for output.
type Formatter interface {
	FormatStatus(*StatusResponse) string
	FormatMetrics(*MetricsResponse) string
	FormatRefresh(*RefreshResponse) string
}

// NewFormatter creates a new formatter based on the format string.
// Valid formats are "json" and "text" (default).
func NewFormatter(format string) Formatter {
	if format == "json" {
		return &jsonFormatter{}
	}
	return &textFormatter{}
}

// textFormatter formats responses as human-readable text.
type textFormatter struct{}

func (f *textFormatter) FormatStatus(s *StatusResponse) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Active Service: %s\n", s.ActiveService))
	b.WriteString(fmt.Sprintf("Switch Count:   %d\n", s.SwitchCount))
	if !s.LastSwitch.IsZero() {
		b.WriteString(fmt.Sprintf("Last Switch:    %s\n", s.LastSwitch.Format(time.RFC3339)))
	}
	b.WriteString("\n")

	// Blue service
	b.WriteString(formatServiceLine("Blue", &s.Blue))
	b.WriteString("\n")

	// Green service
	b.WriteString(formatServiceLine("Green", &s.Green))
	b.WriteString("\n")

	// Git status
	if s.Git.BlueCommit != "" || s.Git.GreenCommit != "" {
		b.WriteString("\nGit:\n")
		if s.Git.BlueCommit != "" {
			b.WriteString(fmt.Sprintf("  Blue Commit:   %s\n", truncateCommit(s.Git.BlueCommit)))
		}
		if s.Git.GreenCommit != "" {
			b.WriteString(fmt.Sprintf("  Green Commit:  %s\n", truncateCommit(s.Git.GreenCommit)))
		}
		if s.Git.ActiveCommit != "" {
			b.WriteString(fmt.Sprintf("  Active Commit: %s\n", truncateCommit(s.Git.ActiveCommit)))
		}
		if s.Git.LastTag != "" {
			b.WriteString(fmt.Sprintf("  Last Tag:      %s\n", s.Git.LastTag))
		}
		if !s.Git.LastFetch.IsZero() {
			b.WriteString(fmt.Sprintf("  Last Fetch:    %s\n", s.Git.LastFetch.Format(time.RFC3339)))
		}
		if s.Git.Error != "" {
			b.WriteString(fmt.Sprintf("  Error:         %s\n", s.Git.Error))
		}
	}

	return b.String()
}

func formatServiceLine(name string, s *ServiceStatus) string {
	healthStatus := "healthy"
	if !s.Healthy {
		healthStatus = "unhealthy"
	}

	latencyStr := formatDuration(s.Latency)

	line := fmt.Sprintf("%-5s: %-9s  %s  (%s)", name, healthStatus, s.URL, latencyStr)
	if s.Error != "" {
		line += fmt.Sprintf("  error: %s", s.Error)
	}
	return line
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dμs", d.Microseconds())
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

func truncateCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

func (f *textFormatter) FormatMetrics(m *MetricsResponse) string {
	var b strings.Builder

	b.WriteString("Requests:\n")
	b.WriteString(fmt.Sprintf("  Total:      %d\n", m.Requests.Total))
	b.WriteString(fmt.Sprintf("  Per Second: %.2f\n", m.Requests.PerSecond))
	b.WriteString(fmt.Sprintf("  Blue:       %d\n", m.Requests.Blue))
	b.WriteString(fmt.Sprintf("  Green:      %d\n", m.Requests.Green))
	b.WriteString("\n")

	b.WriteString("Latency:\n")
	b.WriteString(fmt.Sprintf("  P50: %.2fms\n", m.Latency.P50))
	b.WriteString(fmt.Sprintf("  P90: %.2fms\n", m.Latency.P90))
	b.WriteString(fmt.Sprintf("  P99: %.2fms\n", m.Latency.P99))
	b.WriteString(fmt.Sprintf("  Avg: %.2fms\n", m.Latency.Avg))
	b.WriteString("\n")

	b.WriteString("Errors:\n")
	b.WriteString(fmt.Sprintf("  Total: %d\n", m.Errors.Total))
	b.WriteString(fmt.Sprintf("  Rate:  %.4f\n", m.Errors.Rate))
	if m.Errors.LastError != "" {
		b.WriteString(fmt.Sprintf("  Last:  %s\n", m.Errors.LastError))
	}
	b.WriteString("\n")

	b.WriteString(fmt.Sprintf("Active Connections: %d\n", m.ActiveConnections))
	b.WriteString(fmt.Sprintf("Uptime: %s\n", formatUptime(m.UptimeSeconds)))

	return b.String()
}

func formatUptime(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

func (f *textFormatter) FormatRefresh(r *RefreshResponse) string {
	var b strings.Builder

	if r.Refreshed {
		b.WriteString("Refresh: success\n")
	} else {
		b.WriteString("Refresh: failed\n")
	}

	if r.Error != "" {
		b.WriteString(fmt.Sprintf("Error: %s\n", r.Error))
	}

	if r.BlueCommit != "" {
		b.WriteString(fmt.Sprintf("Blue Commit:   %s\n", truncateCommit(r.BlueCommit)))
	}
	if r.GreenCommit != "" {
		b.WriteString(fmt.Sprintf("Green Commit:  %s\n", truncateCommit(r.GreenCommit)))
	}
	if r.ActiveCommit != "" {
		b.WriteString(fmt.Sprintf("Active Commit: %s\n", truncateCommit(r.ActiveCommit)))
	}
	if !r.LastFetch.IsZero() {
		b.WriteString(fmt.Sprintf("Last Fetch:    %s\n", r.LastFetch.Format(time.RFC3339)))
	}

	return b.String()
}

// jsonFormatter formats responses as JSON.
type jsonFormatter struct{}

func (f *jsonFormatter) FormatStatus(s *StatusResponse) string {
	return toJSON(s)
}

func (f *jsonFormatter) FormatMetrics(m *MetricsResponse) string {
	return toJSON(m)
}

func (f *jsonFormatter) FormatRefresh(r *RefreshResponse) string {
	return toJSON(r)
}

func toJSON(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error": "failed to marshal JSON: %s"}`, err)
	}
	return string(data) + "\n"
}
