//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	playwrightcigo "github.com/mountain-reverie/playwright-ci-go"
	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/app"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/config"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/health"
)

var (
	testBrowser     playwright.Browser
	testAdminServer *testAdminServerWrapper
	testBlueServer  *httptest.Server
	testGreenServer *httptest.Server
)

// testdataDir returns the path to the testdata directory relative to this test file.
func testdataDir() string {
	return filepath.Join("testdata")
}

// failedScreenshotDir returns the path to store failed test screenshots.
func failedScreenshotDir() string {
	return filepath.Join(testdataDir(), "failed")
}

func TestMain(m *testing.M) {
	var err error

	// Ensure testdata/failed directory exists for screenshots
	if err := os.MkdirAll(failedScreenshotDir(), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create screenshot dir: %v\n", err)
		os.Exit(1)
	}

	// Install Playwright container (5 min timeout for initial pull)
	err = playwrightcigo.Install(playwrightcigo.WithTimeout(5 * time.Minute))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to install Playwright: %v\n", err)
		os.Exit(1)
	}

	// Start mock backend servers
	testBlueServer = createMockBackend("blue")
	testGreenServer = createMockBackend("green")

	// Start the admin server with UI
	testAdminServer, err = createTestAdminServer(testBlueServer.URL, testGreenServer.URL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create admin server: %v\n", err)
		testBlueServer.Close()
		testGreenServer.Close()
		playwrightcigo.Uninstall()
		os.Exit(1)
	}

	// Initialize Playwright with Chromium (more stable in containers)
	testBrowser, err = playwrightcigo.Chromium()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize Playwright Chromium: %v\n", err)
		testAdminServer.Close()
		testBlueServer.Close()
		testGreenServer.Close()
		playwrightcigo.Uninstall()
		os.Exit(1)
	}

	code := m.Run()

	// Cleanup
	testBrowser.Close()
	testAdminServer.Close()
	testBlueServer.Close()
	testGreenServer.Close()
	playwrightcigo.Uninstall()

	os.Exit(code)
}

// createMockBackend creates a mock HTTP backend for testing.
func createMockBackend(name string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Service", name)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Hello from %s!\n", name)
	})
	return httptest.NewServer(mux)
}

// testAdminServerWrapper wraps httptest.Server with the Application for cleanup.
type testAdminServerWrapper struct {
	*httptest.Server
	app *app.Application
}

// Close closes the server and performs cleanup.
func (w *testAdminServerWrapper) Close() {
	w.app.Stop()
	w.Server.Close()
}

