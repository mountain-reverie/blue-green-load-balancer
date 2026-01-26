package bgctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

// Config holds the client configuration.
type Config struct {
	Hostname   string
	ControlURL string
	AuthKey    string
	StateDir   string
	Timeout    time.Duration
	Verbose    bool
	Logger     *slog.Logger
}

// Client connects to the admin server via Tailscale network.
type Client struct {
	tsServer   *tsnet.Server
	httpClient *http.Client
	hostname   string
	logger     *slog.Logger
}

// New creates a new client connected to the Tailscale network.
func New(ctx context.Context, cfg *Config) (*Client, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	if cfg.StateDir == "" {
		return nil, fmt.Errorf("state directory is required")
	}

	srv := &tsnet.Server{
		Hostname:   "bgctl",
		Dir:        cfg.StateDir,
		ControlURL: cfg.ControlURL,
		AuthKey:    cfg.AuthKey,
		Ephemeral:  true,
	}

	if !cfg.Verbose {
		srv.Logf = func(format string, args ...any) {}
	}

	logger.Debug("starting tsnet server",
		"state_dir", cfg.StateDir,
		"control_url", cfg.ControlURL,
	)

	if err := srv.Start(); err != nil {
		return nil, fmt.Errorf("starting tsnet server: %w", err)
	}

	lc, err := srv.LocalClient()
	if err != nil {
		_ = srv.Close()
		return nil, fmt.Errorf("getting local client: %w", err)
	}

	logger.Debug("waiting for Tailscale IP assignment")
	if err := waitForTailscaleIP(ctx, lc); err != nil {
		_ = srv.Close()
		return nil, fmt.Errorf("waiting for Tailscale IP: %w", err)
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &Client{
		tsServer: srv,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: srv.Dial,
			},
			Timeout: timeout,
		},
		hostname: cfg.Hostname,
		logger:   logger,
	}, nil
}

// waitForTailscaleIP waits until the tsnet server has a Tailscale IP assigned.
func waitForTailscaleIP(ctx context.Context, lc *local.Client) error {
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

// Close closes the client and its tsnet server.
func (c *Client) Close() error {
	if transport, ok := c.httpClient.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	return c.tsServer.Close()
}

// GetStatus retrieves the current blue/green status.
func (c *Client) GetStatus(ctx context.Context) (*StatusResponse, error) {
	url := fmt.Sprintf("http://%s/api/status", c.hostname)
	c.logger.Debug("fetching status", "url", url)

	resp, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp)
	}

	var status StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("decoding status response: %w", err)
	}

	return &status, nil
}

// GetMetrics retrieves the current metrics snapshot.
func (c *Client) GetMetrics(ctx context.Context) (*MetricsResponse, error) {
	url := fmt.Sprintf("http://%s/api/metrics", c.hostname)
	c.logger.Debug("fetching metrics", "url", url)

	resp, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp)
	}

	var metrics MetricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&metrics); err != nil {
		return nil, fmt.Errorf("decoding metrics response: %w", err)
	}

	return &metrics, nil
}

// TriggerRefresh triggers a git refresh on the admin server.
func (c *Client) TriggerRefresh(ctx context.Context) (*RefreshResponse, error) {
	url := fmt.Sprintf("http://%s/api/webhook/refresh", c.hostname)
	c.logger.Debug("triggering refresh", "url", url)

	resp, err := c.post(ctx, url, "application/json", strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp)
	}

	var refresh RefreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&refresh); err != nil {
		return nil, fmt.Errorf("decoding refresh response: %w", err)
	}

	return &refresh, nil
}

// get performs an HTTP GET request via the Tailscale network.
func (c *Client) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	return c.httpClient.Do(req)
}

// post performs an HTTP POST request via the Tailscale network.
func (c *Client) post(ctx context.Context, url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.httpClient.Do(req)
}

// parseError parses an error response from the admin API.
func (c *Client) parseError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return &APIError{
		StatusCode: resp.StatusCode,
		Message:    string(body),
	}
}

// APIError represents an error response from the admin API.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("API error (status %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("API error: status %d", e.StatusCode)
}
