package salesrag

import (
	"strings"
	"testing"
)

func TestFormatKBFallbackNote(t *testing.T) {
	// 空列表 → 空串（不注入兜底）。
	if formatKBFallbackNote(nil) != "" {
		t.Errorf("empty names should yield empty note")
	}
	if formatKBFallbackNote([]string{}) != "" {
		t.Errorf("empty slice should yield empty note")
	}

	note := formatKBFallbackNote([]string{"产品手册", "百问百答手册", "客户案例库"})
	// 应列出全部文档名 + 含如实拒答指令关键短语。
	for _, want := range []string{"产品手册", "百问百答手册", "客户案例库", "暂时没有", "收录内容范围", "绝不编造"} {
		if !strings.Contains(note, want) {
			t.Errorf("fallback note missing %q; note=%q", want, note)
		}
	}
}
