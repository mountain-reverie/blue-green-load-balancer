#!/bin/bash
# install.sh - Install Blue/Green Load Balancer
#
# This script installs the blue/green load balancer as a systemd service.
# Run as root or with sudo.
#
# Usage:
#   sudo ./install.sh              # Install from current directory
#   sudo ./install.sh /path/to/bin # Install specific binary

set -euo pipefail

# Configuration
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/bluegreen"
STATE_DIR="/var/lib/bluegreen"
SERVICE_FILE="/etc/systemd/system/bluegreen.service"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check if running as root
if [[ $EUID -ne 0 ]]; then
    log_error "This script must be run as root"
    exit 1
fi

# Find binary
if [[ $# -ge 1 ]]; then
    BINARY_PATH="$1"
else
    # Look for binary in common locations
    if [[ -f "${SCRIPT_DIR}/../bluegreen" ]]; then
        BINARY_PATH="${SCRIPT_DIR}/../bluegreen"
    elif [[ -f "${SCRIPT_DIR}/bluegreen" ]]; then
        BINARY_PATH="${SCRIPT_DIR}/bluegreen"
    elif [[ -f "./bluegreen" ]]; then
        BINARY_PATH="./bluegreen"
    else
        log_error "Binary not found. Please build first: go build ./cmd/bluegreen"
        log_error "Or specify path: $0 /path/to/bluegreen"
        exit 1
    fi
fi

if [[ ! -f "$BINARY_PATH" ]]; then
    log_error "Binary not found: $BINARY_PATH"
    exit 1
fi

log_info "Installing Blue/Green Load Balancer..."

# 1. Create system user
if ! id -u bluegreen &>/dev/null; then
    useradd --system --user-group \
        --home-dir "$STATE_DIR" \
        --shell /usr/sbin/nologin \
        --comment "Blue/Green Load Balancer" \
        bluegreen
    log_info "Created user: bluegreen"
else
    log_info "User bluegreen already exists"
fi

# 2. Create directories
mkdir -p "$CONFIG_DIR"
mkdir -p "$STATE_DIR/tailscale"
log_info "Created directories"

# 3. Install binary
install -m 755 "$BINARY_PATH" "$INSTALL_DIR/bluegreen"
log_info "Installed binary to $INSTALL_DIR/bluegreen"

# 4. Install config if not exists
CONFIG_EXAMPLE="${SCRIPT_DIR}/../config/config.example.yaml"
if [[ ! -f "$CONFIG_DIR/config.yaml" ]]; then
    if [[ -f "$CONFIG_EXAMPLE" ]]; then
        install -m 640 -o root -g bluegreen "$CONFIG_EXAMPLE" "$CONFIG_DIR/config.yaml"
        log_info "Installed config to $CONFIG_DIR/config.yaml"
    else
        log_warn "Config example not found, creating placeholder"
        cat > "$CONFIG_DIR/config.yaml" << 'EOF'
# Blue/Green Load Balancer Configuration
# See documentation for full options

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
  # auth_token: "${GIT_AUTH_TOKEN}"  # From environment file

deploy:
  blue_tag: "deploy/blue"
  green_tag: "deploy/green"
  active_tag: "deploy/active"

admin:
  hostname: "bluegreen-admin"
  state_dir: "/var/lib/bluegreen/tailscale"
  # auth_key: "${TS_AUTH_KEY}"  # From environment file

health:
  interval: "10s"
  timeout: "5s"

metrics:
  history_duration: "24h"
  history_resolution: "1m"
EOF
        chown root:bluegreen "$CONFIG_DIR/config.yaml"
        chmod 640 "$CONFIG_DIR/config.yaml"
    fi
else
    log_info "Config already exists at $CONFIG_DIR/config.yaml"
fi

# 5. Create environment file template
if [[ ! -f "$CONFIG_DIR/bluegreen.env" ]]; then
    cat > "$CONFIG_DIR/bluegreen.env" << 'EOF'
# Blue/Green Load Balancer Secrets
# This file contains sensitive credentials - keep permissions restricted

# Tailscale authentication key
# Generate at: https://login.tailscale.com/admin/settings/keys
# For Headscale, use your Headscale API key
TS_AUTH_KEY=

# Git authentication token (for private repos)
# GitHub: https://github.com/settings/tokens
# GIT_AUTH_TOKEN=

# Webhook secret (optional, for git webhook signature verification)
# WEBHOOK_KEY=
EOF
    chmod 600 "$CONFIG_DIR/bluegreen.env"
    chown root:bluegreen "$CONFIG_DIR/bluegreen.env"
    log_info "Created environment file template at $CONFIG_DIR/bluegreen.env"
else
    log_info "Environment file already exists at $CONFIG_DIR/bluegreen.env"
fi

# 6. Set directory ownership
chown -R bluegreen:bluegreen "$STATE_DIR"
chown root:bluegreen "$CONFIG_DIR"
chmod 750 "$CONFIG_DIR"
chmod 750 "$STATE_DIR"
log_info "Set directory permissions"

# 7. Install systemd service
SERVICE_SRC="${SCRIPT_DIR}/bluegreen.service"
if [[ -f "$SERVICE_SRC" ]]; then
    install -m 644 "$SERVICE_SRC" "$SERVICE_FILE"
    log_info "Installed systemd service"
else
    log_error "Service file not found: $SERVICE_SRC"
    exit 1
fi

# 8. Reload systemd
systemctl daemon-reload
log_info "Reloaded systemd"

echo ""
echo -e "${GREEN}Installation complete!${NC}"
echo ""
echo "Next steps:"
echo "  1. Edit $CONFIG_DIR/config.yaml with your settings"
echo "  2. Add your Tailscale auth key to $CONFIG_DIR/bluegreen.env"
echo "  3. Ensure cloudflared.service is installed and running"
echo "  4. Enable and start the service:"
echo "     systemctl enable --now bluegreen"
echo ""
echo "Useful commands:"
echo "  systemctl status bluegreen     # Check service status"
echo "  journalctl -u bluegreen -f     # Follow logs"
echo "  journalctl -u bluegreen -u cloudflared -f  # Combined logs"
echo ""
