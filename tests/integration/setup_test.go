//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestSuite holds all test infrastructure.
type TestSuite struct {
	t       *testing.T
	ctx     context.Context
	cancel  context.CancelFunc
	blue    testcontainers.Container
	green   testcontainers.Container
	gitRepo string
}

// NewTestSuite creates and initializes a new test suite.
func NewTestSuite(t *testing.T) *TestSuite {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)

	suite := &TestSuite{
		t:      t,
		ctx:    ctx,
		cancel: cancel,
	}

	return suite
}

// SetupMockServices starts mock blue and green HTTP services.
func (s *TestSuite) SetupMockServices() {
	var err error

	// Start blue service
	s.blue, err = startMockService(s.ctx, "blue", 3001)
	require.NoError(s.t, err, "failed to start blue service")

	// Start green service
	s.green, err = startMockService(s.ctx, "green", 3002)
	require.NoError(s.t, err, "failed to start green service")
}

// SetupGitRepo creates a local git repository for testing.
func (s *TestSuite) SetupGitRepo() {
	var err error
	s.gitRepo, err = createTestGitRepo(s.t)
	require.NoError(s.t, err, "failed to create test git repo")
}

// Cleanup cleans up all test resources.
func (s *TestSuite) Cleanup() {
	if s.blue != nil {
		s.blue.Terminate(s.ctx)
	}
	if s.green != nil {
		s.green.Terminate(s.ctx)
	}
	if s.gitRepo != "" {
		os.RemoveAll(s.gitRepo)
	}
	s.cancel()
}

// BlueURL returns the URL of the blue service.
func (s *TestSuite) BlueURL() string {
	host, err := s.blue.Host(s.ctx)
	require.NoError(s.t, err)
	port, err := s.blue.MappedPort(s.ctx, "8080")
	require.NoError(s.t, err)
	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

// GreenURL returns the URL of the green service.
func (s *TestSuite) GreenURL() string {
	host, err := s.green.Host(s.ctx)
	require.NoError(s.t, err)
	port, err := s.green.MappedPort(s.ctx, "8080")
	require.NoError(s.t, err)
	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

// GitRepoPath returns the path to the test git repository.
func (s *TestSuite) GitRepoPath() string {
	return s.gitRepo
}

// startMockService starts a mock HTTP service using testcontainers.
func startMockService(ctx context.Context, name string, _ int) (testcontainers.Container, error) {
	req := testcontainers.ContainerRequest{
		Image:        "nginx:alpine",
		ExposedPorts: []string{"8080/tcp"},
		Env: map[string]string{
			"SERVICE_NAME": name,
		},
		WaitingFor: wait.ForHTTP("/").WithPort("8080/tcp"),
		Cmd: []string{
			"sh", "-c",
			fmt.Sprintf(`cat > /etc/nginx/conf.d/default.conf <<EOF
server {
    listen 8080;
    location / {
        add_header X-Service %s;
        return 200 'Hello from %s!\n';
    }
    location /health {
        return 200 'OK\n';
    }
}
EOF
nginx -g 'daemon off;'`, name, name),
		},
	}

	return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
}

// createTestGitRepo creates a temporary git repository with test tags.
func createTestGitRepo(t *testing.T) (string, error) {
	// Create temp directory
	dir, err := os.MkdirTemp("", "bluegreen-test-git-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir: %w", err)
	}

	// Initialize git repo
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("initializing git repo: %w", err)
	}

	// Create a file
	filePath := filepath.Join(dir, "config.txt")
	if err := os.WriteFile(filePath, []byte("initial config"), 0644); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("writing file: %w", err)
	}

	// Add and commit
	worktree, err := repo.Worktree()
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("getting worktree: %w", err)
	}

	if _, err := worktree.Add("config.txt"); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("adding file: %w", err)
	}

	commit, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("committing: %w", err)
	}

	// Create deploy/blue tag
	_, err = repo.CreateTag("deploy/blue", commit, nil)
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("creating blue tag: %w", err)
	}

	// Create deploy/green tag
	_, err = repo.CreateTag("deploy/green", commit, nil)
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("creating green tag: %w", err)
	}

	return dir, nil
}

// UpdateGitTag moves a tag to a new commit.
func (s *TestSuite) UpdateGitTag(tagName, content string) error {
	repo, err := git.PlainOpen(s.gitRepo)
	if err != nil {
		return fmt.Errorf("opening repo: %w", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("getting worktree: %w", err)
	}

	// Update file
	filePath := filepath.Join(s.gitRepo, "config.txt")
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}

	if _, err := worktree.Add("config.txt"); err != nil {
		return fmt.Errorf("adding file: %w", err)
	}

	commit, err := worktree.Commit(fmt.Sprintf("Update for %s", tagName), &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		return fmt.Errorf("committing: %w", err)
	}

	// Delete old tag
	if err := repo.DeleteTag(tagName); err != nil {
		// Tag might not exist, ignore error
	}

	// Create new tag
	_, err = repo.CreateTag(tagName, commit, nil)
	if err != nil {
		return fmt.Errorf("creating tag: %w", err)
	}

	return nil
}

// GetTagCommit returns the commit hash for a tag.
func (s *TestSuite) GetTagCommit(tagName string) (string, error) {
	repo, err := git.PlainOpen(s.gitRepo)
	if err != nil {
		return "", fmt.Errorf("opening repo: %w", err)
	}

	ref, err := repo.Tag(tagName)
	if err != nil {
		return "", fmt.Errorf("getting tag: %w", err)
	}

	// Handle both lightweight and annotated tags
	obj, err := repo.TagObject(ref.Hash())
	if err != nil {
		// Lightweight tag, hash is commit hash
		return ref.Hash().String(), nil
	}

	return obj.Target.String(), nil
}

// CreateBranch creates a new branch in the test repo.
func (s *TestSuite) CreateBranch(branchName string) error {
	repo, err := git.PlainOpen(s.gitRepo)
	if err != nil {
		return fmt.Errorf("opening repo: %w", err)
	}

	headRef, err := repo.Head()
	if err != nil {
		return fmt.Errorf("getting HEAD: %w", err)
	}

	ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), headRef.Hash())
	return repo.Storer.SetReference(ref)
}

// HTTPGet performs an HTTP GET request and returns the response body.
func HTTPGet(url string) (string, int, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", resp.StatusCode, err
	}

	return string(body), resp.StatusCode, nil
}
