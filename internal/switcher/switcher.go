package switcher

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/config"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/health"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/proxy"
)

// SwitchEvent represents a switch operation.
type SwitchEvent struct {
	From      config.ServiceTarget `json:"from"`
	To        config.ServiceTarget `json:"to"`
	Trigger   SwitchTrigger        `json:"trigger"`
	Timestamp time.Time            `json:"timestamp"`
	Success   bool                 `json:"success"`
	Error     string               `json:"error,omitempty"`
	Duration  time.Duration        `json:"duration"`
}

// SwitchTrigger indicates what triggered a switch.
type SwitchTrigger string

const (
	TriggerWebhook SwitchTrigger = "webhook"
	TriggerGitTag  SwitchTrigger = "git_tag"
)

// Switcher orchestrates switching between blue and green backends.
type Switcher struct {
	cfg     *config.Config
	logger  *slog.Logger
	proxy   *proxy.Proxy
	health  *health.Checker
	watcher *GitWatcher

	mu          sync.RWMutex
	lastSwitch  time.Time
	switchCount int64
	history     []SwitchEvent

	onSwitch func(event SwitchEvent)
}

// SwitcherOption configures the switcher.
type SwitcherOption func(*Switcher)

// WithSwitchCallback sets a callback for switch events.
func WithSwitchCallback(fn func(event SwitchEvent)) SwitcherOption {
	return func(s *Switcher) {
		s.onSwitch = fn
	}
}

// NewSwitcher creates a new switch orchestrator.
func NewSwitcher(
	cfg *config.Config,
	logger *slog.Logger,
	p *proxy.Proxy,
	h *health.Checker,
	opts ...SwitcherOption,
) *Switcher {
	s := &Switcher{
		cfg:     cfg,
		logger:  logger,
		proxy:   p,
		health:  h,
		history: make([]SwitchEvent, 0, 100),
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// SetGitWatcher sets the git watcher for automatic switching.
func (s *Switcher) SetGitWatcher(w *GitWatcher) {
	s.watcher = w
}

// Switch performs a switch to the specified target.
func (s *Switcher) Switch(ctx context.Context, target config.ServiceTarget, trigger SwitchTrigger) error {
	s.mu.Lock()
	from := s.proxy.ActiveTarget()

	if from == target {
		s.mu.Unlock()
		s.logger.Info("already on target, no switch needed", "target", target)
		return nil
	}

	// Check health of target
	if !s.health.IsHealthy(target) {
		s.mu.Unlock()
		return fmt.Errorf("target %s is not healthy", target)
	}

	start := time.Now()
	s.mu.Unlock()

	s.logger.Info("starting switch",
		"from", from,
		"to", target,
		"trigger", trigger,
	)

	// Perform the switch
	err := s.proxy.Switch(ctx, target)

	duration := time.Since(start)
	event := SwitchEvent{
		From:      from,
		To:        target,
		Trigger:   trigger,
		Timestamp: start,
		Duration:  duration,
		Success:   err == nil,
	}
	if err != nil {
		event.Error = err.Error()
	}

	s.mu.Lock()
	if err == nil {
		s.lastSwitch = time.Now()
		s.switchCount++
	}

	// Add to history (keep last 100)
	s.history = append(s.history, event)
	if len(s.history) > 100 {
		s.history = s.history[1:]
	}
	s.mu.Unlock()

	// Notify listeners
	if s.onSwitch != nil {
		s.onSwitch(event)
	}

	if err != nil {
		s.logger.Error("switch failed",
			"from", from,
			"to", target,
			"error", err,
			"duration", duration,
		)
		return err
	}

	s.logger.Info("switch completed",
		"from", from,
		"to", target,
		"duration", duration,
	)

	return nil
}

// SwitchToBlue switches to the blue backend.
func (s *Switcher) SwitchToBlue(ctx context.Context, trigger SwitchTrigger) error {
	return s.Switch(ctx, config.ServiceBlue, trigger)
}

// SwitchToGreen switches to the green backend.
func (s *Switcher) SwitchToGreen(ctx context.Context, trigger SwitchTrigger) error {
	return s.Switch(ctx, config.ServiceGreen, trigger)
}

// ActiveTarget returns the currently active target.
func (s *Switcher) ActiveTarget() config.ServiceTarget {
	return s.proxy.ActiveTarget()
}

// LastSwitch returns the time of the last successful switch.
func (s *Switcher) LastSwitch() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSwitch
}

// SwitchCount returns the total number of switches.
func (s *Switcher) SwitchCount() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.switchCount
}

// History returns the switch history.
func (s *Switcher) History() []SwitchEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]SwitchEvent, len(s.history))
	copy(result, s.history)
	return result
}

// GetGitStatus returns the current git status.
func (s *Switcher) GetGitStatus() GitStatus {
	if s.watcher != nil {
		return s.watcher.GetStatus()
	}
	return GitStatus{}
}

// RefreshGitResult contains the result of a git refresh operation.
type RefreshGitResult struct {
	Refreshed    bool      `json:"refreshed"`
	Error        string    `json:"error,omitempty"`
	BlueCommit   string    `json:"blue_commit,omitempty"`
	GreenCommit  string    `json:"green_commit,omitempty"`
	ActiveCommit string    `json:"active_commit,omitempty"`
	LastFetch    time.Time `json:"last_fetch"`
}

// RefreshGit triggers an immediate git fetch and returns the result.
// Any tag changes detected will trigger switches via the normal callback mechanism.
func (s *Switcher) RefreshGit(ctx context.Context) RefreshGitResult {
	if s.watcher == nil {
		return RefreshGitResult{
			Refreshed: false,
			Error:     "git watcher not configured",
		}
	}

	err := s.watcher.FetchNow(ctx)
	status := s.watcher.GetStatus()

	result := RefreshGitResult{
		Refreshed:    err == nil,
		BlueCommit:   status.BlueCommit,
		GreenCommit:  status.GreenCommit,
		ActiveCommit: status.ActiveCommit,
		LastFetch:    status.LastFetch,
	}

	if err != nil {
		result.Error = err.Error()
	}

	return result
}

// HandleTagChange handles a git tag change event.
func (s *Switcher) HandleTagChange(change TagChange) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.logger.Info("handling git tag change",
		"tag", change.Tag,
		"target", change.Target,
	)

	if err := s.Switch(ctx, change.Target, TriggerGitTag); err != nil {
		s.logger.Error("failed to switch on tag change",
			"error", err,
			"tag", change.Tag,
			"target", change.Target,
		)
	}
}

// Status represents the current switcher status.
type Status struct {
	ActiveService config.ServiceTarget `json:"active_service"`
	LastSwitch    time.Time            `json:"last_switch"`
	SwitchCount   int64                `json:"switch_count"`
	BlueHealth    health.Status        `json:"blue_health"`
	GreenHealth   health.Status        `json:"green_health"`
	Git           GitStatus            `json:"git"`
}

// GetStatus returns the current switcher status.
func (s *Switcher) GetStatus() Status {
	return Status{
		ActiveService: s.ActiveTarget(),
		LastSwitch:    s.LastSwitch(),
		SwitchCount:   s.SwitchCount(),
		BlueHealth:    s.health.GetStatus(config.ServiceBlue),
		GreenHealth:   s.health.GetStatus(config.ServiceGreen),
		Git:           s.GetGitStatus(),
	}
}
