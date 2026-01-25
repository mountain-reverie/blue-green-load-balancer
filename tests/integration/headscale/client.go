//go:build integration

package headscale

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"tailscale.com/client/tailscale"
	"tailscale.com/tsnet"
)

// TestClient is an HTTP client that connects via the Tailscale network.
type TestClient struct {
	tsServer   *tsnet.Server
	httpClient *http.Client
}

// NewTestClient creates a new test client connected to a Headscale server.
func NewTestClient(ctx context.Context, hostname, stateDir, controlURL, authKey string) (*TestClient, error) {
	srv := &tsnet.Server{
		Hostname:   hostname,
		Dir:        stateDir,
		ControlURL: controlURL,
		AuthKey:    authKey,
		Ephemeral:  true,
	}

	if err := srv.Start(); err != nil {
		return nil, fmt.Errorf("starting tsnet server: %w", err)
	}

	// Wait for Tailscale IP assignment
	lc, err := srv.LocalClient()
	if err != nil {
		srv.Close()
		return nil, fmt.Errorf("getting local client: %w", err)
	}

	if err := waitForTailscaleIP(ctx, lc); err != nil {
		srv.Close()
		return nil, fmt.Errorf("waiting for Tailscale IP: %w", err)
	}

	return &TestClient{
		tsServer: srv,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: srv.Dial,
			},
			Timeout: 30 * time.Second,
		},
	}, nil
}

// waitForTailscaleIP waits until the tsnet server has a Tailscale IP assigned.
func waitForTailscaleIP(ctx context.Context, lc *tailscale.LocalClient) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			status, err := lc.Status(ctx)
			if err != nil {
				continue
			}
			if status.Self != nil && len(status.Self.TailscaleIPs) > 0 {
				return nil
			}
		}
	}
}

// Get performs an HTTP GET request via the Tailscale network.
func (c *TestClient) Get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.httpClient.Do(req)
}

// Post performs an HTTP POST request via the Tailscale network.
func (c *TestClient) Post(ctx context.Context, url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return c.httpClient.Do(req)
}

// Close closes the test client and its tsnet server.
func (c *TestClient) Close() error {
	return c.tsServer.Close()
}

// TailscaleIP returns the Tailscale IP of this client.
func (c *TestClient) TailscaleIP(ctx context.Context) (string, error) {
	lc, err := c.tsServer.LocalClient()
	if err != nil {
		return "", fmt.Errorf("getting local client: %w", err)
	}

	status, err := lc.Status(ctx)
	if err != nil {
		return "", fmt.Errorf("getting status: %w", err)
	}

	if status.Self == nil || len(status.Self.TailscaleIPs) == 0 {
		return "", fmt.Errorf("no Tailscale IPs assigned")
	}

	return status.Self.TailscaleIPs[0].String(), nil
}

// WaitForPeer waits until a peer with the given hostname is visible.
func (c *TestClient) WaitForPeer(ctx context.Context, hostname string) error {
	lc, err := c.tsServer.LocalClient()
	if err != nil {
		return fmt.Errorf("getting local client: %w", err)
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			status, err := lc.Status(ctx)
			if err != nil {
				continue
			}
			for _, peer := range status.Peer {
				if peer.HostName == hostname {
					return nil
				}
			}
		}
	}
}
