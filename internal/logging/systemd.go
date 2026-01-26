package logging

import (
	"net"
	"os"

	"github.com/coreos/go-systemd/v22/activation"
	"github.com/coreos/go-systemd/v22/daemon"
)

// NotifyReady sends the READY=1 notification to systemd.
// This should be called when the service is fully initialized and ready to serve.
func NotifyReady() error {
	_, err := daemon.SdNotify(false, daemon.SdNotifyReady)
	return err
}

// NotifyReloading sends the RELOADING=1 notification to systemd.
// This should be called when the service starts reloading configuration.
func NotifyReloading() error {
	_, err := daemon.SdNotify(false, daemon.SdNotifyReloading)
	return err
}

// NotifyStopping sends the STOPPING=1 notification to systemd.
// This should be called when the service begins graceful shutdown.
func NotifyStopping() error {
	_, err := daemon.SdNotify(false, daemon.SdNotifyStopping)
	return err
}

// NotifyStatus sends a STATUS= notification to systemd with the given message.
// This updates the status shown by "systemctl status".
func NotifyStatus(status string) error {
	_, err := daemon.SdNotify(false, "STATUS="+status)
	return err
}

// NotifyWatchdog sends the WATCHDOG=1 notification to systemd.
// This should be called periodically if WatchdogSec is configured.
func NotifyWatchdog() error {
	_, err := daemon.SdNotify(false, daemon.SdNotifyWatchdog)
	return err
}

// IsUnderSystemd returns true if the process is running under systemd.
// This checks for the presence of NOTIFY_SOCKET or INVOCATION_ID.
func IsUnderSystemd() bool {
	// Check for systemd notify socket
	if os.Getenv("NOTIFY_SOCKET") != "" {
		return true
	}
	// Check for invocation ID (set by systemd for all services)
	if os.Getenv("INVOCATION_ID") != "" {
		return true
	}
	return false
}

// IsSocketActivated returns true if the process was socket-activated by systemd.
// This checks for the presence of LISTEN_FDS environment variable.
func IsSocketActivated() bool {
	return os.Getenv("LISTEN_FDS") != ""
}

// GetSocketActivationListeners returns listeners passed by systemd socket activation.
// Returns nil if not socket-activated or if there are no listeners.
// The returned listeners are ready to use with http.Server.Serve().
func GetSocketActivationListeners() ([]net.Listener, error) {
	return activation.Listeners()
}

// GetSocketActivationListenersByName returns listeners by their socket unit name.
// This is useful when multiple sockets are configured.
// The name should match the socket unit filename without the .socket extension.
func GetSocketActivationListenersByName() (map[string][]net.Listener, error) {
	return activation.ListenersWithNames()
}
