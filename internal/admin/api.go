package admin

import (
	"context"
	"fmt"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/config"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/health"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/metrics"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/switcher"
)

// ServiceStatus represents the status of a backend service.
type ServiceStatus struct {
	URL        string        `json:"url" example:"http://localhost:3001"`
	HealthPath string        `json:"health_path" example:"/health"`
	Healthy    bool          `json:"healthy" example:"true"`
	LastCheck  time.Time     `json:"last_check"`
	Latency    time.Duration `json:"latency"`
	Error      string        `json:"error,omitempty"`
}

// GitStatusResponse represents git repository status.
type GitStatusResponse struct {
	LastFetch    time.Time `json:"last_fetch"`
	LastTag      string    `json:"last_tag,omitempty"`
	BlueCommit   string    `json:"blue_commit,omitempty"`
	GreenCommit  string    `json:"green_commit,omitempty"`
	ActiveCommit string    `json:"active_commit,omitempty"`
	Error        string    `json:"error,omitempty"`
}

// StatusOutput is the response for GET /api/status
type StatusOutput struct {
	Body struct {
		ActiveService string            `json:"active_service" example:"blue"`
		Blue          ServiceStatus     `json:"blue"`
		Green         ServiceStatus     `json:"green"`
		LastSwitch    time.Time         `json:"last_switch"`
		SwitchCount   int64             `json:"switch_count" example:"5"`
		Git           GitStatusResponse `json:"git"`
	}
}

// SwitchInput is the request body for POST /api/switch
type SwitchInput struct {
	Body struct {
		Target string `json:"target" enum:"blue,green" required:"true" doc:"Target service to switch to"`
	}
}

// SwitchOutput is the response for POST /api/switch
type SwitchOutput struct {
	Body struct {
		Success  bool   `json:"success" example:"true"`
		Previous string `json:"previous" example:"blue"`
		Current  string `json:"current" example:"green"`
		Message  string `json:"message,omitempty"`
	}
}

// MetricsOutput is the response for GET /api/metrics
type MetricsOutput struct {
	Body struct {
		Requests          RequestMetrics `json:"requests"`
		Latency           LatencyMetrics `json:"latency"`
		Errors            ErrorMetrics   `json:"errors"`
		ActiveConnections int64          `json:"active_connections" example:"42"`
		UptimeSeconds     int64          `json:"uptime_seconds" example:"86400"`
	}
}

// RequestMetrics contains request statistics.
type RequestMetrics struct {
	Total     int64   `json:"total" example:"10000"`
	PerSecond float64 `json:"per_second" example:"15.5"`
	Blue      int64   `json:"blue" example:"5000"`
	Green     int64   `json:"green" example:"5000"`
}

// LatencyMetrics contains latency statistics.
type LatencyMetrics struct {
	P50 float64 `json:"p50" example:"10.5"`
	P90 float64 `json:"p90" example:"25.0"`
	P99 float64 `json:"p99" example:"100.0"`
	Avg float64 `json:"avg" example:"15.2"`
}

// ErrorMetrics contains error statistics.
type ErrorMetrics struct {
	Total     int64   `json:"total" example:"10"`
	Rate      float64 `json:"rate" example:"0.001"`
	LastError string  `json:"last_error,omitempty" example:"connection refused"`
}

// MetricsHistoryOutput is the response for GET /api/metrics/history
type MetricsHistoryOutput struct {
	Body struct {
		Points     []metrics.DataPoint `json:"points"`
		Resolution time.Duration       `json:"resolution"`
		Duration   time.Duration       `json:"duration"`
	}
}

// HistoryInput is the query params for GET /api/metrics/history
type HistoryInput struct {
	Duration   string `query:"duration" default:"1h" doc:"Duration of history to return (e.g., 1h, 24h)"`
	Resolution string `query:"resolution" default:"1m" doc:"Resolution of data points (e.g., 1m, 5m)"`
}

