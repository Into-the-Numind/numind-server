package compliance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPlatformHardRulesFenced_NonEmpty(t *testing.T) {
	assert.NotEmpty(t, PlatformHardRulesFenced)
}

func TestPlatformHardRulesFenced_HasFenceTags(t *testing.T) {
	assert.True(t, strings.HasPrefix(PlatformHardRulesFenced, "<platform_hard_rules>"),
		"must start with opening fence tag")
	assert.Contains(t, PlatformHardRulesFenced, "</platform_hard_rules>",
		"must contain closing fence tag")
}

func TestPlatformHardRulesFenced_Has6Rules(t *testing.T) {
	// 验证 6 条编号规则都在
	for i := 1; i <= 6; i++ {
		needle := strings.Repeat("", 0) + string(rune('0'+i)) + "."
		assert.Contains(t, PlatformHardRulesFenced, needle,
			"rule #%d should be present", i)
	}
}
