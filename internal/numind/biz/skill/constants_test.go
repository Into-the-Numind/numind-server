package skill

import (
	"strings"
	"testing"
)

// TestPlatformBasePrompt_NoEmojiInstruction 防回归：PlatformBasePrompt 必须包含
// 「输出风格」段，明确指示 agent 回答正文不要用 emoji / 表情符号装饰，改用 markdown
// 结构（标题 / 加粗 / 列表 / 分隔线）组织内容。
//
// 背景：agent-output-polish feature #3 —— agent 回答里大量装饰性 emoji 显得不专业、
// 信息密度低；通过基础 prompt 统一约束输出风格。
func TestPlatformBasePrompt_NoEmojiInstruction(t *testing.T) {
	wantSubstrings := []string{
		"## 输出风格",    // 必须有「输出风格」段标题
		"不要使用 emoji", // 必须明确说不要使用 emoji
		"表情符号",       // 表情符号相关说明
		"markdown",   // 引导用 markdown 结构组织内容
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(PlatformBasePrompt, sub) {
			t.Errorf("PlatformBasePrompt 缺少预期子串 %q", sub)
		}
	}
}
