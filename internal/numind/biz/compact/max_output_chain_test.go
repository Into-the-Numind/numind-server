package compact

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEscalateMaxTokens_FromDefault(t *testing.T) {
	assert.Equal(t, EscalatedMaxTokens, EscalateMaxTokens(DefaultMaxTokens))
	assert.Equal(t, 65536, EscalateMaxTokens(8192))
}

func TestEscalateMaxTokens_AlreadyMax(t *testing.T) {
	assert.Equal(t, EscalatedMaxTokens, EscalateMaxTokens(EscalatedMaxTokens))
}

func TestEscalateMaxTokens_AboveMax(t *testing.T) {
	// 100000 > EscalatedMaxTokens → cap at 65536
	assert.Equal(t, EscalatedMaxTokens, EscalateMaxTokens(100_000))
}

func TestConstants_AreSpecValues(t *testing.T) {
	assert.Equal(t, 8192, DefaultMaxTokens, "blueprint §4.1.6 DefaultMaxTokens")
	assert.Equal(t, 65536, EscalatedMaxTokens, "blueprint §4.1.6 EscalatedMaxTokens (64k)")
}
