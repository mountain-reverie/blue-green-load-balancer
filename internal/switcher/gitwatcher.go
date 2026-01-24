package switcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"

	appconfig "github.com/mountain-reverie/blue-green-load-balancer/internal/config"
)

// GitStatus represents the current git repository state.
type GitStatus struct {
	LastFetch    time.Time `json:"last_fetch"`
	LastTag      string    `json:"last_tag"`
	BlueCommit   string    `json:"blue_commit,omitempty"`
	GreenCommit  string    `json:"green_commit,omitempty"`
	ActiveCommit string    `json:"active_commit,omitempty"`
	Error        string    `json:"error,omitempty"`
}

// TagChange represents a detected tag change.
type TagChange struct {
	Tag       string
	Commit    string
	Target    appconfig.ServiceTarget
	Timestamp time.Time
}

// GitWatcher polls a git repository for tag changes.
type GitWatcher struct {
	cfg    *appconfig.Config
	logger *slog.Logger

	mu        sync.RWMutex
	status    GitStatus
	lastBlue  string
	lastGreen string

	onTagChange func(change TagChange)

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// GitWatcherOption configures the git watcher.
type GitWatcherOption func(*GitWatcher)

// WithTagChangeCallback sets a callback for tag changes.
func WithTagChangeCallback(fn func(change TagChange)) GitWatcherOption {
	return func(w *GitWatcher) {
		w.onTagChange = fn
	}
}

// NewGitWatcher creates a new git watcher.
func NewGitWatcher(cfg *appconfig.Config, logger *slog.Logger, opts ...GitWatcherOption) *GitWatcher {
	w := &GitWatcher{
		cfg:    cfg,
		logger: logger,
	}

	for _, opt := range opts {
		opt(w)
	}

	return w
}

// Start begins the git polling loop.
func (w *GitWatcher) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)

	w.wg.Add(1)
	go w.pollLoop(ctx)
}

// Stop stops the git polling loop.
func (w *GitWatcher) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
}

// pollLoop runs the git polling loop.
func (w *GitWatcher) pollLoop(ctx context.Context) {
	defer w.wg.Done()

	ticker := time.NewTicker(w.cfg.Git.PollInterval)
	defer ticker.Stop()

	// Initial fetch
	w.fetchAndCheck(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.fetchAndCheck(ctx)
		}
	}
}

// fetchAndCheck fetches the repository and checks for tag changes.
func (w *GitWatcher) fetchAndCheck(ctx context.Context) {
	refs, err := w.fetchRefs(ctx)
	if err != nil {
		w.logger.Error("failed to fetch git refs", "error", err)
		w.mu.Lock()
		w.status.Error = err.Error()
		w.status.LastFetch = time.Now()
		w.mu.Unlock()
		return
	}

	w.mu.Lock()
	w.status.LastFetch = time.Now()
	w.status.Error = ""

	// Check blue tag
	blueRef := "refs/tags/" + w.cfg.Deploy.BlueTag
	if commit, ok := refs[blueRef]; ok {
		w.status.BlueCommit = commit
		if w.lastBlue != "" && w.lastBlue != commit {
			w.mu.Unlock()
			w.notifyTagChange(TagChange{
				Tag:       w.cfg.Deploy.BlueTag,
				Commit:    commit,
				Target:    appconfig.ServiceBlue,
				Timestamp: time.Now(),
			})
			w.mu.Lock()
		}
		w.lastBlue = commit
	}

	// Check green tag
	greenRef := "refs/tags/" + w.cfg.Deploy.GreenTag
	if commit, ok := refs[greenRef]; ok {
		w.status.GreenCommit = commit
		if w.lastGreen != "" && w.lastGreen != commit {
			w.mu.Unlock()
			w.notifyTagChange(TagChange{
				Tag:       w.cfg.Deploy.GreenTag,
				Commit:    commit,
				Target:    appconfig.ServiceGreen,
				Timestamp: time.Now(),
			})
			w.mu.Lock()
		}
		w.lastGreen = commit
	}

	// Check active tag
	activeRef := "refs/tags/" + w.cfg.Deploy.ActiveTag
	if commit, ok := refs[activeRef]; ok {
		w.status.ActiveCommit = commit
		w.status.LastTag = w.cfg.Deploy.ActiveTag
	}

	w.mu.Unlock()
}

// fetchRefs fetches references from the remote repository.
func (w *GitWatcher) fetchRefs(ctx context.Context) (map[string]string, error) {
	if w.cfg.Git.RepoURL == "" {
		return nil, errors.New("git repo URL not configured")
	}

	// Create a remote
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{w.cfg.Git.RepoURL},
	})

	// Set up authentication if provided
	var auth *http.BasicAuth
	if w.cfg.Git.AuthToken != "" {
		auth = &http.BasicAuth{
			Username: "git",
			Password: w.cfg.Git.AuthToken,
		}
	}

	// List remote references
	listOpts := &git.ListOptions{
		Auth: auth,
	}

	refs, err := remote.ListContext(ctx, listOpts)
	if err != nil {
		return nil, fmt.Errorf("listing remote refs: %w", err)
	}

	result := make(map[string]string)
	for _, ref := range refs {
		result[ref.Name().String()] = ref.Hash().String()
	}

	return result, nil
}

// notifyTagChange notifies listeners of a tag change.
func (w *GitWatcher) notifyTagChange(change TagChange) {
	w.logger.Info("tag change detected",
		"tag", change.Tag,
		"commit", change.Commit,
		"target", change.Target,
	)

	if w.onTagChange != nil {
		w.onTagChange(change)
	}
}

// GetStatus returns the current git status.
func (w *GitWatcher) GetStatus() GitStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.status
}

// FetchNow performs an immediate fetch.
func (w *GitWatcher) FetchNow(ctx context.Context) error {
	w.fetchAndCheck(ctx)
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.status.Error != "" {
		return errors.New(w.status.Error)
	}
	return nil
}

// CloneRepo clones the git repository to a local directory.
// This can be used for more complex git operations.
func CloneRepo(ctx context.Context, cfg *appconfig.Config, destDir string) (*git.Repository, error) {
	if cfg.Git.RepoURL == "" {
		return nil, errors.New("git repo URL not configured")
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(destDir), 0750); err != nil {
		return nil, fmt.Errorf("creating destination directory: %w", err)
	}

	// Set up authentication
	var auth *http.BasicAuth
	if cfg.Git.AuthToken != "" {
		auth = &http.BasicAuth{
			Username: "git",
			Password: cfg.Git.AuthToken,
		}
	}

	// Clone options
	cloneOpts := &git.CloneOptions{
		URL:           cfg.Git.RepoURL,
		Auth:          auth,
		ReferenceName: plumbing.NewBranchReferenceName(cfg.Git.Branch),
		SingleBranch:  true,
		Depth:         1,
		Tags:          git.AllTags,
	}

	repo, err := git.PlainCloneContext(ctx, destDir, false, cloneOpts)
	if err != nil {
		return nil, fmt.Errorf("cloning repository: %w", err)
	}

	return repo, nil
}