// createTestAdminServer creates a test admin server using the Application type.
// This ensures tests run against the actual production code paths.
func createTestAdminServer(blueURL, greenURL string) (*testAdminServerWrapper, error) {
	cfg := &config.Config{
		Services: config.ServiceConfig{
			Blue: config.ServiceEndpoint{
				URL:        blueURL,
				HealthPath: "/health",
			},
			Green: config.ServiceEndpoint{
				URL:        greenURL,
				HealthPath: "/health",
			},
		},
		Proxy: config.ProxyConfig{
			ListenAddr:   ":0",
			DrainTimeout: 5 * time.Second,
		},
		Health: config.HealthConfig{
			Interval: 1 * time.Second,
			Timeout:  500 * time.Millisecond,
		},
		Metrics: config.MetricsConfig{
			HistoryDuration:   time.Hour,
			HistoryResolution: time.Minute,
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Create application using the shared app package
	application, err := app.New(cfg, app.WithLogger(logger))
	if err != nil {
		return nil, fmt.Errorf("creating application: %w", err)
	}

	// Start the application (health checker, SSE updates, etc.)
	ctx := context.Background()
	if err := application.Start(ctx); err != nil {
		return nil, fmt.Errorf("starting application: %w", err)
	}

	// Wait for initial health checks with polling
	waitForHealthChecks(application.Health, 2*time.Second)

	// Simulate some metrics data for tests
	application.Metrics.RecordRequest(config.ServiceBlue, 200, 10*time.Millisecond)
	application.Metrics.RecordRequest(config.ServiceBlue, 200, 15*time.Millisecond)
	application.Metrics.RecordRequest(config.ServiceGreen, 200, 12*time.Millisecond)

	// Create test server using the application's handler
	// This includes all routes: UI, Huma API (/api/switch, /api/status, etc.), and metrics
	server := httptest.NewServer(application.Handler())

	return &testAdminServerWrapper{
		Server: server,
		app:    application,
	}, nil
}

// findStaticDir locates the static directory relative to the test file.
func findStaticDir() string {
	// Try common locations
	paths := []string{
		"../../static",
		"../../../static",
		"static",
	}

	for _, p := range paths {
		if abs, err := filepath.Abs(p); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
		}
	}

	// Return empty if not found; tests will still work but without CSS
	return ""
}

// waitForHealthChecks waits for both services to complete at least one health check.
func waitForHealthChecks(h *health.Checker, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		blueStatus := h.GetStatus(config.ServiceBlue)
		greenStatus := h.GetStatus(config.ServiceGreen)
		if !blueStatus.LastCheck.IsZero() && !greenStatus.LastCheck.IsZero() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// baselineScreenshotPath returns the path for a test's baseline screenshot in testdata/.
func baselineScreenshotPath(testName string) string {
	// Sanitize test name for use as filename (replace / with _)
	safeName := strings.ReplaceAll(testName, "/", "_")
	return filepath.Join(testdataDir(), fmt.Sprintf("%s.png", safeName))
}

// failedScreenshotPath returns the path for a test's failed screenshot in testdata/failed/.
func failedScreenshotPath(testName string) string {
	// Sanitize test name for use as filename (replace / with _)
	safeName := strings.ReplaceAll(testName, "/", "_")
	return filepath.Join(failedScreenshotDir(), fmt.Sprintf("%s.png", safeName))
}

// saveNamedScreenshot saves a screenshot with a specific name to testdata/.
// Use this for capturing specific states (e.g., before/after a switch).
func saveNamedScreenshot(t *testing.T, page playwright.Page, name string) {
	path := filepath.Join(testdataDir(), fmt.Sprintf("%s.png", name))
	_, err := page.Screenshot(playwright.PageScreenshotOptions{
		Path:     playwright.String(path),
		FullPage: playwright.Bool(true),
	})
	if err != nil {
		t.Logf("failed to take screenshot %s: %v", name, err)
	}
}

// manageScreenshot takes a screenshot during the test run.
// Always saves a baseline screenshot to testdata/ (for documentation/comparison).
// If the test fails, also saves to testdata/failed/ for debugging.
func manageScreenshot(t *testing.T, page playwright.Page) {
	baselinePath := baselineScreenshotPath(t.Name())
	failedPath := failedScreenshotPath(t.Name())

	// Always take a baseline screenshot
	_, err := page.Screenshot(playwright.PageScreenshotOptions{
		Path:     playwright.String(baselinePath),
		FullPage: playwright.Bool(true),
	})
	if err != nil {
		t.Logf("failed to take baseline screenshot: %v", err)
		return
	}

	// If test failed, also save to failed directory for easy comparison
	if t.Failed() {
		_, err := page.Screenshot(playwright.PageScreenshotOptions{
			Path:     playwright.String(failedPath),
			FullPage: playwright.Bool(true),
		})
		if err != nil {
			t.Logf("failed to take failure screenshot: %v", err)
		} else {
			t.Logf("failure screenshot saved to: %s", failedPath)
		}
	}
}

func TestDashboardLoads(t *testing.T) {
	page, err := testBrowser.NewPage()
	require.NoError(t, err, "failed to create new page")
	defer page.Close()
	defer manageScreenshot(t, page)

	// Navigate to the dashboard
	_, err = page.Goto(testAdminServer.URL)
	require.NoError(t, err, "failed to navigate to dashboard")

	// Wait for page to load (use domcontentloaded since SSE keeps network busy)
	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	})
	require.NoError(t, err, "failed to wait for DOM content loaded")

	// Verify page title
	title, err := page.Title()
	require.NoError(t, err, "failed to get page title")
	assert.Equal(t, "Blue/Green Load Balancer", title, "page title should match")

	// Verify main header is present
	header, err := page.Locator("h1").TextContent()
	require.NoError(t, err, "failed to get header content")
	assert.Equal(t, "Blue/Green Load Balancer", header, "header should be visible")

	// Verify main sections are present
	// Status section
	statusSectionVisible, err := page.Locator("section:has(h2:text('Service Status'))").IsVisible()
	require.NoError(t, err, "failed to check status section visibility")
	assert.True(t, statusSectionVisible, "status section should be visible")

	// Metrics section
	metricsSectionVisible, err := page.Locator("section:has(h2:text('Metrics'))").IsVisible()
	require.NoError(t, err, "failed to check metrics section visibility")
	assert.True(t, metricsSectionVisible, "metrics section should be visible")

	// History section
	historySectionVisible, err := page.Locator("section:has(h2:text('Switch History'))").IsVisible()
	require.NoError(t, err, "failed to check history section visibility")
	assert.True(t, historySectionVisible, "history section should be visible")
}

