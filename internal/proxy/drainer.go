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
	drainMu           sync.Mutex    // Serializes Drain() calls to prevent WaitGroup reuse
	prevDone          chan struct{} // Tracks previous drain goroutine completion
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
		// Hold mutex while checking draining state and adding to WaitGroup
		// to prevent race with Drain() setting draining=true and calling wg.Wait().
		d.mu.Lock()
		if d.draining.Load() {
			d.mu.Unlock()
			http.Error(w, "Service is draining", http.StatusServiceUnavailable)
			return
		}
		d.activeConnections.Add(1)
		d.wg.Add(1)
		d.mu.Unlock()

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

// Track enters a tracked section. Returns a release function, or nil if draining.
// This method atomically checks the draining state and adds to the WaitGroup
// under mutex protection, preventing races with Drain().
func (d *Drainer) Track() func() {
	d.mu.Lock()
	if d.draining.Load() {
		d.mu.Unlock()
		return nil
	}
	d.activeConnections.Add(1)
	d.wg.Add(1)
	d.mu.Unlock()

	return func() {
		d.activeConnections.Add(-1)
		d.wg.Done()
	}
}

// Drain starts the drain process and waits for all active connections to complete.
// It returns when all connections are drained or the context is cancelled.
// Concurrent calls to Drain() are serialized to prevent WaitGroup reuse panics.
func (d *Drainer) Drain(ctx context.Context, timeout time.Duration) error {
	// Serialize drain operations to prevent WaitGroup reuse while Wait() is running
	d.drainMu.Lock()
	defer d.drainMu.Unlock()

	// Wait for any previous drain goroutine to complete before starting a new one.
	// This prevents multiple wg.Wait() goroutines from running concurrently,
	// which would cause a panic if the WaitGroup is reused after Reset().
	if d.prevDone != nil {
		select {
		case <-d.prevDone:
			// Previous drain completed
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	d.mu.Lock()
	if d.draining.Load() {
		d.mu.Unlock()
		return nil
	}
	d.draining.Store(true)
	close(d.drainCh)
	d.mu.Unlock()

	done := make(chan struct{})
	d.prevDone = done
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
// It waits for any previous drain goroutine to complete before resetting,
// preventing WaitGroup reuse panics.
func (d *Drainer) Reset() {
	d.drainMu.Lock()
	defer d.drainMu.Unlock()

	// Wait for previous drain goroutine to complete before resetting.
	// This prevents WaitGroup reuse while wg.Wait() is still running.
	if d.prevDone != nil {
		<-d.prevDone
		d.prevDone = nil
	}

	d.mu.Lock()
	d.draining.Store(false)
	d.drainCh = make(chan struct{})
	d.mu.Unlock()
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
// Returns nil if the drainer is currently draining.
func (ct *ConnectionTracker) Track() func() {
	// Hold mutex while checking draining state and adding to WaitGroup
	// to prevent race with Drain() setting draining=true and calling wg.Wait().
	ct.drainer.mu.Lock()
	if ct.drainer.draining.Load() {
		ct.drainer.mu.Unlock()
		return nil
	}
	ct.drainer.activeConnections.Add(1)
	ct.drainer.wg.Add(1)
	ct.drainer.mu.Unlock()

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
