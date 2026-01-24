package ui

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mountain-reverie/blue-green-load-balancer/internal/metrics"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/switcher"
	"github.com/mountain-reverie/blue-green-load-balancer/internal/ui/templates"
)

// Handlers provides HTTP handlers for the UI.
type Handlers struct {
	switcher *switcher.Switcher
	metrics  *metrics.Collector
	sse      *SSEBroker
}

// NewHandlers creates new UI handlers.
func NewHandlers(sw *switcher.Switcher, m *metrics.Collector) *Handlers {
	h := &Handlers{
		switcher: sw,
		metrics:  m,
		sse:      NewSSEBroker(),
	}
	return h
}

// RegisterRoutes registers UI routes on the given router.
func (h *Handlers) RegisterRoutes(r chi.Router) {
	r.Get("/", h.Dashboard)
	r.Get("/dashboard", h.Dashboard)
	r.Get("/events", h.SSEHandler)
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
}

// Dashboard renders the main dashboard page.
func (h *Handlers) Dashboard(w http.ResponseWriter, r *http.Request) {
	status := h.switcher.GetStatus()
	var snapshot metrics.Snapshot
	if h.metrics != nil {
		snapshot = h.metrics.Snapshot()
	}

	data := templates.DashboardData{
		Status:  status,
		Metrics: snapshot,
		History: h.switcher.History(),
	}

	component := templates.DashboardPage(data)
	component.Render(r.Context(), w)
}

// SSEHandler handles Server-Sent Events connections.
func (h *Handlers) SSEHandler(w http.ResponseWriter, r *http.Request) {
	h.sse.ServeHTTP(w, r)
}

// SSEBroker returns the SSE broker for sending updates.
func (h *Handlers) SSEBroker() *SSEBroker {
	return h.sse
}

// StartUpdates starts sending periodic updates to SSE clients.
func (h *Handlers) StartUpdates() {
	h.sse.Start()
}

// StopUpdates stops sending updates.
func (h *Handlers) StopUpdates() {
	h.sse.Stop()
}

// SendStatusUpdate sends a status update to all SSE clients.
func (h *Handlers) SendStatusUpdate() {
	status := h.switcher.GetStatus()
	var snapshot metrics.Snapshot
	if h.metrics != nil {
		snapshot = h.metrics.Snapshot()
	}

	h.sse.SendEvent("status", StatusUpdate{
		Status:  status,
		Metrics: snapshot,
	})
}

// SendSwitchEvent sends a switch event to all SSE clients.
func (h *Handlers) SendSwitchEvent(event switcher.SwitchEvent) {
	h.sse.SendEvent("switch", event)
}

// StatusUpdate is sent via SSE when status changes.
type StatusUpdate struct {
	Status  switcher.Status  `json:"status"`
	Metrics metrics.Snapshot `json:"metrics"`
}
