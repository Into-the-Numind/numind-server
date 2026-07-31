package budget

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"numind-server/internal/pkg/model"
)

func TestDefaultLimits(t *testing.T) {
	d := DefaultLimits()
	assert.Equal(t, 300, d.MaxTurns)
	assert.Equal(t, 30*time.Minute, d.MaxWallTime)
	assert.Equal(t, int64(200000), d.MaxDailyCredits)
}

func TestDimensionConstants(t *testing.T) {
	assert.Equal(t, Dimension("max_turns"), DimMaxTurns)
	assert.Equal(t, Dimension("max_wall_time"), DimMaxWallTime)
	assert.Equal(t, Dimension("max_daily_credits"), DimMaxDailyCredits)
}

func TestLimitsFromAgentDef_NilAgentDef(t *testing.T) {
	got := LimitsFromAgentDef(nil)
	assert.Equal(t, DefaultLimits(), got)
}

func TestLimitsFromAgentDef_NilPointers(t *testing.T) {
	ad := &model.AgentDefinition{} // all *uint fields nil
	got := LimitsFromAgentDef(ad)
	assert.Equal(t, DefaultLimits(), got)
}

func TestLimitsFromAgentDef_ZeroPointers(t *testing.T) {
	zero := uint(0)
	ad := &model.AgentDefinition{
		DailyCreditCap: &zero,
	}
	// *uint 解引用为 0 → falls through to default
	got := LimitsFromAgentDef(ad)
	assert.Equal(t, DefaultLimits(), got)
}

func TestLimitsFromAgentDef_NonZeroPointers(t *testing.T) {
	dailyCap := uint(5000)
	ad := &model.AgentDefinition{
		DailyCreditCap: &dailyCap,
	}
	got := LimitsFromAgentDef(ad)
	assert.Equal(t, int64(5000), got.MaxDailyCredits)
	// 未配置的字段走 default
	assert.Equal(t, 300, got.MaxTurns)
	assert.Equal(t, 30*time.Minute, got.MaxWallTime)
}
