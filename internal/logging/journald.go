// Package logging provides logging utilities including native journald integration.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/coreos/go-systemd/v22/journal"
)

// JournaldHandler is a slog.Handler that writes directly to journald
// with structured field support. Fields are converted to journald variables
// (uppercase, underscore-separated).
type JournaldHandler struct {
	level  slog.Leveler
	groups []string
	attrs  []slog.Attr
}

// NewJournaldHandler creates a handler that writes to journald if available,
// otherwise falls back to JSON output on stdout.
func NewJournaldHandler(level slog.Leveler) slog.Handler {
	if !journal.Enabled() {
		// Fall back to JSON handler for non-systemd environments
		return slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	return &JournaldHandler{level: level}
}

// Enabled reports whether the handler handles records at the given level.
func (h *JournaldHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

// Handle writes the record to journald.
func (h *JournaldHandler) Handle(_ context.Context, r slog.Record) error {
	// Convert slog level to journald priority
	priority := levelToPriority(r.Level)

	// Build journald variables from attributes
	vars := make(map[string]string)

	// Add pre-configured attrs
	for _, a := range h.attrs {
		addAttrToVars(vars, h.groups, a)
	}

	// Add record attrs
	r.Attrs(func(a slog.Attr) bool {
		addAttrToVars(vars, h.groups, a)
		return true
	})

	// Add standard fields
	if r.PC != 0 {
		// Get source location
		fs := slog.Source{}
		_ = slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
		// The source is available but we skip it for performance
		// You could add CODE_FILE, CODE_LINE, CODE_FUNC here
		_ = fs
	}

	return journal.Send(r.Message, priority, vars)
}

// WithAttrs returns a new handler with the given attributes added.
func (h *JournaldHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &JournaldHandler{
		level:  h.level,
		groups: h.groups,
		attrs:  newAttrs,
	}
}

// WithGroup returns a new handler with the given group appended.
func (h *JournaldHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	newGroups := make([]string, len(h.groups)+1)
	copy(newGroups, h.groups)
	newGroups[len(h.groups)] = name
	return &JournaldHandler{
		level:  h.level,
		groups: newGroups,
		attrs:  h.attrs,
	}
}

// levelToPriority converts slog.Level to journal.Priority.
func levelToPriority(level slog.Level) journal.Priority {
	switch {
	case level >= slog.LevelError:
		return journal.PriErr
	case level >= slog.LevelWarn:
		return journal.PriWarning
	case level >= slog.LevelInfo:
		return journal.PriInfo
	default:
		return journal.PriDebug
	}
}

// addAttrToVars adds a slog.Attr to the journald variables map.
// Keys are converted to uppercase with underscores, prefixed with groups.
func addAttrToVars(vars map[string]string, groups []string, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}

	key := attrKey(groups, a.Key)
	value := a.Value.Resolve()

	switch value.Kind() {
	case slog.KindGroup:
		// Recurse into group
		newGroups := append(groups, a.Key)
		for _, ga := range value.Group() {
			addAttrToVars(vars, newGroups, ga)
		}
	default:
		vars[key] = value.String()
	}
}

// attrKey builds a journald-compatible variable name from groups and key.
// Journald variables must be uppercase, start with a letter, and contain
// only letters, numbers, and underscores.
func attrKey(groups []string, key string) string {
	parts := make([]string, 0, len(groups)+1)
	for _, g := range groups {
		parts = append(parts, sanitizeKey(g))
	}
	parts = append(parts, sanitizeKey(key))
	return strings.Join(parts, "_")
}

// sanitizeKey converts a key to journald-compatible format.
func sanitizeKey(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A') // Convert to uppercase
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if b.Len() == 0 {
				b.WriteRune('_') // Prefix numbers with underscore
			}
			b.WriteRune(r)
		case r == '_' || r == '-' || r == '.':
			b.WriteRune('_')
		default:
			b.WriteRune('_')
		}
	}
	result := b.String()
	if result == "" {
		return "FIELD"
	}
	return result
}
