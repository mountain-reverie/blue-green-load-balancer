# Systemd Integration for Blue/Green Load Balancer

## Overview

This document describes the systemd integration for the blue/green load balancer, including:
- Service dependency on Cloudflare Tunnel
- Native journald log integration with structured fields
- Non-root user execution
- Security sandboxing
- `Type=notify-reload` for proper startup/reload signaling

## Implementation Status

| Feature | Status |
|---------|--------|
| Systemd service file | ✅ Implemented |
| Native journald logging | ✅ Implemented |
| sd_notify READY/RELOADING | ✅ Implemented |
| SIGHUP config reload | ✅ Implemented |
| Installation scripts | ✅ Implemented |
| Non-root user | ✅ Implemented |
| Security hardening | ✅ Implemented |

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        Internet                              │
└─────────────────────────┬───────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────────┐
│              Cloudflare Tunnel (cloudflared)                 │
│              systemd: cloudflared.service                    │
│              Connects to localhost:8080                      │
└─────────────────────────┬───────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────────┐
│              Blue/Green Load Balancer                        │
│              systemd: bluegreen.service                      │
│              Listens on :8080                                │
│              Admin on Tailscale network                      │
└──────────────┬─────────────────────────────┬────────────────┘
               │                             │
               ▼                             ▼
        ┌─────────────┐              ┌─────────────┐
        │  Blue App   │              │  Green App  │
        │  :3001      │              │  :3002      │
        └─────────────┘              └─────────────┘
```

## File Locations

| File | Path | Purpose |
|------|------|---------|
| Binary | `/usr/local/bin/bluegreen` | Application executable |
| Config | `/etc/bluegreen/config.yaml` | Main configuration |
| Service | `/etc/systemd/system/bluegreen.service` | Systemd unit file |
| State | `/var/lib/bluegreen/` | Tailscale state, runtime data |
| Environment | `/etc/bluegreen/bluegreen.env` | Secrets (auth keys) |

## Implementation Plan

### Phase 1: Systemd Service File (Implemented)

The service file is in `deploy/bluegreen.service`:

```ini
[Unit]
Description=Blue/Green Load Balancer
Documentation=https://github.com/mountain-reverie/blue-green-load-balancer

# Wait for network to be fully online
After=network-online.target
Wants=network-online.target

# Start after Cloudflare tunnel is ready
# BindsTo ensures we stop if cloudflared stops unexpectedly
After=cloudflared.service
BindsTo=cloudflared.service

[Service]
# notify-reload: service sends READY=1 when ready, supports SIGHUP reload
Type=notify-reload

# Run as dedicated non-root user
User=bluegreen
Group=bluegreen

# Alternatively, use DynamicUser for ephemeral user:
# DynamicUser=yes

# Directory management - systemd creates these automatically
StateDirectory=bluegreen
StateDirectoryMode=0750
ConfigurationDirectory=bluegreen
ConfigurationDirectoryMode=0750

# Working directory
WorkingDirectory=/var/lib/bluegreen

# Environment file for secrets
EnvironmentFile=-/etc/bluegreen/bluegreen.env

# Main process
ExecStart=/usr/local/bin/bluegreen -config /etc/bluegreen/config.yaml

# Reload configuration (sends SIGHUP)
ExecReload=/bin/kill -HUP $MAINPID

# Graceful shutdown - send SIGTERM, wait 35s for drain (30s drain + 5s buffer)
TimeoutStopSec=35
KillMode=mixed
KillSignal=SIGTERM

# Restart policy
Restart=on-failure
RestartSec=5s
StartLimitIntervalSec=60
StartLimitBurst=3

# Logging - stdout/stderr go to journald automatically
# The application uses native journald protocol for structured logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=bluegreen

# Security hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
MemoryDenyWriteExecute=yes

# Allow network access
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
PrivateNetwork=no

# Allow binding to privileged ports if needed (< 1024)
# AmbientCapabilities=CAP_NET_BIND_SERVICE
# CapabilityBoundingSet=CAP_NET_BIND_SERVICE

# System call filtering
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources

[Install]
WantedBy=multi-user.target
```

### Phase 2: User Setup

Create a dedicated system user:

```bash
#!/bin/bash
# install-user.sh

# Create system user without login shell
useradd --system \
    --user-group \
    --home-dir /var/lib/bluegreen \
    --shell /usr/sbin/nologin \
    --comment "Blue/Green Load Balancer" \
    bluegreen

# Create required directories
mkdir -p /etc/bluegreen
mkdir -p /var/lib/bluegreen/tailscale