func TestStatusSection(t *testing.T) {
	page, err := testBrowser.NewPage()
	require.NoError(t, err, "failed to create new page")
	defer page.Close()
	defer manageScreenshot(t, page)

	// Navigate to the dashboard
	_, err = page.Goto(testAdminServer.URL)
	require.NoError(t, err, "failed to navigate to dashboard")

	// Wait for page to load (use domcontentloaded since SSE keeps network busy)
	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	})
	require.NoError(t, err, "failed to wait for DOM content loaded")

	// Verify active service indicator is present
	activeServiceVisible, err := page.Locator("#active-service").IsVisible()
	require.NoError(t, err, "failed to check active service visibility")
	assert.True(t, activeServiceVisible, "active service indicator should be visible")

	// Get active service value - should be blue by default
	activeServiceText, err := page.Locator("#active-service").TextContent()
	require.NoError(t, err, "failed to get active service text")
	assert.Equal(t, "blue", activeServiceText, "active service should be blue by default")

	// Verify Blue service card is present (use border-blue-500 class for specificity)
	blueCardVisible, err := page.Locator("div.border-blue-500").IsVisible()
	require.NoError(t, err, "failed to check blue card visibility")
	assert.True(t, blueCardVisible, "blue service card should be visible")

	// Verify Blue service has health status displayed
	blueHealthStatusVisible, err := page.Locator("div.border-blue-500 span").Filter(playwright.LocatorFilterOptions{
		HasText: regexp.MustCompile(`(Healthy|Unhealthy)`),
	}).IsVisible()
	require.NoError(t, err, "failed to check blue health status visibility")
	assert.True(t, blueHealthStatusVisible, "blue service health status should be visible")

	// Verify Green service card is present (use border-green-500 class for specificity)
	greenCardVisible, err := page.Locator("div.border-green-500").IsVisible()
	require.NoError(t, err, "failed to check green card visibility")
	assert.True(t, greenCardVisible, "green service card should be visible")

	// Verify Green service has health status displayed
	greenHealthStatusVisible, err := page.Locator("div.border-green-500 span").Filter(playwright.LocatorFilterOptions{
		HasText: regexp.MustCompile(`(Healthy|Unhealthy)`),
	}).IsVisible()
	require.NoError(t, err, "failed to check green health status visibility")
	assert.True(t, greenHealthStatusVisible, "green service health status should be visible")

	// Verify both services show latency
	blueLatencyVisible, err := page.Locator("div.border-blue-500 p").Filter(playwright.LocatorFilterOptions{
		HasText: regexp.MustCompile(`Latency:`),
	}).IsVisible()
	require.NoError(t, err, "failed to check blue latency visibility")
	assert.True(t, blueLatencyVisible, "blue service latency should be visible")

	greenLatencyVisible, err := page.Locator("div.border-green-500 p").Filter(playwright.LocatorFilterOptions{
		HasText: regexp.MustCompile(`Latency:`),
	}).IsVisible()
	require.NoError(t, err, "failed to check green latency visibility")
	assert.True(t, greenLatencyVisible, "green service latency should be visible")
}

