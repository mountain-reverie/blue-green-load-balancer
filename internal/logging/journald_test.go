package logging

import (
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