# Set ownership
chown -R bluegreen:bluegreen /var/lib/bluegreen
chown -R root:bluegreen /etc/bluegreen
chmod 750 /etc/bluegreen
chmod 750 /var/lib/bluegreen
```

### Phase 3: Configuration Files

#### Main Configuration: `/etc/bluegreen/config.yaml`

```yaml
# Blue/Green Load Balancer Configuration
# Systemd managed deployment

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
  repo_url: "https://github.com/your-org/deploy-config.git"
  poll_interval: "30s"
  branch: "main"
  auth_token: "${GIT_AUTH_TOKEN}"  # From environment file

deploy:
  blue_tag: "deploy/blue"
  green_tag: "deploy/green"
  active_tag: "deploy/active"

admin:
  hostname: "bluegreen-admin"
  state_dir: "/var/lib/bluegreen/tailscale"
  auth_key: "${TS_AUTH_KEY}"  # From environment file
  ephemeral: false

health:
  interval: "10s"
  timeout: "5s"

metrics:
  history_duration: "24h"
  history_resolution: "1m"
```

#### Environment File: `/etc/bluegreen/bluegreen.env`

```bash
# Tailscale authentication key
# Generate at: https://login.tailscale.com/admin/settings/keys
TS_AUTH_KEY=tskey-auth-xxxxxxxxxxxxx

# Git authentication token (for private repos)
# GIT_AUTH_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxx

# Webhook secret (optional)
# WEBHOOK_KEY=your-webhook-secret
```

Set secure permissions:
```bash
chmod 600 /etc/bluegreen/bluegreen.env
chown root:bluegreen /etc/bluegreen/bluegreen.env
```

### Phase 4: Cloudflare Tunnel Integration

#### Option A: Using `After=` + `BindsTo=` (Recommended)

The bluegreen service depends on cloudflared:
- `After=cloudflared.service` - Start after tunnel is ready
- `BindsTo=cloudflared.service` - Stop if tunnel stops unexpectedly

#### Cloudflare Tunnel Configuration

```yaml
# /etc/cloudflared/config.yaml
tunnel: your-tunnel-id
credentials-file: /etc/cloudflared/credentials.json

ingress:
  - hostname: app.example.com
    service: http://localhost:8080
  - service: http_status:404
```

#### Cloudflare Tunnel Service

If not installed via `cloudflared service install`, create manually:

```ini
# /etc/systemd/system/cloudflared.service
[Unit]
Description=Cloudflare Tunnel
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
ExecStart=/usr/local/bin/cloudflared tunnel --no-autoupdate run
Restart=on-failure
RestartSec=5s
TimeoutStartSec=0

User=cloudflared
Group=cloudflared

# Hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
StateDirectory=cloudflared
ReadWritePaths=/etc/cloudflared

[Install]
WantedBy=multi-user.target
```

### Phase 5: Journald Integration (Implemented)

The application uses native journald protocol when running under systemd, with automatic fallback to JSON on stdout for non-systemd environments.

#### Implementation

The `internal/logging` package provides:

- **`JournaldHandler`**: Custom `slog.Handler` that writes directly to journald
- **`NewJournaldHandler(level)`**: Creates handler with journald or JSON fallback
- **`IsUnderSystemd()`**: Detects if running under systemd

Features:
- Structured field preservation (e.g., `ERROR=connection refused`)
- Native priority levels mapped from slog levels
- Fields converted to journald format (uppercase, underscore-separated)
- Automatic fallback to JSON for development/testing

#### Viewing Logs

```bash
# Follow logs in real-time
journalctl -u bluegreen -f

# View logs with JSON parsing
journalctl -u bluegreen -o json-pretty

# Filter by priority
journalctl -u bluegreen -p err

# Logs since boot
journalctl -u bluegreen -b

# Logs from last hour
journalctl -u bluegreen --since "1 hour ago"

# Combined logs with cloudflared
journalctl -u bluegreen -u cloudflared -f

# Query by structured field (if using native journald)
journalctl _SYSTEMD_UNIT=bluegreen.service BLUE_URL=http://localhost:3001
```

### Phase 6: Installation Script

```bash
#!/bin/bash
# install.sh - Install Blue/Green Load Balancer

set -euo pipefail

INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/bluegreen"
STATE_DIR="/var/lib/bluegreen"
SERVICE_FILE="/etc/systemd/system/bluegreen.service"

echo "Installing Blue/Green Load Balancer..."

# 1. Create user
if ! id -u bluegreen &>/dev/null; then
    useradd --system --user-group \
        --home-dir "$STATE_DIR" \
        --shell /usr/sbin/nologin \
        --comment "Blue/Green Load Balancer" \
        bluegreen
    echo "Created user: bluegreen"
fi

# 2. Create directories
mkdir -p "$CONFIG_DIR"
mkdir -p "$STATE_DIR/tailscale"

