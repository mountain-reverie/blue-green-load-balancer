# GitHub Copilot Instructions

## Project Context

This is a Go-based blue/green load balancer with the following characteristics:

- **Purpose**: Route traffic between two backend services (blue/green) with zero-downtime switching
- **Admin Interface**: Accessible only via Tailscale network using tsnet
- **Deployment Triggers**: Git tag changes and webhooks
- **UI**: Real-time dashboard with SSE updates using templ templates

## Code Generation Guidelines

### Go Style

- Use Go 1.25+ features
- Prefer `log/slog` over `log` package
- Use `context.Context` for cancellation propagation
- Follow standard Go error handling patterns
- Use table-driven tests

### HTTP Handlers

- Use Huma v2 for API handlers with typed request/response structs
- Use chi router for middleware and routing
- Implement proper request validation
- Return appropriate HTTP status codes

### Concurrency

- Use `sync/atomic` for simple counters and pointers
- Use `sync.RWMutex` for complex shared state
- Always use `context.Context` for cancellation
- Avoid goroutine leaks with proper cleanup

### Configuration

- Support YAML configuration files
- Use environment variable expansion in config
- Provide sensible defaults
- Validate configuration on load

### Metrics

- Use Prometheus client_golang
- Include labels for backend (blue/green)
- Track request count, duration, and errors
- Maintain historical data in ring buffers

### Templates

- Use templ for type-safe HTML templates
- Keep templates in `internal/ui/templates/`
- Use tailwindcss classes for styling
- Support SSE for real-time updates

## File Locations

- Configuration: `internal/config/`
- Proxy logic: `internal/proxy/`
- Admin API: `internal/admin/`
- Health checks: `internal/health/`
- Metrics: `internal/metrics/`
- UI templates: `internal/ui/templates/`
- Integration tests: `tests/integration/`

## Dependencies

- github.com/danielgtaylor/huma/v2 - API framework
- github.com/go-chi/chi/v5 - HTTP router
- github.com/a-h/templ - Template engine
- github.com/go-git/go-git/v5 - Git operations
- tailscale.com/tsnet - Tailscale server
- github.com/prometheus/client_golang - Metrics
- github.com/testcontainers/testcontainers-go - Integration tests
