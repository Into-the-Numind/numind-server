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
	// 验证 6 条编号前缀 + 每条独有关键词都在（避免空行/错文也通过）
	for i := 1; i <= 6; i++ {
		prefix := string(rune('0'+i)) + "."
		assert.Contains(t, PlatformHardRulesFenced, prefix,
			"rule #%d numeric prefix should be present", i)
	}
	// 每条规则独有关键词（spec §4.3 文案锁定）
	uniqueKeywords := []string{
		"中国政治制度",    // rule 1
		"医疗诊断",      // rule 2
		"投资行为承诺",    // rule 3
		"身份证号、银行卡号", // rule 4
		"真实政治人物",    // rule 5
		"礼貌说明无法回答",  // rule 6
	}
	for _, kw := range uniqueKeywords {
		assert.Contains(t, PlatformHardRulesFenced, kw,
			"unique keyword %q should be present", kw)
	}
}