# 3. Install binary
install -m 755 bluegreen "$INSTALL_DIR/bluegreen"
echo "Installed binary to $INSTALL_DIR/bluegreen"

# 4. Install config if not exists
if [[ ! -f "$CONFIG_DIR/config.yaml" ]]; then
    install -m 640 -o root -g bluegreen config/config.example.yaml "$CONFIG_DIR/config.yaml"
    echo "Installed config to $CONFIG_DIR/config.yaml"
fi

# 5. Create environment file template
if [[ ! -f "$CONFIG_DIR/bluegreen.env" ]]; then
    cat > "$CONFIG_DIR/bluegreen.env" << 'EOF'
# Tailscale authentication key
TS_AUTH_KEY=

# Git authentication token (optional)
# GIT_AUTH_TOKEN=

# Webhook secret (optional)
# WEBHOOK_KEY=
EOF
    chmod 600 "$CONFIG_DIR/bluegreen.env"
    chown root:bluegreen "$CONFIG_DIR/bluegreen.env"
    echo "Created environment file template at $CONFIG_DIR/bluegreen.env"
fi

# 6. Set directory ownership
chown -R bluegreen:bluegreen "$STATE_DIR"
chown root:bluegreen "$CONFIG_DIR"
chmod 750 "$CONFIG_DIR"
chmod 750 "$STATE_DIR"

# 7. Install systemd service
install -m 644 deploy/bluegreen.service "$SERVICE_FILE"
echo "Installed systemd service"

# 8. Reload systemd
systemctl daemon-reload
echo "Reloaded systemd"

echo ""
echo "Installation complete!"
echo ""
echo "Next steps:"
echo "  1. Edit $CONFIG_DIR/config.yaml with your settings"
echo "  2. Add your Tailscale auth key to $CONFIG_DIR/bluegreen.env"
echo "  3. Enable and start the service:"
echo "     systemctl enable --now bluegreen"
echo ""
echo "View logs with:"
echo "  journalctl -u bluegreen -f"
```

### Phase 7: Service Management

```bash
# Enable service to start on boot
systemctl enable bluegreen

# Start the service
systemctl start bluegreen

# Check status (shows READY/RELOADING state)
systemctl status bluegreen

# View logs
journalctl -u bluegreen -f

# Reload configuration (hot reload via SIGHUP)
# This reloads: health intervals, git poll interval, health paths
# Does NOT reload: listen addresses, Tailscale settings
systemctl reload bluegreen

# Restart for settings that require restart
systemctl restart bluegreen

# Stop
systemctl stop bluegreen
```

#### Hot-Reloadable Settings

The following settings can be changed without restart using `systemctl reload`:
- `health.interval`, `health.timeout`
- `git.poll_interval`
- `services.*.health_path`
- `proxy.drain_timeout`

Settings that require full restart:
- `proxy.listen_addr`
- `admin.*` (Tailscale/Headscale configuration)

## Dependency Diagram

```
                    network-online.target
                            │
                            ▼
                   cloudflared.service
                            │
              ┌─────────────┴─────────────┐
              │  After + BindsTo          │
              ▼                           │
        bluegreen.service ◄───────────────┘
              │
              ▼
        multi-user.target
```

## Security Considerations

1. **Non-root execution**: Service runs as dedicated `bluegreen` user
2. **Filesystem isolation**: `ProtectSystem=strict`, `ProtectHome=yes`
3. **No privilege escalation**: `NoNewPrivileges=yes`
4. **Syscall filtering**: Only allows system-service related calls
5. **Secret management**: Auth keys in separate env file with restricted permissions
6. **Network restrictions**: Only AF_INET, AF_INET6, AF_UNIX, AF_NETLINK allowed

## Files to Create

| Priority | File | Description |
|----------|------|-------------|
| 1 | `deploy/bluegreen.service` | Systemd service unit |
| 2 | `deploy/install.sh` | Installation script |
| 3 | `config/config.example.yaml` | Update with systemd paths |
| 4 | `docs/deployment.md` | Deployment documentation |

## References

- [systemd.exec - Execution environment](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html)
- [Dynamic Users with systemd](https://0pointer.net/blog/dynamic-users-with-systemd.html)
- [Cloudflare Tunnel Linux Service](https://developers.cloudflare.com/cloudflare-one/connections/connect-apps/run-tunnel/as-a-service/linux/)
- [systemd Unit Dependencies](https://fedoramagazine.org/systemd-unit-dependencies-and-order/)
- [go-systemd journal package](https://pkg.go.dev/github.com/coreos/go-systemd/v22/journal)
- [journalctl usage](https://www.digitalocean.com/community/tutorials/how-to-use-journalctl-to-view-and-manipulate-systemd-logs)
