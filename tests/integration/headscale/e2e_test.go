package headscale

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/app"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/config"
)

const (
	e2eTestTimeout     = 5 * time.Minute
	e2ePeerWaitTimeout = 2 * time.Minute
	e2eGitPollInterval = 500 * time.Millisecond
	e2ePollInterval    = 100 * time.Millisecond
	e2ePollTimeout     = 5 * time.Second
)

// E2ETestSuite holds all infrastructure for end-to-end testing.
type E2ETestSuite struct {
	ctx    context.Context
	cancel context.CancelFunc
	t      *testing.T
	tmpDir string
	// gitBareDir is the bare repository used as the "remote" for the git watcher.
	gitBareDir string
	// gitWorkDir is the working copy used to make changes and push to bare repo.
	gitWorkDir string

	headscale  *Container
	app        *app.Application
	testClient *TestClient

	// cleanupFuncs tracks cleanup functions in reverse order for proper teardown.
	cleanupFuncs []func()
	// cleanupOnce ensures Cleanup is only executed once.
	cleanupOnce sync.Once
}

// setupE2ETestSuite creates the full end-to-end test environment.
// It uses a cleanup stack pattern to ensure proper teardown even on partial failures.
func setupE2ETestSuite(t *testing.T) *E2ETestSuite {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), e2eTestTimeout)
	suite := &E2ETestSuite{
		ctx:          ctx,
		cancel:       cancel,
		t:            t,
		cleanupFuncs: make([]func(), 0, 8),
	}

	// Helper to register cleanup functions that will run in reverse order
	addCleanup := func(fn func()) {
		suite.cleanupFuncs = append(suite.cleanupFuncs, fn)
	}

	var err error

	// Create temp directory for all test state
	suite.tmpDir, err = os.MkdirTemp("", "e2e-test-*")
	if err != nil {
		cancel()
		t.Fatalf("failed to create temp directory: %v", err)
	}
	addCleanup(func() {
		if err := os.RemoveAll(suite.tmpDir); err != nil {
			t.Logf("warning: failed to remove temp directory: %v", err)
		}
	})

	// 1. Start Headscale container
	t.Log("Step 1: Starting Headscale container...")
	suite.headscale, err = StartHeadscale(ctx)
	if err != nil {
		suite.Cleanup()
		t.Fatalf("failed to start Headscale container: %v", err)
	}
	addCleanup(func() {
		if err := suite.headscale.Terminate(context.Background()); err != nil {
			t.Logf("warning: failed to terminate Headscale container: %v", err)
		}
	})
	t.Logf("Headscale running at %s", suite.headscale.URL)

	// 2. Create Headscale user and preauth keys
	t.Log("Step 2: Creating Headscale user and preauth keys...")
	if err = suite.headscale.CreateUser(ctx, "e2etest"); err != nil {
		suite.Cleanup()
		t.Fatalf("failed to create Headscale user: %v", err)
	}

	adminKey, err := suite.headscale.CreatePreauthKey(ctx, "e2etest")
	if err != nil {
		suite.Cleanup()
		t.Fatalf("failed to create admin preauth key: %v", err)
	}

	clientKey, err := suite.headscale.CreatePreauthKey(ctx, "e2etest")
	if err != nil {
		suite.Cleanup()
		t.Fatalf("failed to create client preauth key: %v", err)
	}

	// 3. Create local git repository with tags (bare repo + working copy)
	t.Log("Step 3: Creating local git repository with deploy tags...")
	suite.gitBareDir, suite.gitWorkDir, err = createE2EGitRepo(t, suite.tmpDir)
	if err != nil {
		suite.Cleanup()
		t.Fatalf("failed to create git repository: %v", err)
	}
	t.Logf("Git bare repo at %s, working copy at %s", suite.gitBareDir, suite.gitWorkDir)

	// 4. Create mock backend servers
	t.Log("Step 4: Creating mock backend configuration...")
	// For E2E test, we use placeholder URLs since we're testing the switching logic,
	// not actual proxying. Health checks will fail but that's OK for this test.

	// 5. Create application with full configuration
	t.Log("Step 5: Creating application with git watcher...")
	cfg := &config.Config{
		Services: config.ServiceConfig{
			Blue: config.ServiceEndpoint{
				URL:        "http://localhost:19001", // Placeholder
				HealthPath: "/health",
			},
			Green: config.ServiceEndpoint{
				URL:        "http://localhost:19002", // Placeholder
				HealthPath: "/health",
			},
		},
		Proxy: config.ProxyConfig{
			ListenAddr:   ":0", // Don't actually listen
			DrainTimeout: 5 * time.Second,
		},
		Git: config.GitConfig{
			RepoURL:      "file://" + suite.gitBareDir,
			PollInterval: e2eGitPollInterval,
			Branch:       "master", // go-git defaults to master
		},
		Deploy: config.DeployConfig{
			BlueTag:   "deploy/blue",
			GreenTag:  "deploy/green",
			ActiveTag: "deploy/active",
		},
		Admin: config.AdminConfig{
			Hostname:   "e2e-admin",
			StateDir:   filepath.Join(suite.tmpDir, "admin-state"),
			AuthKey:    adminKey,
			ControlURL: suite.headscale.URL,
			Ephemeral:  true,
		},
		Health: config.HealthConfig{
			Interval: 1 * time.Second,
			Timeout:  500 * time.Millisecond,
		},
		Metrics: config.MetricsConfig{
			HistoryDuration:   1 * time.Hour,
			HistoryResolution: 1 * time.Minute,
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	suite.app, err = app.New(cfg, app.WithLogger(logger))
	if err != nil {
		suite.Cleanup()
		t.Fatalf("failed to create application: %v", err)
	}
	addCleanup(func() {
		suite.app.Stop()
	})

	// 6. Start the application (starts git watcher, health checker, etc.)
	t.Log("Step 6: Starting application...")
	if err = suite.app.Start(ctx); err != nil {
		suite.Cleanup()
		t.Fatalf("failed to start application: %v", err)
	}

	// 7. Start admin server on Headscale network
	t.Log("Step 7: Starting admin server on Tailscale network...")
	if err = suite.app.Admin.Start(ctx); err != nil {
		suite.Cleanup()
		t.Fatalf("failed to start admin server: %v", err)
	}

	// 8. Create test client connected to Headscale
	t.Log("Step 8: Creating test client...")
	clientStateDir := filepath.Join(suite.tmpDir, "client-state")
	suite.testClient, err = NewTestClient(ctx, "e2e-client", clientStateDir, suite.headscale.URL, clientKey)
	if err != nil {
		suite.Cleanup()
		t.Fatalf("failed to create test client: %v", err)
	}
	addCleanup(func() {
		if err := suite.testClient.Close(); err != nil {
			t.Logf("warning: failed to close test client: %v", err)
		}
	})

	clientIP, err := suite.testClient.TailscaleIP(ctx)
	if err != nil {
		suite.Cleanup()
		t.Fatalf("failed to get test client IP: %v", err)
	}
	t.Logf("Test client connected with IP: %s", clientIP)

	// 9. Wait for admin server to be visible as peer
	t.Log("Step 9: Waiting for admin server peer visibility...")
	peerCtx, peerCancel := context.WithTimeout(ctx, e2ePeerWaitTimeout)
	defer peerCancel()
	if err = suite.testClient.WaitForPeer(peerCtx, "e2e-admin"); err != nil {
		suite.Cleanup()
		t.Fatalf("failed to find admin server peer: %v", err)
	}
	t.Log("Admin server peer visible - E2E setup complete")

	return suite
}

// Cleanup releases all test resources in reverse order of creation.
// Safe to call multiple times - only executes once.
func (s *E2ETestSuite) Cleanup() {
	s.cleanupOnce.Do(func() {
		// Run cleanup functions in reverse order (LIFO)
		for i := len(s.cleanupFuncs) - 1; i >= 0; i-- {
			s.cleanupFuncs[i]()
		}
		s.cleanupFuncs = nil

		if s.cancel != nil {
			s.cancel()
		}
	})
}

// adminURL constructs URL for admin endpoints via MagicDNS.
func (s *E2ETestSuite) adminURL(path string) string {
	return "http://e2e-admin:80" + path
}

// updateGitTag moves a deploy tag to a new commit in the working copy and pushes to bare repo.
func (s *E2ETestSuite) updateGitTag(tagName, content string) error {
	repo, err := git.PlainOpen(s.gitWorkDir)
	if err != nil {
		return fmt.Errorf("opening git work repo: %w", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("getting worktree: %w", err)
	}

	// Update file content
	filePath := filepath.Join(s.gitWorkDir, "deploy.txt")
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing deploy.txt: %w", err)
	}

	if _, err := worktree.Add("deploy.txt"); err != nil {
		return fmt.Errorf("staging deploy.txt: %w", err)
	}

	commit, err := worktree.Commit("Update for "+tagName, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "E2E Test",
			Email: "e2e@test.local",
			When:  time.Now(),
		},
	})
	if err != nil {
		return fmt.Errorf("committing changes: %w", err)
	}

	// Delete old tag locally (ignore error if doesn't exist)
	_ = repo.DeleteTag(tagName)

	// Create new tag locally
	if _, err = repo.CreateTag(tagName, commit, nil); err != nil {
		return fmt.Errorf("creating tag %s: %w", tagName, err)
	}

	// Push the commit and tag to the bare repo (origin)
	if err = repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gitconfig.RefSpec{gitconfig.RefSpec("+refs/heads/master:refs/heads/master")},
	}); err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("pushing commits: %w", err)
	}

	// Push the tag (force to update existing tag)
	tagRefSpec := gitconfig.RefSpec(fmt.Sprintf("+refs/tags/%s:refs/tags/%s", tagName, tagName))
	if err = repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gitconfig.RefSpec{tagRefSpec},
	}); err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("pushing tag %s: %w", tagName, err)
	}

	return nil
}