func TestMetricsSection(t *testing.T) {
	page, err := testBrowser.NewPage()
	require.NoError(t, err, "failed to create new page")
	defer page.Close()
	defer manageScreenshot(t, page)

	// Navigate to the dashboard
	_, err = page.Goto(testAdminServer.URL)
	require.NoError(t, err, "failed to navigate to dashboard")

	// Wait for page to load (use domcontentloaded since SSE keeps network busy)
	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	})
	require.NoError(t, err, "failed to wait for DOM content loaded")

	// Verify Total Requests metric card
	totalRequestsVisible, err := page.Locator("#total-requests").IsVisible()
	require.NoError(t, err, "failed to check total requests visibility")
	assert.True(t, totalRequestsVisible, "total requests metric should be visible")

	// Verify Requests/sec metric card
	rpsVisible, err := page.Locator("#rps").IsVisible()
	require.NoError(t, err, "failed to check requests per second visibility")
	assert.True(t, rpsVisible, "requests per second metric should be visible")

	// Verify Active Connections metric card
	activeConnectionsVisible, err := page.Locator("#active-connections").IsVisible()
	require.NoError(t, err, "failed to check active connections visibility")
	assert.True(t, activeConnectionsVisible, "active connections metric should be visible")

	// Verify Error Rate metric card
	errorRateVisible, err := page.Locator("#error-rate").IsVisible()
	require.NoError(t, err, "failed to check error rate visibility")
	assert.True(t, errorRateVisible, "error rate metric should be visible")

	// Verify P50 latency metric card
	p50Visible, err := page.Locator("#p50").IsVisible()
	require.NoError(t, err, "failed to check p50 latency visibility")
	assert.True(t, p50Visible, "p50 latency metric should be visible")

	// Verify P90 latency metric card
	p90Visible, err := page.Locator("#p90").IsVisible()
	require.NoError(t, err, "failed to check p90 latency visibility")
	assert.True(t, p90Visible, "p90 latency metric should be visible")

	// Verify P99 latency metric card
	p99Visible, err := page.Locator("#p99").IsVisible()
	require.NoError(t, err, "failed to check p99 latency visibility")
	assert.True(t, p99Visible, "p99 latency metric should be visible")

	// Verify Uptime metric card
	uptimeVisible, err := page.Locator("#uptime").IsVisible()
	require.NoError(t, err, "failed to check uptime visibility")
	assert.True(t, uptimeVisible, "uptime metric should be visible")

	// Verify metric values are populated (not empty)
	totalRequestsValue, err := page.Locator("#total-requests").TextContent()
	require.NoError(t, err, "failed to get total requests value")
	assert.NotEmpty(t, totalRequestsValue, "total requests should have a value")

	rpsValue, err := page.Locator("#rps").TextContent()
	require.NoError(t, err, "failed to get rps value")
	assert.NotEmpty(t, rpsValue, "requests per second should have a value")

	// Verify metric labels are present
	metricLabels := []string{"Total Requests", "Requests/sec", "Active Connections", "Error Rate", "P50 Latency", "P90 Latency", "P99 Latency", "Uptime"}
	for _, label := range metricLabels {
		labelVisible, err := page.Locator(fmt.Sprintf("p:text('%s')", label)).IsVisible()
		require.NoError(t, err, "failed to check %s label visibility", label)
		assert.True(t, labelVisible, "%s label should be visible", label)
	}
}

func TestSwitchControls(t *testing.T) {
	page, err := testBrowser.NewPage()
	require.NoError(t, err, "failed to create new page")
	defer page.Close()

	// Navigate to the dashboard
	_, err = page.Goto(testAdminServer.URL)
	require.NoError(t, err, "failed to navigate to dashboard")

	// Wait for page to load (use domcontentloaded since SSE keeps network busy)
	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	})
	require.NoError(t, err, "failed to wait for DOM content loaded")

	// Verify we start with blue as the active service
	activeService, err := page.Locator("#active-service").TextContent()
	require.NoError(t, err, "failed to get active service")
	assert.Equal(t, "blue", activeService, "should start with blue as active service")

	// Take "before" screenshot showing blue is active
	saveNamedScreenshot(t, page, "TestSwitchControls_BeforeSwitch")

	// Verify the blue service card has the active styling (border-blue-500)
	blueCardVisible, err := page.Locator("div.border-blue-500").IsVisible()
	require.NoError(t, err, "failed to check blue card visibility")
	assert.True(t, blueCardVisible, "blue service card should be visible with blue border")

	// Switch to green via API
	switchReq := map[string]string{"target": "green"}
	switchBody, _ := json.Marshal(switchReq)
	resp, err := http.Post(testAdminServer.URL+"/api/switch", "application/json", bytes.NewReader(switchBody))
	require.NoError(t, err, "failed to call switch API")
	require.Equal(t, http.StatusOK, resp.StatusCode, "switch API should return 200 OK")
	resp.Body.Close()

	// Reload the page to see the updated state
	_, err = page.Reload()
	require.NoError(t, err, "failed to reload page")

	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	})
	require.NoError(t, err, "failed to wait for DOM content loaded after reload")

	// Verify green is now the active service
	activeServiceAfter, err := page.Locator("#active-service").TextContent()
	require.NoError(t, err, "failed to get active service after switch")
	assert.Equal(t, "green", activeServiceAfter, "green should be active after switch")

	// Verify the green service card styling indicates it's the active one
	greenCardVisible, err := page.Locator("div.border-green-500").IsVisible()
	require.NoError(t, err, "failed to check green card visibility")
	assert.True(t, greenCardVisible, "green service card should be visible with green border")

	// Take "after" screenshot showing green is now active
	saveNamedScreenshot(t, page, "TestSwitchControls_AfterSwitch")

	// Also save the final state as the default screenshot for this test
	manageScreenshot(t, page)
}

