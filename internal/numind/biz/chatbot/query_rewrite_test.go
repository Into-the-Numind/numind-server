package chatbot

import "testing"

// TestSanitizeRewrite covers the anchor-drop / pathological-output guard: a good
// rewrite passes through; empty/whitespace/runaway output falls back to the
// original query so a bad rewrite can never tank retrieval recall.
func TestSanitizeRewrite(t *testing.T) {
	const orig = "你们和市面上那些教做流量的培训机构有什么不一样?"
	long := ""
	for i := 0; i < 201; i++ {
		long += "字"
	}
	cases := []struct {
		name      string
		rewritten string
		want      string
	}{
		{"good rewrite passes through", "我们卖的是陪跑服务不是培训 与教做流量的培训机构的区别", "我们卖的是陪跑服务不是培训 与教做流量的培训机构的区别"},
		{"empty falls back", "", orig},
		{"whitespace only falls back", "   \n\t ", orig},
		{"trims surrounding whitespace", "  价格异议 销冠话术  ", "价格异议 销冠话术"},
		{"runaway (>200 runes) falls back", long, orig},
		{"exactly 200 runes kept", string([]rune(long)[:200]), string([]rune(long)[:200])},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeRewrite(orig, c.rewritten); got != c.want {
				t.Errorf("sanitizeRewrite(orig, %q) = %q, want %q", c.rewritten, got, c.want)
			}
		})
	}
}
