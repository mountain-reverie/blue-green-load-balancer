//go:build integration

package headscale

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/admin"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/config"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/metrics"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/switcher"
)

const (
	testUser          = "testuser"
	adminHostname     = "bluegreen-admin-test"
	testClientName    = "test-client"
	testTimeout       = 5 * time.Minute
	peerWaitTimeout   = 2 * time.Minute
)

// TestSuite holds all Headscale test infrastructure.
type TestSuite struct {
	ctx        context.Context
	cancel     context.CancelFunc
	headscale  *Container
	adminSrv   *admin.Server
	testClient *TestClient
	tmpDir     string
	t          *testing.T
}

// setupTestSuite creates and initializes the Headscale test suite.
func setupTestSuite(t *testing.T) *TestSuite {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	suite := &TestSuite{
		ctx:    ctx,
		cancel: cancel,
		t:      t,
	}

	var err error

	// Create temp directory for tsnet state
	suite.tmpDir, err = os.MkdirTemp("", "headscale-test-*")
	require.NoError(t, err, "failed to create temp directory")

	// 1. Start Headscale container
	t.Log("Starting Headscale container...")
	suite.headscale, err = StartHeadscale(ctx)
	require.NoError(t, err, "failed to start Headscale container")
	t.Logf("Headscale running at %s", suite.headscale.URL)

	// 2. Create user and preauth keys
	t.Log("Creating Headscale user...")
	err = suite.headscale.CreateUser(ctx, testUser)
	require.NoError(t, err, "failed to create Headscale user")

	t.Log("Creating admin preauth key...")
	adminKey, err := suite.headscale.CreatePreauthKey(ctx, testUser)
	require.NoError(t, err, "failed to create admin preauth key")

	t.Log("Creating test client preauth key...")
	clientKey, err := suite.headscale.CreatePreauthKey(ctx, testUser)
	require.NoError(t, err, "failed to create test client preauth key")

	// 3. Create admin server config
	cfg := &config.Config{
		Services: config.ServiceConfig{
			Blue: config.ServiceEndpoint{
				URL:        "http://localhost:3001",
				HealthPath: "/health",
			},
			Green: config.ServiceEndpoint{
				URL:        "http://localhost:3002",
				HealthPath: "/health",
			},
		},
		Proxy: config.ProxyConfig{
			ListenAddr:   ":8080",
			DrainTimeout: 30 * time.Second,
		},
		Admin: config.AdminConfig{
			Hostname:   adminHostname,
			StateDir:   filepath.Join(suite.tmpDir, "admin-state"),
			AuthKey:    adminKey,
			ControlURL: suite.headscale.URL,
			Ephemeral:  true,
		},
		Health: config.HealthConfig{
			Interval: 10 * time.Second,
			Timeout:  5 * time.Second,
		},
		Metrics: config.MetricsConfig{
			HistoryDuration:   24 * time.Hour,
			HistoryResolution: 1 * time.Minute,
		},
	}

	// 4. Create and start admin server
	t.Log("Starting admin server with tsnet...")
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Create minimal switcher and metrics for admin server
	sw := switcher.New(cfg, logger, nil)
	metricsCollector := metrics.NewCollector(cfg, logger)

	suite.adminSrv = admin.NewServer(cfg, logger, sw, metricsCollector)
	err = suite.adminSrv.Start(ctx)
	require.NoError(t, err, "failed to start admin server")
	t.Log("Admin server started")

	// 5. Create test client
	t.Log("Creating test client...")
	clientStateDir := filepath.Join(suite.tmpDir, "client-state")
	suite.testClient, err = NewTestClient(ctx, testClientName, clientStateDir, suite.headscale.URL, clientKey)
	require.NoError(t, err, "failed to create test client")

	clientIP, err := suite.testClient.TailscaleIP(ctx)
	require.NoError(t, err, "failed to get test client IP")
	t.Logf("Test client connected with IP: %s", clientIP)

	// 6. Wait for peer visibility
	t.Logf("Waiting for admin server peer (%s)...", adminHostname)
	peerCtx, peerCancel := context.WithTimeout(ctx, peerWaitTimeout)
	defer peerCancel()
	err = suite.testClient.WaitForPeer(peerCtx, adminHostname)
	require.NoError(t, err, "failed to find admin server peer")
	t.Log("Admin server peer visible")

	return suite
}

// Cleanup cleans up all test resources.
func (s *TestSuite) Cleanup() {
	if s.testClient != nil {
		s.testClient.Close()
	}
	if s.adminSrv != nil {
		s.adminSrv.Stop()
	}
	if s.headscale != nil {
		s.headscale.Terminate(s.ctx)
	}
	if s.tmpDir != "" {
		os.RemoveAll(s.tmpDir)
	}
	s.cancel()
}

// adminURL returns the URL for an admin endpoint using MagicDNS.
func (s *TestSuite) adminURL(path string) string {
	return "http://" + adminHostname + ":80" + path
}

// adminURLByIP returns the URL for an admin endpoint using direct Tailscale IP.
func (s *TestSuite) adminURLByIP(path string) (string, error) {
	ip, err := s.headscale.GetTailscaleIP(s.ctx, adminHostname)
	if err != nil {
		return "", err
	}
	return "http://" + ip + ":80" + path, nil
}

func TestHeadscaleAdminStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Headscale integration test in short mode")
	}

	suite := setupTestSuite(t)
	defer suite.Cleanup()

	// Test /api/status endpoint via Tailscale network
	t.Log("Testing /api/status via Tailscale...")

	resp, err := suite.testClient.Get(suite.ctx, suite.adminURL("/api/status"))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Verify response contains expected fields
	var status map[string]interface{}
	err = json.Unmarshal(body, &status)
	require.NoError(t, err, "failed to parse status response")

	assert.Contains(t, status, "active")
	t.Logf("Status response: %s", string(body))
}

func TestHeadscaleAdminSwitch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Headscale integration test in short mode")
	}

	suite := setupTestSuite(t)
	defer suite.Cleanup()

	// Test /api/switch endpoint via Tailscale network
	t.Log("Testing /api/switch via Tailscale...")

	resp, err := suite.testClient.Post(suite.ctx, suite.adminURL("/api/switch"),
		"application/json", strings.NewReader(`{"target":"green"}`))
	require.NoError(t, err)
	defer resp.Body.Close()

	// Switch may fail due to no real backends, but we're testing connectivity
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	t.Logf("Switch response: %d - %s", resp.StatusCode, string(body))

	// Any response (success or error) proves the network path works
	assert.True(t, resp.StatusCode == http.StatusOK ||
		resp.StatusCode == http.StatusBadRequest ||
		resp.StatusCode == http.StatusServiceUnavailable,
		"unexpected status code: %d", resp.StatusCode)
}

func TestHeadscaleDashboardUI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Headscale integration test in short mode")
	}

	suite := setupTestSuite(t)
	defer suite.Cleanup()

	// Test dashboard root via Tailscale network
	t.Log("Testing dashboard UI via Tailscale...")

	resp, err := suite.testClient.Get(suite.ctx, suite.adminURL("/"))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Dashboard should return HTML
	contentType := resp.Header.Get("Content-Type")
	assert.Contains(t, contentType, "text/html", "expected HTML content type")
	assert.Contains(t, string(body), "Blue/Green", "expected dashboard content")
	t.Log("Dashboard UI accessible")
}

func TestHeadscaleMetrics(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Headscale integration test in short mode")
	}

	suite := setupTestSuite(t)
	defer suite.Cleanup()

	// Test /api/metrics endpoint via Tailscale network
	t.Log("Testing /api/metrics via Tailscale...")

	resp, err := suite.testClient.Get(suite.ctx, suite.adminURL("/api/metrics"))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var metrics map[string]interface{}
	err = json.Unmarshal(body, &metrics)
	require.NoError(t, err, "failed to parse metrics response")

	t.Logf("Metrics response: %s", string(body))
}

func TestHeadscaleMetricsHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Headscale integration test in short mode")
	}

	suite := setupTestSuite(t)
	defer suite.Cleanup()

	// Test /api/metrics/history endpoint via Tailscale network
	t.Log("Testing /api/metrics/history via Tailscale...")

	resp, err := suite.testClient.Get(suite.ctx, suite.adminURL("/api/metrics/history"))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// History should return JSON array
	var history interface{}
	err = json.Unmarshal(body, &history)
	require.NoError(t, err, "failed to parse history response")

	t.Logf("History response: %s", string(body))
}

func TestHeadscaleSSE(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Headscale integration test in short mode")
	}

	suite := setupTestSuite(t)
	defer suite.Cleanup()

	// Test /events SSE endpoint via Tailscale network
	t.Log("Testing /events SSE via Tailscale...")

	// Create a context with shorter timeout for SSE test
	sseCtx, cancel := context.WithTimeout(suite.ctx, 10*time.Second)
	defer cancel()

	resp, err := suite.testClient.Get(sseCtx, suite.adminURL("/events"))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream",
		"expected SSE content type")

	// Read one event or timeout
	reader := bufio.NewReader(resp.Body)
	eventReceived := false

	for {
		select {
		case <-sseCtx.Done():
			// Timeout is OK for SSE - we just want to verify connection works
			t.Log("SSE connection verified (timed out waiting for events)")
			return
		default:
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF || sseCtx.Err() != nil {
					break
				}
				t.Logf("SSE read error: %v", err)
				break
			}
			if strings.HasPrefix(line, "data:") {
				eventReceived = true
				t.Logf("Received SSE event: %s", strings.TrimSpace(line))
				return
			}
		}
		if eventReceived {
			break
		}
	}

	t.Log("SSE endpoint accessible")
}

func TestHeadscaleDirectIP(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Headscale integration test in short mode")
	}

	suite := setupTestSuite(t)
	defer suite.Cleanup()

	// Test access via direct Tailscale IP (fallback if MagicDNS fails)
	t.Log("Testing access via direct Tailscale IP...")

	url, err := suite.adminURLByIP("/api/status")
	if err != nil {
		t.Skipf("Could not get admin server IP: %v", err)
	}

	resp, err := suite.testClient.Get(suite.ctx, url)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	t.Logf("Direct IP access successful: %s", url)
}

func TestHeadscaleOpenAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Headscale integration test in short mode")
	}

	suite := setupTestSuite(t)
	defer suite.Cleanup()

	// Test /api/openapi endpoint via Tailscale network
	t.Log("Testing /api/openapi via Tailscale...")

	resp, err := suite.testClient.Get(suite.ctx, suite.adminURL("/api/openapi"))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// OpenAPI spec should be valid JSON
	var spec map[string]interface{}
	err = json.Unmarshal(body, &spec)
	require.NoError(t, err, "failed to parse OpenAPI spec")

	assert.Contains(t, spec, "openapi", "expected OpenAPI spec")
	assert.Contains(t, spec, "info", "expected OpenAPI info")
	t.Log("OpenAPI spec accessible")
}