// RegisterAPI registers all API operations with Huma.
func RegisterAPI(api huma.API, s *Server) {
	// GET /api/status
	huma.Get(api, "/api/status", func(ctx context.Context, input *struct{}) (*StatusOutput, error) {
		status := s.switcher.GetStatus()
		blueEndpoint := s.cfg.GetServiceEndpoint(config.ServiceBlue)
		greenEndpoint := s.cfg.GetServiceEndpoint(config.ServiceGreen)

		out := &StatusOutput{}
		out.Body.ActiveService = string(status.ActiveService)
		out.Body.LastSwitch = status.LastSwitch
		out.Body.SwitchCount = status.SwitchCount

		out.Body.Blue = toServiceStatus(blueEndpoint, status.BlueHealth)
		out.Body.Green = toServiceStatus(greenEndpoint, status.GreenHealth)

		out.Body.Git = GitStatusResponse{
			LastFetch:    status.Git.LastFetch,
			LastTag:      status.Git.LastTag,
			BlueCommit:   status.Git.BlueCommit,
			GreenCommit:  status.Git.GreenCommit,
			ActiveCommit: status.Git.ActiveCommit,
			Error:        status.Git.Error,
		}

		return out, nil
	})

	// POST /api/switch
	huma.Post(api, "/api/switch", func(ctx context.Context, input *SwitchInput) (*SwitchOutput, error) {
		var target config.ServiceTarget
		switch input.Body.Target {
		case "blue":
			target = config.ServiceBlue
		case "green":
			target = config.ServiceGreen
		default:
			return nil, huma.Error400BadRequest(fmt.Sprintf("invalid target: %s", input.Body.Target))
		}

		previous := s.switcher.ActiveTarget()

		if err := s.switcher.Switch(ctx, target, switcher.TriggerManual); err != nil {
			return nil, huma.Error500InternalServerError(err.Error())
		}

		out := &SwitchOutput{}
		out.Body.Success = true
		out.Body.Previous = string(previous)
		out.Body.Current = string(target)
		out.Body.Message = fmt.Sprintf("Switched from %s to %s", previous, target)

		return out, nil
	})

	// GET /api/metrics
	huma.Get(api, "/api/metrics", func(ctx context.Context, input *struct{}) (*MetricsOutput, error) {
		if s.metrics == nil {
			return nil, huma.Error500InternalServerError("metrics not available")
		}

		snapshot := s.metrics.Snapshot()

		out := &MetricsOutput{}
		out.Body.Requests = RequestMetrics{
			Total:     snapshot.TotalRequests,
			PerSecond: snapshot.RequestsPerSecond,
			Blue:      snapshot.BlueRequests,
			Green:     snapshot.GreenRequests,
		}
		out.Body.Latency = LatencyMetrics{
			P50: snapshot.LatencyP50,
			P90: snapshot.LatencyP90,
			P99: snapshot.LatencyP99,
			Avg: snapshot.LatencyAvg,
		}
		out.Body.Errors = ErrorMetrics{
			Total:     snapshot.TotalErrors,
			Rate:      snapshot.ErrorRate,
			LastError: snapshot.LastError,
		}
		out.Body.ActiveConnections = snapshot.ActiveConnections
		out.Body.UptimeSeconds = snapshot.UptimeSeconds

		return out, nil
	})

	// GET /api/metrics/history
	huma.Get(api, "/api/metrics/history", func(ctx context.Context, input *HistoryInput) (*MetricsHistoryOutput, error) {
		if s.metrics == nil {
			return nil, huma.Error500InternalServerError("metrics not available")
		}

		duration, err := time.ParseDuration(input.Duration)
		if err != nil {
			duration = time.Hour
		}

		resolution, err := time.ParseDuration(input.Resolution)
		if err != nil {
			resolution = time.Minute
		}

		points := s.metrics.History(duration, resolution)

		out := &MetricsHistoryOutput{}
		out.Body.Points = points
		out.Body.Duration = duration
		out.Body.Resolution = resolution

		return out, nil
	})
}

func toServiceStatus(endpoint config.ServiceEndpoint, h health.Status) ServiceStatus {
	return ServiceStatus{
		URL:        endpoint.URL,
		HealthPath: endpoint.HealthPath,
		Healthy:    h.Healthy,
		LastCheck:  h.LastCheck,
		Latency:    h.Latency,
		Error:      h.Error,
	}
}
