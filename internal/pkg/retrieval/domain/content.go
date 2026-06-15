package domain

import "strings"

// ContextJoinMarker 是规则兜底切块器历史在重叠拼接处插入的字面标记。
//
// 旧 chunk（dev 上 673/688 含此标记）渲染进 LLM prompt 或面向用户的引用前
// 必须 strip；新切块已不再插入（见 ingest 包 EnhancedMarkdownSplitter.addOverlap）。
// 本常量是该标记字面量的唯一真相源。
const ContextJoinMarker = "[上下文衔接]"

// StripContextJoinMarker 移除 chunk 内容里历史遗留的 [上下文衔接] 切块标记，
// 防止内部切块标记泄漏进 LLM prompt 或引用展示。
//
// 切块器历史插入的规范形态是 "\n\n[上下文衔接]\n\n"，剥除后折叠回单个空行分隔，
// 保留前后重叠文本之间的段落边界；对任何非规范包裹的残留标记做兜底清除。
// 对不含标记的内容零额外分配直接返回原串。
func StripContextJoinMarker(s string) string {
	if !strings.Contains(s, ContextJoinMarker) {
		return s
	}
	// 规范形态折叠为单个空行。
	s = strings.ReplaceAll(s, "\n\n"+ContextJoinMarker+"\n\n", "\n\n")
	// 兜底：清除任何非规范包裹的残留标记。
	return strings.ReplaceAll(s, ContextJoinMarker, "")
}
