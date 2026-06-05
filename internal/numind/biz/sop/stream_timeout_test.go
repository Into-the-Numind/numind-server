package sop

import (
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func TestSopStreamTimeouts_Defaults(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	assert.Equal(t, 4*time.Minute, sopIdleTimeout(), "idle default = 4m")
	assert.Equal(t, 30*time.Minute, sopOverallTimeout(), "overall default = 30m")
}

func TestSopStreamTimeouts_ConfigOverride(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set("sop.stream_idle_timeout", "90s")
	viper.Set("sop.stream_overall_timeout", "10m")

	assert.Equal(t, 90*time.Second, sopIdleTimeout(), "idle honors config override")
	assert.Equal(t, 10*time.Minute, sopOverallTimeout(), "overall honors config override")
}

func TestSopStreamTimeouts_ZeroFallsBackToDefault(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	// Explicit zero / unparseable → fall back to code default (defensive).
	viper.Set("sop.stream_idle_timeout", "0s")
	assert.Equal(t, 4*time.Minute, sopIdleTimeout(), "non-positive config falls back to default")
}