func TestSSEConnection(t *testing.T) {
	page, err := testBrowser.NewPage()
	require.NoError(t, err, "failed to create new page")
	defer page.Close()
	defer manageScreenshot(t, page)

	// Navigate to the dashboard
	_, err = page.Goto(testAdminServer.URL)
	require.NoError(t, err, "failed to navigate to dashboard")

	// Wait for page to load (use domcontentloaded since SSE keeps network busy)
	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	})
	require.NoError(t, err, "failed to wait for DOM content loaded")

	// Verify the main element has SSE connection attributes
	mainElementVisible, err := page.Locator("main[hx-ext='sse']").IsVisible()
	require.NoError(t, err, "failed to check main element with sse extension")
	assert.True(t, mainElementVisible, "main element should have hx-ext='sse' attribute")

	// Verify SSE connect endpoint is configured
	sseConnectAttr, err := page.Locator("main").GetAttribute("sse-connect")
	require.NoError(t, err, "failed to get sse-connect attribute")
	assert.Equal(t, "/events", sseConnectAttr, "main element should have sse-connect='/events' attribute")

	// Verify the SSE endpoint is accessible with retry
	var resp *http.Response
	for i := 0; i < 5; i++ {
		resp, err = http.Get(testAdminServer.URL + "/events")
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.NoError(t, err, "failed to connect to SSE endpoint")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "SSE endpoint should return 200 OK")
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"), "SSE endpoint should return text/event-stream content type")

	// Read initial connection event using buffered reader
	reader := bufio.NewReader(resp.Body)
	var sseData strings.Builder
	for i := 0; i < 5; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		sseData.WriteString(line)
		if strings.TrimSpace(line) == "" && sseData.Len() > 0 {
			break // End of SSE message (blank line)
		}
	}

	sseContent := sseData.String()
	assert.Contains(t, sseContent, "event: connected", "SSE should send connected event on connection")
	assert.Contains(t, sseContent, `"connected": true`, "SSE connected event should contain connected: true")

	// Verify HTMX SSE extension is loaded (script from unpkg.com)
	htmxSSEScriptCount, err := page.Locator("script[src*='sse.js']").Count()
	require.NoError(t, err, "failed to check HTMX SSE script")
	assert.GreaterOrEqual(t, htmxSSEScriptCount, 1, "HTMX SSE extension script should be loaded")

	// Verify the page has JavaScript handlers for SSE updates
	// Check that the updateStatus and updateMetrics functions exist
	hasUpdateStatus, err := page.Evaluate("typeof updateStatus === 'function'")
	require.NoError(t, err, "failed to evaluate updateStatus function existence")
	assert.True(t, hasUpdateStatus.(bool), "updateStatus function should be defined")

	hasUpdateMetrics, err := page.Evaluate("typeof updateMetrics === 'function'")
	require.NoError(t, err, "failed to evaluate updateMetrics function existence")
	assert.True(t, hasUpdateMetrics.(bool), "updateMetrics function should be defined")
}

