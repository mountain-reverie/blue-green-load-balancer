// Package logging provides logging utilities including native journald integration.
package logging

import (
	"log/slog"
	"os"
	"strings"

	slogjournal "github.com/systemd/slog-journal"
)

// NewJournaldHandler creates a handler that writes to journald if available,
// otherwise falls back to JSON output on stdout.
//
// When running under systemd, this uses the native journald protocol with:
//   - Automatic field mapping (Message→MESSAGE, Level→PRIORITY, etc.)
//   - Source location tracking (CODE_FILE, CODE_FUNC, CODE_LINE)
//   - Large message handling via temporary file descriptors
//   - Key sanitization to match journald requirements (uppercase, underscores)
//
// When not running under systemd, falls back to JSON handler on stdout.
func NewJournaldHandler(level slog.Leveler) slog.Handler {
	if !IsUnderSystemd() {
		// Fall back to JSON handler for non-systemd environments
		return slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}

	// Use the official systemd slog-journal handler with key transformation
	h, err := slogjournal.NewHandler(&slogjournal.Options{
		Level: level,
		// Transform keys to journald format: uppercase with underscores
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			a.Key = sanitizeKey(a.Key)
			return a
		},
		ReplaceGroup: func(group string) string {
			return sanitizeKey(group)
		},
	})
	if err != nil {
		// Fall back to JSON if journald handler creation fails
		return slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}

	return h
}

// sanitizeKey converts a key to journald-compatible format.
// Journald variables must be uppercase, start with a letter, and contain
// only letters, numbers, and underscores.
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
