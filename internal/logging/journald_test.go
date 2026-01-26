package logging

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeKey(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "SIMPLE"},
		{"camelCase", "CAMELCASE"},
		{"with-dash", "WITH_DASH"},
		{"with.dot", "WITH_DOT"},
		{"with_underscore", "WITH_UNDERSCORE"},
		{"123number", "_123NUMBER"},
		{"MixedCase123", "MIXEDCASE123"},
		{"special@chars!", "SPECIAL_CHARS_"},
		{"", "FIELD"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := sanitizeKey(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAttrKey(t *testing.T) {
	tests := []struct {
		groups   []string
		key      string
		expected string
	}{
		{nil, "error", "ERROR"},
		{[]string{"http"}, "status", "HTTP_STATUS"},
		{[]string{"http", "request"}, "method", "HTTP_REQUEST_METHOD"},
		{[]string{}, "simple", "SIMPLE"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := attrKey(tt.groups, tt.key)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestLevelToPriority(t *testing.T) {
	// Test that level mapping is correct
	tests := []struct {
		level    slog.Level
		priority int // journal.Priority is an int
	}{
		{slog.LevelDebug, 7},  // PriDebug
		{slog.LevelInfo, 6},   // PriInfo
		{slog.LevelWarn, 4},   // PriWarning
		{slog.LevelError, 3},  // PriErr
	}

	for _, tt := range tests {
		t.Run(tt.level.String(), func(t *testing.T) {
			result := levelToPriority(tt.level)
			assert.Equal(t, tt.priority, int(result))
		})
	}
}

func TestJournaldHandlerEnabled(t *testing.T) {
	h := &JournaldHandler{level: slog.LevelInfo}
	ctx := t.Context()

	assert.True(t, h.Enabled(ctx, slog.LevelInfo))
	assert.True(t, h.Enabled(ctx, slog.LevelWarn))
	assert.True(t, h.Enabled(ctx, slog.LevelError))
	assert.False(t, h.Enabled(ctx, slog.LevelDebug))
}

func TestJournaldHandlerWithAttrs(t *testing.T) {
	h := &JournaldHandler{level: slog.LevelInfo}

	newH := h.WithAttrs([]slog.Attr{slog.String("key", "value")})
	jh, ok := newH.(*JournaldHandler)
	assert.True(t, ok)
	assert.Len(t, jh.attrs, 1)
	assert.Equal(t, "key", jh.attrs[0].Key)
}

func TestJournaldHandlerWithGroup(t *testing.T) {
	h := &JournaldHandler{level: slog.LevelInfo}

	newH := h.WithGroup("http")
	jh, ok := newH.(*JournaldHandler)
	assert.True(t, ok)
	assert.Len(t, jh.groups, 1)
	assert.Equal(t, "http", jh.groups[0])

	// Empty group should return same handler
	sameH := h.WithGroup("")
	assert.Equal(t, h, sameH)
}
