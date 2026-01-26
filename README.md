# Blue/Green Load Balancer

[![CI](https://github.com/mountain-reverie/blue-green-load-balancer/actions/workflows/main.yml/badge.svg)](https://github.com/mountain-reverie/blue-green-load-balancer/actions/workflows/main.yml)
[![Coverage](https://mountain-reverie.github.io/blue-green-load-balancer/coverage-badge.svg)](https://mountain-reverie.github.io/blue-green-load-balancer/coverage.html)
[![Benchmark](https://mountain-reverie.github.io/blue-green-load-balancer/benchmark/badge.svg)](https://mountain-reverie.github.io/blue-green-load-balancer/benchmark/)
[![Go Report Card](https://goreportcard.com/badge/github.com/mountain-reverie/blue-green-load-balancer)](https://goreportcard.com/report/github.com/mountain-reverie/blue-green-load-balancer)

A Go-based load balancer for blue/green deployments with git-driven switching and a secure admin interface accessible only via Tailscale.

## Overview

This load balancer proxies HTTP traffic between two backend services (blue and green) and automatically switches between them based on git tags. The admin interface runs on a private Tailscale network, keeping management access separate from public traffic.

### Key Features

- **Blue/Green Switching**: Seamlessly switch traffic between two backend services
- **Git-Driven Deployments**: Monitor git tags to trigger automatic switches
- **Secure Admin Interface**: Admin UI accessible only via Tailscale (or Headscale)
- **Graceful Draining**: Active connections are drained before switching backends
- **Health Monitoring**: Continuous health checks on both backends
- **Live Dashboard**: Real-time status updates via Server-Sent Events
- **Prometheus Metrics**: Export metrics for monitoring and alerting

## Architecture

```
                                    ┌─────────────────┐
                                    │  Tailscale/     │
                                    │  Headscale      │
                                    │  Network        │
                                    └────────┬────────┘
                                             │
┌──────────────┐    ┌──────────────┐    ┌────▼────┐
│   Internet   │───▶│  Cloudflare  │───▶│  Proxy  │
│   Traffic    │    │  Tunnel      │    │  :8080  │
└──────────────┘    └──────────────┘    └────┬────┘
                                             │
                         ┌───────────────────┼───────────────────┐
                         │                   │                   │
                    ┌────▼────┐         ┌────▼────┐         ┌────▼────┐
                    │  Blue   │         │  Green  │         │  Admin  │
                    │ Backend │         │ Backend │         │   UI    │
                    │  :3001  │         │  :3002  │         │  (tsnet)│
                    └─────────┘         └─────────┘         └─────────┘
```

## Installation

### Prerequisites

- Go 1.25 or later
- Docker (for integration tests)
- Tailscale auth key (for production) or Headscale server (for self-hosted)

### Building

```bash
go build -o bluegreen ./cmd/bluegreen
```

### Running

```bash
# With Tailscale admin interface
./bluegreen -config config/config.yaml

# With local admin interface (for development)
./bluegreen -config config/config.yaml -local-admin :8081
```

### Docker

Pre-built multi-architecture images are available:

```bash
# Pull the latest image
docker pull ghcr.io/mountain-reverie/blue-green-load-balancer:latest

# Run with a config file
docker run -v /path/to/config.yaml:/etc/bluegreen/config.yaml \
  ghcr.io/mountain-reverie/blue-green-load-balancer:latest
```

Images are built for both `linux/amd64` and `linux/arm64` using distroless base images.

## Configuration

Create a configuration file (see `config/config.example.yaml`):

```yaml
services:
  blue:
    url: "http://localhost:3001"
    health_path: "/health"
  green:
    url: "http://localhost:3002"
    health_path: "/health"

proxy:
  listen_addr: ":8080"
  drain_timeout: "30s"

git:
  repo_url: "https://github.com/user/deploy-config.git"
  poll_interval: "30s"
  branch: "main"
  # auth_token: "${GIT_AUTH_TOKEN}"  # For private repos

deploy:
  blue_tag: "deploy/blue"
  green_tag: "deploy/green"
  active_tag: "deploy/active"

admin:
  hostname: "bluegreen-admin"
  state_dir: "/var/lib/bluegreen/tailscale"
  # auth_key: "${TS_AUTH_KEY}"      # Tailscale auth key
  # control_url: "https://headscale.example.com"  # For Headscale

health:
  interval: "10s"
  timeout: "5s"

metrics:
  history_duration: "24h"
  history_resolution: "1m"
```

### Configuration Reference

| Section | Field | Description | Default |
|---------|-------|-------------|---------|
| `services.blue` | `url` | URL of the blue backend service | Required |
| `services.blue` | `health_path` | Health check endpoint path | `/health` |
| `services.green` | `url` | URL of the green backend service | Required |
| `services.green` | `health_path` | Health check endpoint path | `/health` |
| `proxy` | `listen_addr` | Address for the proxy to listen on | `:8080` |
| `proxy` | `drain_timeout` | Time to wait for connections to drain | `30s` |
| `git` | `repo_url` | Git repository URL to watch | Optional |
| `git` | `poll_interval` | How often to check for tag changes | `30s` |
| `git` | `branch` | Branch to monitor | `main` |
| `git` | `auth_token` | Token for private repository access | Optional |
| `deploy` | `blue_tag` | Git tag indicating blue deployment | `deploy/blue` |
| `deploy` | `green_tag` | Git tag indicating green deployment | `deploy/green` |
| `deploy` | `active_tag` | Git tag indicating which is active | `deploy/active` |
| `admin` | `hostname` | Tailscale hostname for admin interface | `bluegreen-admin` |
| `admin` | `state_dir` | Directory for Tailscale state | `/var/lib/bluegreen/tailscale` |
| `admin` | `auth_key` | Tailscale or Headscale auth key | Optional |
| `admin` | `control_url` | Custom control server URL (Headscale) | Optional |
| `admin` | `ephemeral` | Remove node when offline | `false` |
| `health` | `interval` | Time between health checks | `10s` |
| `health` | `timeout` | Health check timeout | `5s` |
| `metrics` | `history_duration` | How long to keep metrics history | `24h` |
| `metrics` | `history_resolution` | Resolution of historical data | `1m` |

### Environment Variables

Configuration values support environment variable expansion:

```yaml
git:
  auth_token: "${GIT_AUTH_TOKEN}"
admin:
  auth_key: "${TS_AUTH_KEY}"
```

## Git-Driven Switching

The load balancer monitors a git repository for tag changes to determine which backend should receive traffic.

### Tag Structure

- `deploy/blue` - Points to the commit deployed on blue backend
- `deploy/green` - Points to the commit deployed on green backend
- `deploy/active` - Points to the same commit as the currently active backend

### Switching Logic

When the git watcher detects that `deploy/active` points to a different commit than the current active backend:

1. Identifies which backend (blue or green) has the matching commit
2. Initiates graceful drain of the current backend
3. Switches traffic to the new backend
4. Updates internal state

### Manual Switching

Use the admin API to switch manually:

```bash
curl -X POST http://bluegreen-admin:80/api/switch \
  -H "Content-Type: application/json" \
  -d '{"target": "green"}'
```

## Admin Interface

The admin interface is accessible only via the Tailscale network at `http://<hostname>:80`.

### Dashboard

The web dashboard (`/`) shows:

- Current active backend (blue or green)
- Health status of both backends
- Request metrics and latency
- Recent switch history
- Git tag information

### API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | Dashboard UI |
| `/api/status` | GET | Current status of both backends |
| `/api/metrics` | GET | Current metrics snapshot |
| `/api/metrics/history` | GET | Historical metrics data |
| `/api/switch` | POST | Manually switch to specified backend |
| `/api/docs` | GET | Swagger UI documentation |
| `/api/openapi.json` | GET | OpenAPI specification |
| `/events` | GET | Server-Sent Events stream |
| `/metrics` | GET | Prometheus metrics |

### Status Response Example

```json
{
  "active_service": "blue",
  "blue": {
    "url": "http://localhost:3001",
    "health_path": "/health",
    "healthy": true,
    "last_check": "2024-01-15T10:30:00Z",
    "latency": 5000000
  },
  "green": {
    "url": "http://localhost:3002",
    "health_path": "/health",
    "healthy": true,
    "last_check": "2024-01-15T10:30:00Z",
    "latency": 4500000
  },
  "last_switch": "2024-01-15T09:00:00Z",
  "switch_count": 5,
  "git": {
    "last_fetch": "2024-01-15T10:29:30Z",
    "blue_commit": "abc1234",
    "green_commit": "def5678"
  }
}
```

## Using with Headscale

For self-hosted Tailscale control, configure the admin section:

```yaml
admin:
  hostname: "bluegreen-admin"
  state_dir: "/var/lib/bluegreen/tailscale"
  control_url: "https://headscale.example.com"
  auth_key: "${HEADSCALE_AUTH_KEY}"
  ephemeral: true  # Recommended for containers
```

Generate an auth key in Headscale:

```bash
headscale preauthkeys create --user myuser --reusable --ephemeral
```

## Prometheus Metrics

The `/metrics` endpoint exposes Prometheus metrics:

- `bluegreen_requests_total` - Total requests by backend and status
- `bluegreen_request_duration_seconds` - Request latency histogram
- `bluegreen_active_connections` - Current active connections
- `bluegreen_switches_total` - Total backend switches
- `bluegreen_backend_health` - Backend health status (0/1)

## Development

### Project Structure

```
cmd/bluegreen/          # Application entrypoint
internal/
  admin/                # Tailscale admin server and API
  app/                  # Application orchestration
  config/               # Configuration loading
  health/               # Backend health checking
  metrics/              # Prometheus metrics
  proxy/                # Reverse proxy
  switcher/             # Switch logic and git watching
  ui/                   # Dashboard templates and SSE
static/                 # Static assets (CSS)
tests/integration/      # Integration tests
config/                 # Example configuration
```

### Running Tests

```bash
# Unit tests
go test ./...

# Integration tests (requires Docker)
go test -v ./tests/integration/...

# Headscale integration tests
go test -v ./tests/integration/headscale/...
```

### Generating Templates

If modifying UI templates:

```bash
templ generate ./internal/ui/templates/
```

## Command Line Options

### bluegreen

```
Usage: bluegreen [options]

Options:
  -config string
        Path to configuration file (default "config/config.yaml")
  -local-admin string
        Run admin server locally on this address instead of Tailscale
  -log-level string
        Log level: debug, info, warn, error (default "info")
```

### bgctl

A CLI tool for interacting with the admin server via Tailscale:

```
Usage: bgctl [global flags] <command>

Commands:
  status    Display current blue/green status
  metrics   Display current metrics snapshot
  refresh   Trigger a git refresh

Global Flags:
  -H, --hostname string      Admin server hostname (default: bluegreen-admin)
  --control-url string       Tailscale/Headscale control server URL
  --auth-key string          Tailscale auth key (or TS_AUTH_KEY env)
  --state-dir string         Tailscale state directory (default: ~/.bgctl/tailscale)
  -o, --output string        Output format: json or text (default: text)
  --timeout duration         Request timeout (default: 30s)
  -v, --verbose              Enable verbose logging
```

Example usage:

```bash
# Check status
bgctl status

# JSON output for scripting
bgctl -o json status | jq '.active_service'

# With Headscale
bgctl --control-url https://headscale.example.com status

# Trigger git refresh
bgctl refresh
```

## License

See LICENSE file.
