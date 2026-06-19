package retrieve

import (
	"strings"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/retrieval/domain"
)

func TestCleanPassageForRerank(t *testing.T) {
	in := "## 价格相关\n\n这是**重点**内容，见 [详情](https://x.com/y) 和 ![图](https://img/z.png)。\n\n| 套餐 | 价格 |\n| --- | --- |\n| 基础 | 2万 |\n\n公式 $$E=mc^2$$ 结束。"
	out := cleanPassageForRerank(in)
	// markdown 标题号/强调/图片/公式应被去除。
	for _, noise := range []string{"##", "**", "![", "$$", "https://img/z.png"} {
		if strings.Contains(out, noise) {
			t.Errorf("cleaned passage still contains %q: %q", noise, out)
		}
	}
	// 链接锚文本应保留；正文应保留。
	if !strings.Contains(out, "详情") || !strings.Contains(out, "重点") || !strings.Contains(out, "基础") {
		t.Errorf("cleaned passage lost meaningful text: %q", out)
	}
	// 表格分隔行 --- 应被去掉。
	if strings.Contains(out, "---") {
		t.Errorf("cleaned passage still has table separator: %q", out)
	}
	// 空串 → 空串（不 panic）。
	if cleanPassageForRerank("") != "" {
		t.Errorf("empty input should clean to empty")
	}
	// 单 $ 价格不应被当公式删除（只 $$...$$ 算公式）。
	if p := cleanPassageForRerank("基础套餐 $100 高级套餐 $200 起"); !strings.Contains(p, "100") || !strings.Contains(p, "200") {
		t.Errorf("single-$ prices should be preserved, got %q", p)
	}
}

func TestTrigramJaccard(t *testing.T) {
	if s := trigramJaccard("完全相同的一段中文文本", "完全相同的一段中文文本"); s < 0.99 {
		t.Errorf("identical strings should be ~1.0, got %v", s)
	}
	if s := trigramJaccard("产品价格方案介绍", "天气预报今天下雨"); s > 0.1 {
		t.Errorf("disjoint strings should be ~0, got %v", s)
	}
	if s := trigramJaccard("", "abc"); s != 0 {
		t.Errorf("empty input should be 0, got %v", s)
	}
}

func TestDedupDiverse(t *testing.T) {
	chunks := []domain.KnowledgeChunk{
		{ID: "1", Content: "我们的留学申请服务覆盖选校文书网申签证五大模块全程顾问跟进"},
		{ID: "2", Content: "我们的留学申请服务覆盖选校文书网申签证五大模块全程顾问跟进哦"}, // 近重复 #1
		{ID: "3", Content: "团队规模在三十到四十人之间属于精品工作室模式多老师带一个学生"},
	}
	out := dedupDiverse(chunks, rerankDedupSimThreshold)
	if len(out) != 2 {
		t.Fatalf("expected 2 chunks after dedup (drop near-duplicate), got %d", len(out))
	}
	if out[0].ID != "1" || out[1].ID != "3" {
		t.Errorf("dedup should keep first occurrence #1 and distinct #3, got %v/%v", out[0].ID, out[1].ID)
	}
}

func rr(idx int, score float64) aiservice.RerankResult {
	return aiservice.RerankResult{Index: idx, Score: score}
}

func TestApplyRerankFilter_HardenedDegradation(t *testing.T) {
	chunks := []domain.KnowledgeChunk{{ID: "a"}, {ID: "b"}, {ID: "c"}}

	// 全部低于主阈值(0.3)但有 >0.21(=0.3*0.7) 的 → ×0.7 重试应回收。
	results := []aiservice.RerankResult{rr(0, 0.25), rr(1, 0.10), rr(2, 0.05)}
	opts := Options{RerankHardening: true} // threshold=0.3 default, !NoFloor
	got := applyRerankFilter(chunks, results, opts)
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("×0.7 retry should recover the 0.25 chunk, got %d chunks", len(got))
	}

	// 全部 < 0.15 → top-1 floor 也拒绝（不 ground 在垃圾上）。
	garbage := []aiservice.RerankResult{rr(0, 0.12), rr(1, 0.08)}
	if got := applyRerankFilter(chunks, garbage, opts); len(got) != 0 {
		t.Errorf("hardened: all <0.15 should return empty (no garbage floor), got %d", len(got))
	}

	// top-1 恰 ≥0.15 但 <0.21 → ×0.7 重试空 → top-1 floor 保 1 条。
	borderline := []aiservice.RerankResult{rr(0, 0.18), rr(1, 0.05)}
	if got := applyRerankFilter(chunks, borderline, opts); len(got) != 1 || got[0].ID != "a" {
		t.Errorf("hardened: top-1=0.18 (>=0.15) should floor to 1 chunk, got %d", len(got))
	}
}

func TestApplyRerankFilter_FlagOffUnchanged(t *testing.T) {
	chunks := []domain.KnowledgeChunk{{ID: "a"}, {ID: "b"}}
	// hardening 关 + 全低于阈值 + !NoFloor → 现状行为：保底 top-1（即使分很低）。
	results := []aiservice.RerankResult{rr(0, 0.05), rr(1, 0.02)}
	got := applyRerankFilter(chunks, results, Options{RerankHardening: false})
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("flag off should keep legacy floor top-1 unconditionally, got %d", len(got))
	}
}
