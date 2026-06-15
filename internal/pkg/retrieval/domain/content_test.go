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
			// 旧 Go 切块器在 prefix 为空时会产出以 "\n\n[marker]\n\n" 开头的内容；
			// 剥除后留下一个无害的前导空行，关键是标记本身必须消失。
			name: "canonical marker at start leaves harmless leading blank line",
			in:   "\n\n" + ContextJoinMarker + "\n\n" + "正文内容",
			want: "\n\n正文内容",
		},
		{
			name: "canonical marker at end leaves harmless trailing blank line",
			in:   "正文内容" + "\n\n" + ContextJoinMarker + "\n\n",
			want: "正文内容\n\n",
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
