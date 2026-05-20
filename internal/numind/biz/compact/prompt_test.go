package compact

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNoToolsPreamble_Substantive(t *testing.T) {
	assert.Greater(t, len(NoToolsPreamble), 50, "preamble must be substantive enough to suppress tool use")
	assert.Contains(t, NoToolsPreamble, "禁止调用任何工具", "core instruction must be present")
}

func TestBaseCompactPrompt_Has9Sections(t *testing.T) {
	// Each section starts with "<n>. " — verify all 9 are present.
	sections := []string{
		"1. 主要请求和意图",
		"2. 关键技术概念",
		"3. 文件和代码片段",
		"4. 错误和修复",
		"5. 问题解决过程",
		"6. 所有用户消息原文",
		"7. 待办任务",
		"8. 当前进展",
		"9. 可选下一步",
	}
	for _, s := range sections {
		assert.Contains(t, BaseCompactPrompt, s, "missing section header: %s", s)
	}
}

func TestBaseCompactPrompt_VerbatimGuard(t *testing.T) {
	// Sections 6 and 9 must include verbatim guidance (intent / task drift protection).
	assert.Contains(t, BaseCompactPrompt, "防 intent drift")
	assert.Contains(t, BaseCompactPrompt, "防 task drift")
	assert.Contains(t, BaseCompactPrompt, "verbatim")
}

func TestFullCompactSystemPrompt_ContainsBothSections(t *testing.T) {
	full := FullCompactSystemPrompt()
	assert.True(t, strings.HasPrefix(full, NoToolsPreamble), "preamble must lead the prompt")
	assert.Contains(t, full, BaseCompactPrompt, "base template must be embedded")
}
