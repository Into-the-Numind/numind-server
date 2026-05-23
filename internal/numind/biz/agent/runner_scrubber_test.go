package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// scrubFinalAnswer 是 runner 调用 scrubber 的薄封装。本 test 覆盖关键集成点，
// scrubber 自身的 18 个 case 已在 internal/numind/biz/compactv2/scrubber/scrubber_test.go 覆盖。

func TestScrubFinalAnswer_EmptyInput_Passthrough(t *testing.T) {
	assert.Equal(t, "", scrubFinalAnswer(""))
}

func TestScrubFinalAnswer_PlainText_Untouched(t *testing.T) {
	in := "你好，这是一个普通回答，没有任何内部标签。\nSecond line."
	assert.Equal(t, in, scrubFinalAnswer(in))
}

func TestScrubFinalAnswer_StripsReferenceOnlyBlock(t *testing.T) {
	in := `用户你好，让我先回顾一下之前的对话：<reference-only data-internal="true">## 1. Active Task
之前讨论了 V1.5 上下文管理</reference-only>

基于上面的总结，我的答复是 …`
	got := scrubFinalAnswer(in)
	assert.NotContains(t, got, "<reference-only", "open tag should be stripped")
	assert.NotContains(t, got, "</reference-only>", "close tag should be stripped")
	assert.NotContains(t, got, "Active Task", "summary body should be stripped")
	assert.Contains(t, got, "基于上面的总结", "user-visible part should survive")
}

func TestScrubFinalAnswer_StripsMemoryTag(t *testing.T) {
	in := `根据你的偏好 <memory data-internal="true" id="m1">用户偏好简洁回答</memory> 我直接说重点。`
	got := scrubFinalAnswer(in)
	assert.NotContains(t, got, "<memory")
	assert.NotContains(t, got, "用户偏好简洁回答")
	assert.Contains(t, got, "我直接说重点")
}

func TestScrubFinalAnswer_PreservesUserLiteralTags(t *testing.T) {
	// 用户裸写 <memory> 不带 data-internal → scrubber 白名单约定，不剥
	in := `请帮我理解 <memory> 标签在 HTML 里有什么用？`
	got := scrubFinalAnswer(in)
	assert.Equal(t, in, got, "user's literal <memory> without data-internal should be preserved")
}

func TestScrubFinalAnswer_StripsInlinePersonalMemory(t *testing.T) {
	in := `回答用户问题 [Personal Memory: 用户上次买了 A 产品] 同时推荐 B 产品。`
	got := scrubFinalAnswer(in)
	assert.NotContains(t, got, "[Personal Memory:")
	assert.Contains(t, got, "同时推荐 B 产品")
}

func TestScrubFinalAnswer_HandlesMixedInternalAndUser(t *testing.T) {
	in := `回答 <memory data-internal="true">internal</memory> 中含有 <memory> 用户问的标签 含义。`
	got := scrubFinalAnswer(in)
	assert.NotContains(t, got, `<memory data-internal="true">internal</memory>`, "internal tag stripped")
	assert.Contains(t, got, "<memory>", "user-literal tag preserved (no data-internal)")
}

func TestScrubFinalAnswer_FastPathNoMarkers(t *testing.T) {
	// 用大字符串测 fast path 不破坏内容
	in := strings.Repeat("中文段落。Hello world. ", 500)
	got := scrubFinalAnswer(in)
	assert.Equal(t, in, got)
}
