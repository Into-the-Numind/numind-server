package compliance

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultOutOfScopeNarration_NonEmpty(t *testing.T) {
	assert.NotEmpty(t, DefaultOutOfScopeNarration)
	assert.Contains(t, DefaultOutOfScopeNarration, "范围")
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short string returned as-is", "hello", 10, "hello"},
		{"exact length returned as-is", "hello", 5, "hello"},
		{"long string truncated", "hello world", 5, "hello"},
		{"empty string", "", 5, ""},
		{"zero max", "hello", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, truncate(tt.in, tt.max))
		})
	}
}