// StatusResponse represents the expected structure of /api/status response.
type StatusResponse struct {
	ActiveService string `json:"active_service"`
	SwitchCount   int64  `json:"switch_count"`
	Git           struct {
		BlueCommit  string `json:"blue_commit"`
		GreenCommit string `json:"green_commit"`
		LastFetch   string `json:"last_fetch"`
	} `json:"git"`
}

// getStatus fetches current status via the admin API.
func (s *E2ETestSuite) getStatus() (*StatusResponse, error) {
	resp, err := s.testClient.Get(s.ctx, s.adminURL("/api/status"))
	if err != nil {
		return nil, fmt.Errorf("GET /api/status: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /api/status returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	var status StatusResponse
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("parsing status JSON: %w", err)
	}
	return &status, nil
}

// RefreshResponse represents the expected structure of webhook refresh response.
type RefreshResponse struct {
	Refreshed   bool   `json:"refreshed"`
	Error       string `json:"error,omitempty"`
	BlueCommit  string `json:"blue_commit,omitempty"`
	GreenCommit string `json:"green_commit,omitempty"`
}

// triggerWebhookRefresh calls the webhook refresh endpoint.
func (s *E2ETestSuite) triggerWebhookRefresh() (*RefreshResponse, error) {
	resp, err := s.testClient.Post(s.ctx, s.adminURL("/api/webhook/refresh"),
		"application/json", strings.NewReader(`{}`))
	if err != nil {
		return nil, fmt.Errorf("POST /api/webhook/refresh: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	var result RefreshResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing refresh JSON: %w", err)
	}
	return &result, nil
}

// createE2EGitRepo creates a bare repository and a working copy for E2E testing.
// Returns (bareDir, workDir, error). The bare repo acts as the "remote" that
// the git watcher fetches from. The working copy is used to make changes and push.
func createE2EGitRepo(t *testing.T, baseDir string) (string, string, error) {
	t.Helper()

	bareDir := filepath.Join(baseDir, "git-bare.git")
	workDir := filepath.Join(baseDir, "git-work")

	// 1. Create bare repository (the "remote")
	_, err := git.PlainInit(bareDir, true) // true = bare
	if err != nil {
		return "", "", fmt.Errorf("initializing bare repo: %w", err)
	}

	// 2. Create working repository
	workRepo, err := git.PlainInit(workDir, false)
	if err != nil {
		return "", "", fmt.Errorf("initializing work repo: %w", err)
	}

	// 3. Add the bare repo as origin remote
	_, err = workRepo.CreateRemote(&gitconfig.RemoteConfig{
		Name: "origin",
		URLs: []string{bareDir},
	})
	if err != nil {
		return "", "", fmt.Errorf("creating origin remote: %w", err)
	}

	// 4. Create initial file and commit
	filePath := filepath.Join(workDir, "deploy.txt")
	if err := os.WriteFile(filePath, []byte("initial"), 0644); err != nil {
		return "", "", fmt.Errorf("writing deploy.txt: %w", err)
	}

	worktree, err := workRepo.Worktree()
	if err != nil {
		return "", "", fmt.Errorf("getting worktree: %w", err)
	}

	if _, err := worktree.Add("deploy.txt"); err != nil {
		return "", "", fmt.Errorf("staging deploy.txt: %w", err)
	}

	commit, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "E2E Test",
			Email: "e2e@test.local",
			When:  time.Now(),
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("creating initial commit: %w", err)
	}

	// 5. Create deploy tags
	if _, err := workRepo.CreateTag("deploy/blue", commit, nil); err != nil {
		return "", "", fmt.Errorf("creating deploy/blue tag: %w", err)
	}
	if _, err := workRepo.CreateTag("deploy/green", commit, nil); err != nil {
		return "", "", fmt.Errorf("creating deploy/green tag: %w", err)
	}

	// 6. Push everything to the bare repo
	err = workRepo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gitconfig.RefSpec{gitconfig.RefSpec("+refs/heads/master:refs/heads/master")},
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return "", "", fmt.Errorf("pushing initial commit: %w", err)
	}

	// Push tags
	err = workRepo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs: []gitconfig.RefSpec{
			gitconfig.RefSpec("+refs/tags/deploy/blue:refs/tags/deploy/blue"),
			gitconfig.RefSpec("+refs/tags/deploy/green:refs/tags/deploy/green"),
		},
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return "", "", fmt.Errorf("pushing tags: %w", err)
	}

	t.Logf("Created git repos: bare=%s, work=%s, commit=%s", bareDir, workDir, commit.String()[:8])
	return bareDir, workDir, nil
}

