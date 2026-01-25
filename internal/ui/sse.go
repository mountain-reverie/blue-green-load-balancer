package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// SSEBroker manages Server-Sent Events connections.
type SSEBroker struct {
	clients    map[chan []byte]bool
	register   chan chan []byte
	unregister chan chan []byte
	broadcast  chan []byte
	mu         sync.RWMutex
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

// NewSSEBroker creates a new SSE broker.
func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		clients:    make(map[chan []byte]bool),
		register:   make(chan chan []byte),
		unregister: make(chan chan []byte),
		broadcast:  make(chan []byte, 100),
	}
}

// Start begins the broker's event loop.
func (b *SSEBroker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel

	b.wg.Add(1)
	go b.run(ctx)
}

// Stop stops the broker.
func (b *SSEBroker) Stop() {
	if b.cancel != nil {
		b.cancel()
	}
	b.wg.Wait()
}

// run is the main event loop.
func (b *SSEBroker) run(ctx context.Context) {
	defer b.wg.Done()

	for {
		select {
		case <-ctx.Done():
			b.mu.Lock()
			for client := range b.clients {
				close(client)
				delete(b.clients, client)
			}
			b.mu.Unlock()
			return

		case client := <-b.register:
			b.mu.Lock()
			b.clients[client] = true
			b.mu.Unlock()

		case client := <-b.unregister:
			b.mu.Lock()
			if _, ok := b.clients[client]; ok {
				close(client)
				delete(b.clients, client)
			}
			b.mu.Unlock()

		case msg := <-b.broadcast:
			b.mu.RLock()
			for client := range b.clients {
				select {
				case client <- msg:
				default:
					// Client buffer full, skip
				}
			}
			b.mu.RUnlock()
		}
	}
}

// ServeHTTP implements http.Handler for SSE connections.
func (b *SSEBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Create client channel
	client := make(chan []byte, 10)

	// Register client
	b.register <- client

	// Ensure cleanup (non-blocking in case broker is stopped)
	defer func() {
		select {
		case b.unregister <- client:
		default:
			// Broker already stopped, channel is no longer being received
		}
	}()

	// Send initial connection message
	fmt.Fprintf(w, "event: connected\ndata: {\"connected\": true}\n\n")
	flusher.Flush()

	// Keep-alive ticker
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case msg, ok := <-client:
			if !ok {
				return
			}
			w.Write(msg)
			flusher.Flush()

		case <-ticker.C:
			// Send keep-alive
			fmt.Fprintf(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// SendEvent sends an event to all connected clients.
func (b *SSEBroker) SendEvent(eventType string, data any) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return
	}

	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, jsonData)

	select {
	case b.broadcast <- []byte(msg):
	default:
		// Broadcast channel full, drop message
	}
}

// SendRaw sends a raw message to all connected clients.
func (b *SSEBroker) SendRaw(msg string) {
	select {
	case b.broadcast <- []byte(msg):
	default:
	}
}

// ClientCount returns the number of connected clients.
func (b *SSEBroker) ClientCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
}
