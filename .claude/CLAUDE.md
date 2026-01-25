# Blue/Green Load Balancer - Claude Code Guidelines

## Project Overview

A Go-based blue/green load balancer that:

- Proxies traffic between two local HTTP services (blue/green)
- Exposes public traffic via Cloudflare Tunnel (externally configured)
- Provides admin interface accessible only via Tailscale network
- Switches deployments based on git tags (watch + webhook)
- Displays comprehensive statistics with live UI updates

## Technology Stack

- **Language**: Go 1.25+
- **HTTP Framework**: Huma v2 (REST API with auto OpenAPI generation)
- **Templating**: templ + templUI (type-safe HTML templates)
- **Tailscale**: tsnet (embedded Tailscale server)
- **Git Operations**: go-git (pure Go git implementation)
- **Metrics**: Prometheus client_golang
- **Testing**: testcontainers-go, testify

## Code Style & Conventions

### Go Code

- Use `log/slog` for structured logging
- Use `context.Context` for cancellation and timeouts
- Follow standard Go project layout
- Use interfaces for testability
- Prefer composition over inheritance

### Error Handling

- Wrap errors with context using `fmt.Errorf("operation: %w", err)`
- Return errors up the call stack, handle at appropriate level
- Use sentinel errors sparingly

### Testing

- Unit tests in `*_test.go` files alongside code
- Integration tests in `tests/integration/`
- Use testcontainers for external dependencies
- Table-driven tests where appropriate

## Project Structure

```
cmd/bluegreen/main.go       # Application entrypoint
internal/
  config/                   # Configuration loading
  proxy/                    # Reverse proxy with graceful switching
  switcher/                 # Switch orchestration and git watching
  admin/                    # Tailscale admin server with Huma API
  health/                   # Backend health checking
  metrics/                  # Prometheus metrics and history
  ui/                       # Dashboard templates and SSE
tests/integration/          # Integration tests with testcontainers
```

## Key Design Decisions

1. **Atomic Backend Switching**: Use `atomic.Pointer` for lock-free backend switching
2. **Graceful Drain**: Track active connections and drain before switching
3. **tsnet for Admin**: Admin interface only accessible via Tailscale network
4. **Pure Go Git**: No shell dependencies for git operations
5. **Ring Buffer Metrics**: Fixed memory usage for historical data

## Common Tasks

### Building

```bash
go build ./cmd/bluegreen
```

### Running Tests

```bash
go test ./...
go test -v ./tests/integration/...  # Integration tests
```

### Generating Templates

```bash
templ generate ./internal/ui/templates/
```

### Running Locally

```bash
./bluegreen -config config/config.yaml -local-admin :8081
```

## API Endpoints

- `GET /api/status` - Current status of blue/green services
- `GET /api/metrics` - Current metrics snapshot
- `GET /api/metrics/history` - Historical metrics data
- `POST /api/switch` - Manually trigger switch
- `POST /api/webhook/switch` - Webhook-triggered switch
- `GET /api/docs` - Swagger UI
- `GET /api/openapi` - OpenAPI spec
