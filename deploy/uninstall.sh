#!/bin/bash
# uninstall.sh - Uninstall Blue/Green Load Balancer
#
# This script removes the blue/green load balancer systemd service.
# Run as root or with sudo.
#
# Usage:
#   sudo ./uninstall.sh          # Uninstall, keep config
#   sudo ./uninstall.sh --purge  # Uninstall and remove all data

set -euo pipefail

# Configuration
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/bluegreen"
STATE_DIR="/var/lib/bluegreen"
SERVICE_FILE="/etc/systemd/system/bluegreen.service"

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

PURGE=false
if [[ "${1:-}" == "--purge" ]]; then
    PURGE=true
fi

# Check if running as root
if [[ $EUID -ne 0 ]]; then
    log_error "This script must be run as root"
    exit 1
fi

log_info "Uninstalling Blue/Green Load Balancer..."

# 1. Stop and disable service
if systemctl is-active --quiet bluegreen 2>/dev/null; then
    systemctl stop bluegreen
    log_info "Stopped bluegreen service"
fi

if systemctl is-enabled --quiet bluegreen 2>/dev/null; then
    systemctl disable bluegreen
    log_info "Disabled bluegreen service"
fi

# 2. Remove service file
if [[ -f "$SERVICE_FILE" ]]; then
    rm -f "$SERVICE_FILE"
    systemctl daemon-reload
    log_info "Removed systemd service file"
fi

# 3. Remove binary
if [[ -f "$INSTALL_DIR/bluegreen" ]]; then
    rm -f "$INSTALL_DIR/bluegreen"
    log_info "Removed binary"
fi

# 4. Optionally remove config and state
if [[ "$PURGE" == true ]]; then
    if [[ -d "$CONFIG_DIR" ]]; then
        rm -rf "$CONFIG_DIR"
        log_info "Removed configuration directory"
    fi

    if [[ -d "$STATE_DIR" ]]; then
        rm -rf "$STATE_DIR"
        log_info "Removed state directory"
    fi

    # Remove user
    if id -u bluegreen &>/dev/null; then
        userdel bluegreen 2>/dev/null || true
        log_info "Removed bluegreen user"
    fi

    # Remove group if it still exists
    if getent group bluegreen &>/dev/null; then
        groupdel bluegreen 2>/dev/null || true
        log_info "Removed bluegreen group"
    fi
else
    log_warn "Configuration and state preserved in $CONFIG_DIR and $STATE_DIR"
    log_warn "Run with --purge to remove all data"
fi

echo ""
echo -e "${GREEN}Uninstallation complete!${NC}"