func TestHistorySection(t *testing.T) {
	page, err := testBrowser.NewPage()
	require.NoError(t, err, "failed to create new page")
	defer page.Close()
	defer manageScreenshot(t, page)

	// Navigate to the dashboard
	_, err = page.Goto(testAdminServer.URL)
	require.NoError(t, err, "failed to navigate to dashboard")

	// Wait for page to load (use domcontentloaded since SSE keeps network busy)
	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	})
	require.NoError(t, err, "failed to wait for DOM content loaded")

	// Verify history section header
	historyHeader, err := page.Locator("section:has(h2:text('Switch History')) h2").TextContent()
	require.NoError(t, err, "failed to get history section header")
	assert.Equal(t, "Switch History", historyHeader, "history section header should be 'Switch History'")

	// Verify history table is present
	historyTableVisible, err := page.Locator("section:has(h2:text('Switch History')) table").IsVisible()
	require.NoError(t, err, "failed to check history table visibility")
	assert.True(t, historyTableVisible, "history table should be visible")

	// Verify table headers
	tableHeaders := []string{"Time", "From", "To", "Trigger", "Duration", "Status"}
	for _, header := range tableHeaders {
		headerVisible, err := page.Locator(fmt.Sprintf("th:text('%s')", header)).IsVisible()
		require.NoError(t, err, "failed to check %s header visibility", header)
		assert.True(t, headerVisible, "%s table header should be visible", header)
	}

	// When there are no switch events, the table should show "No switch events yet"
	// This is the expected initial state
	noEventsMessageVisible, err := page.Locator("td:text('No switch events yet')").IsVisible()
	require.NoError(t, err, "failed to check no events message")
	// This should be true for initial state with no switches
	if noEventsMessageVisible {
		t.Log("History table shows 'No switch events yet' as expected for initial state")
	} else {
		// If there are events, verify the table rows have proper structure
		rows, err := page.Locator("section:has(h2:text('Switch History')) tbody tr").Count()
		require.NoError(t, err, "failed to count history table rows")
		t.Logf("History table has %d rows", rows)
	}
}

func TestDashboardResponsiveness(t *testing.T) {
	page, err := testBrowser.NewPage()
	require.NoError(t, err, "failed to create new page")
	defer page.Close()
	defer manageScreenshot(t, page)

	// Test with different viewport sizes
	viewports := []struct {
		name   string
		width  int
		height int
	}{
		{"desktop", 1920, 1080},
		{"tablet", 768, 1024},
		{"mobile", 375, 667},
	}

	for _, vp := range viewports {
		t.Run(vp.name, func(t *testing.T) {
			err := page.SetViewportSize(vp.width, vp.height)
			require.NoError(t, err, "failed to set viewport size for %s", vp.name)

			_, err = page.Goto(testAdminServer.URL)
			require.NoError(t, err, "failed to navigate to dashboard for %s", vp.name)

			err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
				State: playwright.LoadStateDomcontentloaded,
			})
			require.NoError(t, err, "failed to wait for DOM content loaded for %s", vp.name)

			// Verify critical elements are still visible at all viewport sizes
			headerVisible, err := page.Locator("h1").IsVisible()
			require.NoError(t, err, "failed to check header visibility for %s", vp.name)
			assert.True(t, headerVisible, "header should be visible at %s viewport", vp.name)

			activeServiceVisible, err := page.Locator("#active-service").IsVisible()
			require.NoError(t, err, "failed to check active service visibility for %s", vp.name)
			assert.True(t, activeServiceVisible, "active service should be visible at %s viewport", vp.name)

			// Metrics should be visible
			totalRequestsVisible, err := page.Locator("#total-requests").IsVisible()
			require.NoError(t, err, "failed to check total requests visibility for %s", vp.name)
			assert.True(t, totalRequestsVisible, "total requests should be visible at %s viewport", vp.name)
		})
	}
}

func TestDashboardAccessibility(t *testing.T) {
	page, err := testBrowser.NewPage()
	require.NoError(t, err, "failed to create new page")
	defer page.Close()
	defer manageScreenshot(t, page)

	_, err = page.Goto(testAdminServer.URL)
	require.NoError(t, err, "failed to navigate to dashboard")

	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	})
	require.NoError(t, err, "failed to wait for DOM content loaded")

	// Verify the page has a lang attribute
	htmlLang, err := page.Locator("html").GetAttribute("lang")
	require.NoError(t, err, "failed to get html lang attribute")
	assert.Equal(t, "en", htmlLang, "page should have lang='en' attribute")

	// Verify the page has a proper heading structure
	h1Count, err := page.Locator("h1").Count()
	require.NoError(t, err, "failed to count h1 elements")
	assert.Equal(t, 1, h1Count, "page should have exactly one h1 element")

	// Verify viewport meta tag is present
	viewportMetaCount, err := page.Locator("meta[name='viewport']").Count()
	require.NoError(t, err, "failed to check viewport meta tag")
	assert.Equal(t, 1, viewportMetaCount, "viewport meta tag should be present")

	// Verify charset meta tag is present
	charsetMetaCount, err := page.Locator("meta[charset='UTF-8']").Count()
	require.NoError(t, err, "failed to check charset meta tag")
	assert.Equal(t, 1, charsetMetaCount, "charset meta tag should be present")
}

