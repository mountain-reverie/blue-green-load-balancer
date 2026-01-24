package proxy

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Drainer tracks active connections and provides graceful drain functionality.
type Drainer struct {
	activeConnections atomic.Int64
	wg                sync.WaitGroup
	draining          atomic.Bool
	drainCh           chan struct{}
	mu                sync.Mutex
}

// NewDrainer creates a new connection drainer.
func NewDrainer() *Drainer {
	return &Drainer{
		drainCh: make(chan struct{}),
	}
}

// TrackRequest wraps an HTTP handler to track active requests.
func (d *Drainer) TrackRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d.draining.Load() {
			http.Error(w, "Service is draining", http.StatusServiceUnavailable)
			return
		}

		d.activeConnections.Add(1)
		d.wg.Add(1)
		defer func() {
			d.activeConnections.Add(-1)
			d.wg.Done()
		}()

		next.ServeHTTP(w, r)
	})
}

// ActiveConnections returns the current number of active connections.
func (d *Drainer) ActiveConnections() int64 {
	return d.activeConnections.Load()
}

// IsDraining returns whether the drainer is currently in drain mode.
func (d *Drainer) IsDraining() bool {
	return d.draining.Load()
}

// Drain starts the drain process and waits for all active connections to complete.
// It returns when all connections are drained or the context is cancelled.
func (d *Drainer) Drain(ctx context.Context, timeout time.Duration) error {
	d.mu.Lock()
	if d.draining.Load() {
		d.mu.Unlock()
		return nil
	}
	d.draining.Store(true)
	close(d.drainCh)
	d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reset resets the drainer state for a new drain cycle.
func (d *Drainer) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.draining.Store(false)
	d.drainCh = make(chan struct{})
}

// DrainCh returns a channel that is closed when draining starts.
func (d *Drainer) DrainCh() <-chan struct{} {
	return d.drainCh
}

// ConnectionTracker provides a way to track connection state.
type ConnectionTracker struct {
	drainer *Drainer
	onTrack func(delta int64)
}

// NewConnectionTracker creates a tracker with optional callbacks.
func NewConnectionTracker(d *Drainer, onTrack func(delta int64)) *ConnectionTracker {
	return &ConnectionTracker{
		drainer: d,
		onTrack: onTrack,
	}
}

// Track increments the connection count and returns a release function.
func (ct *ConnectionTracker) Track() func() {
	ct.drainer.activeConnections.Add(1)
	ct.drainer.wg.Add(1)
	if ct.onTrack != nil {
		ct.onTrack(1)
	}

	return func() {
		ct.drainer.activeConnections.Add(-1)
		ct.drainer.wg.Done()
		if ct.onTrack != nil {
			ct.onTrack(-1)
		}
	}
}
