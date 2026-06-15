package domain

import (
	"strings"
	"testing"
)

func TestStripContextJoinMarker(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "canonical marker collapses to single blank line",
			in:   "前文内容" + "\n\n" + ContextJoinMarker + "\n\n" + "后文内容",
			want: "前文内容\n\n后文内容",
		},
		{
			name: "multiple canonical markers (prefix+suffix overlap)",
			in:   "## V014 ·\n\n" + ContextJoinMarker + "\n\n## V015 · 标题\n\n" + ContextJoinMarker + "\n\n V016 · 标题",
			want: "## V014 ·\n\n## V015 · 标题\n\n V016 · 标题",
		},
		{
			name: "bare marker without canonical wrapping is removed",
			in:   "前文" + ContextJoinMarker + "后文",
			want: "前文后文",
		},
		{
			name: "no marker returns input unchanged",
			in:   "正常的知识库内容，没有任何标记。",
			want: "正常的知识库内容，没有任何标记。",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripContextJoinMarker(tc.in)
			if got != tc.want {
				t.Errorf("StripContextJoinMarker(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// 回归保护：strip 后绝不能再含内部切块标记。
			if strings.Contains(got, "上下文衔接") {
				t.Errorf("StripContextJoinMarker left a leaked marker in: %q", got)
			}
		})
	}
}