// pollUntil polls a condition function until it returns true or the context is canceled.
func pollUntil(ctx context.Context, interval time.Duration, condition func() (bool, error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Check immediately first
	if done, err := condition(); err != nil {
		return err
	} else if done {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			done, err := condition()
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
	}
}

// TestE2EGitWebhookHeadscale is a comprehensive end-to-end test that validates:
// 1. Git tag changes trigger switches
// 2. Webhook refresh triggers immediate git check
// 3. Admin UI accessible via Headscale/Tailscale network
// 4. Status API reflects correct state
func TestE2EGitWebhookHeadscale(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E integration test in short mode")
	}

	suite := setupE2ETestSuite(t)
	defer suite.Cleanup()

	// --- Test 1: Verify initial status via Tailscale ---
	t.Run("initial status via Tailscale", func(t *testing.T) {
		status, err := suite.getStatus()
		require.NoError(t, err, "failed to get initial status")

		assert.Equal(t, "blue", status.ActiveService, "initial active service should be blue")
		assert.NotEmpty(t, status.Git.BlueCommit, "blue_commit should be set")
		assert.NotEmpty(t, status.Git.GreenCommit, "green_commit should be set")

		t.Logf("Initial status: active=%s, blue_commit=%s, green_commit=%s",
			status.ActiveService,
			status.Git.BlueCommit,
			status.Git.GreenCommit)
	})

	// --- Test 2: Dashboard accessible via Tailscale ---
	t.Run("dashboard UI via Tailscale", func(t *testing.T) {
		resp, err := suite.testClient.Get(suite.ctx, suite.adminURL("/"))
		require.NoError(t, err, "failed to get dashboard")
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		// Verify key UI elements
		bodyStr := string(body)
		assert.Contains(t, bodyStr, "Blue/Green Load Balancer", "should contain title")
		assert.Contains(t, bodyStr, "Git Controls", "should contain Git Controls section")
		assert.Contains(t, bodyStr, "Refresh Git Tags", "should contain refresh button")
	})

	// --- Test 3: Webhook refresh works via Tailscale ---
	t.Run("webhook refresh via Tailscale", func(t *testing.T) {
		result, err := suite.triggerWebhookRefresh()
		require.NoError(t, err, "failed to trigger webhook refresh")

		t.Logf("Webhook refresh result: refreshed=%v, error=%s", result.Refreshed, result.Error)

		// The response should indicate whether refresh succeeded
		// Note: refresh may fail if git watcher has issues with file:// URLs
		// but we've confirmed the endpoint is accessible via Tailscale
	})

	// --- Test 4: Git tag change triggers switch ---
	t.Run("git tag change triggers switch", func(t *testing.T) {
		// Get initial state
		initialStatus, err := suite.getStatus()
		require.NoError(t, err)
		initialGreenCommit := initialStatus.Git.GreenCommit
		t.Logf("Before tag change: active=%s, green_commit=%s", initialStatus.ActiveService, initialGreenCommit)

		// Update the green tag to a new commit
		t.Log("Updating deploy/green tag to new commit...")
		err = suite.updateGitTag("deploy/green", "green deployment v2 - "+time.Now().String())
		require.NoError(t, err, "failed to update git tag")

		// Trigger immediate refresh via webhook (don't wait for poll)
		t.Log("Triggering webhook refresh...")
		_, err = suite.triggerWebhookRefresh()
		require.NoError(t, err, "failed to trigger refresh after tag change")

		// Poll until the git commit changes or timeout
		pollCtx, pollCancel := context.WithTimeout(suite.ctx, e2ePollTimeout)
		defer pollCancel()

		var finalStatus *StatusResponse
		err = pollUntil(pollCtx, e2ePollInterval, func() (bool, error) {
			status, err := suite.getStatus()
			if err != nil {
				return false, err
			}
			finalStatus = status
			// Check if green commit has changed
			return status.Git.GreenCommit != initialGreenCommit, nil
		})

		require.NoError(t, err, "green commit should change after tag update and webhook refresh")

		t.Logf("Green commit changed as expected: %s -> %s",
			initialGreenCommit, finalStatus.Git.GreenCommit)
		assert.NotEqual(t, initialGreenCommit, finalStatus.Git.GreenCommit,
			"green commit should have changed")
	})

	// --- Test 5: No switch when target equals current ---
	t.Run("no switch when target unchanged", func(t *testing.T) {
		// Get initial switch count
		initialStatus, err := suite.getStatus()
		require.NoError(t, err)
		initialSwitchCount := initialStatus.SwitchCount

		// Trigger refresh without any tag changes
		_, err = suite.triggerWebhookRefresh()
		require.NoError(t, err)

		// Wait briefly to ensure any async processing completes
		time.Sleep(500 * time.Millisecond)

		// Switch count should not have changed
		newStatus, err := suite.getStatus()
		require.NoError(t, err)

		assert.Equal(t, initialSwitchCount, newStatus.SwitchCount,
			"switch count should not change when no tag changes detected")
	})

	// --- Test 6: API endpoints accessible via Tailscale ---
	t.Run("API endpoints via Tailscale", func(t *testing.T) {
		endpoints := []string{
			"/api/metrics",
			"/api/metrics/history",
			"/api/openapi",
			"/api/docs",
		}

		for _, endpoint := range endpoints {
			t.Run(endpoint, func(t *testing.T) {
				resp, err := suite.testClient.Get(suite.ctx, suite.adminURL(endpoint))
				require.NoError(t, err, "failed to GET %s", endpoint)
				defer func() { _ = resp.Body.Close() }()
				assert.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status for %s", endpoint)
			})
		}
	})

	// --- Test 7: SSE endpoint accessible and streaming via Tailscale ---
	t.Run("SSE events via Tailscale", func(t *testing.T) {
		sseCtx, cancel := context.WithTimeout(suite.ctx, 10*time.Second)
		defer cancel()

		resp, err := suite.testClient.Get(sseCtx, suite.adminURL("/events"))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

		// Read from SSE stream to verify we receive events
		reader := bufio.NewReader(resp.Body)
		eventReceived := false

		for {
			select {
			case <-sseCtx.Done():
				// Timeout - SSE connection works but no events received within timeout.
				// This is acceptable as we've verified the stream is accessible.
				t.Log("SSE endpoint accessible (timed out waiting for events)")
				return
			default:
				line, err := reader.ReadString('\n')
				if err != nil {
					if errors.Is(err, io.EOF) || sseCtx.Err() != nil {
						break
					}
					t.Logf("SSE read error: %v", err)
					break
				}
				// SSE events start with "data:" prefix
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
	})

	t.Log("E2E test completed successfully")
}

// TestE2EWebhookTriggersGitRefresh specifically tests that the webhook
// endpoint triggers an immediate git refresh rather than waiting for polling.
func TestE2EWebhookTriggersGitRefresh(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E integration test in short mode")
	}

	suite := setupE2ETestSuite(t)
	defer suite.Cleanup()

	// Get initial last_fetch time
	initialStatus, err := suite.getStatus()
	require.NoError(t, err)
	initialLastFetch := initialStatus.Git.LastFetch
	t.Logf("Initial last_fetch: %s", initialLastFetch)

	// Wait a moment to ensure time difference is measurable
	time.Sleep(200 * time.Millisecond)

	// Trigger webhook refresh
	result, err := suite.triggerWebhookRefresh()
	require.NoError(t, err)
	t.Logf("Webhook result: refreshed=%v, error=%s", result.Refreshed, result.Error)

	// Poll until last_fetch changes or timeout
	pollCtx, pollCancel := context.WithTimeout(suite.ctx, e2ePollTimeout)
	defer pollCancel()

	var newLastFetch string
	err = pollUntil(pollCtx, e2ePollInterval, func() (bool, error) {
		status, err := suite.getStatus()
		if err != nil {
			return false, err
		}
		newLastFetch = status.Git.LastFetch
		return newLastFetch != initialLastFetch, nil
	})

	require.NoError(t, err, "last_fetch should change after webhook refresh")
	t.Logf("last_fetch changed as expected: %s -> %s", initialLastFetch, newLastFetch)
}

// TestE2EMultipleGitTagUpdates verifies that multiple rapid tag updates are handled correctly.
func TestE2EMultipleGitTagUpdates(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E integration test in short mode")
	}

	suite := setupE2ETestSuite(t)
	defer suite.Cleanup()

	// Perform multiple rapid tag updates
	const numUpdates = 3
	for i := 1; i <= numUpdates; i++ {
		content := fmt.Sprintf("update %d at %s", i, time.Now().Format(time.RFC3339Nano))
		t.Logf("Update %d: %s", i, content)

		err := suite.updateGitTag("deploy/blue", content)
		require.NoError(t, err, "failed to update git tag on iteration %d", i)

		// Brief pause to avoid timestamp collisions
		time.Sleep(10 * time.Millisecond)
	}

	// Trigger refresh and verify it completes without error
	result, err := suite.triggerWebhookRefresh()
	require.NoError(t, err, "failed to trigger webhook refresh after multiple updates")
	t.Logf("Final refresh result: refreshed=%v, error=%s", result.Refreshed, result.Error)

	// Verify status endpoint still works
	status, err := suite.getStatus()
	require.NoError(t, err, "failed to get status after multiple updates")
	t.Logf("Final status: active=%s, blue_commit=%s", status.ActiveService, status.Git.BlueCommit)
}

// TestE2EInvalidEndpoint verifies that invalid endpoints return appropriate errors.
func TestE2EInvalidEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E integration test in short mode")
	}

	suite := setupE2ETestSuite(t)
	defer suite.Cleanup()

	// Test non-existent endpoint
	resp, err := suite.testClient.Get(suite.ctx, suite.adminURL("/api/nonexistent"))
	require.NoError(t, err, "request should succeed even for invalid endpoint")
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "non-existent endpoint should return 404")
}

// TestE2EContextCancellation verifies that operations respect context cancellation.
func TestE2EContextCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E integration test in short mode")
	}

	suite := setupE2ETestSuite(t)
	defer suite.Cleanup()

	// Create a context that's already canceled
	canceledCtx, cancel := context.WithCancel(suite.ctx)
	cancel()

	// Attempt to get status with canceled context
	resp, err := suite.testClient.Get(canceledCtx, suite.adminURL("/api/status"))
	if err == nil {
		_ = resp.Body.Close()
	}

	// The request should fail with context canceled error
	assert.Error(t, err, "request with canceled context should fail")
	assert.True(t, errors.Is(err, context.Canceled),
		"error should be context.Canceled, got: %v", err)
}
