package ingest

import (
	"strings"
	"testing"
)

func newTestStructureSplitter() *StructureAwareSplitter {
	return NewStructureAwareSplitter(StructureAwareSplitterConfig{})
}

// 每块 Content 不得含面包屑前缀；有标题时 EmbedText 必含面包屑。
func assertCleanContentAndBreadcrumb(t *testing.T, chunks []SplitChunk) {
	t.Helper()
	for i, c := range chunks {
		if c.Content == "" {
			t.Errorf("chunk %d: empty Content", i)
		}
		// EmbedText 必须以 Content 结尾（即 EmbedText = [breadcrumb + "\n\n" +] Content）。
		if !strings.HasSuffix(c.EmbedText, c.Content) {
			t.Errorf("chunk %d: EmbedText must end with Content; embed=%q content=%q", i, c.EmbedText, c.Content)
		}
		// 若有面包屑（EmbedText 比 Content 长），前缀必须以 "\n\n" 分隔符结尾，
		// 证明 Content 是干净正文、面包屑没渗进 Content。
		if len(c.EmbedText) > len(c.Content) {
			prefix := c.EmbedText[:len(c.EmbedText)-len(c.Content)]
			if !strings.HasSuffix(prefix, "\n\n") {
				t.Errorf("chunk %d: breadcrumb prefix malformed (Content may be polluted): prefix=%q", i, prefix)
			}
		}
	}
}

func TestStructureSplitter_FAQ(t *testing.T) {
	doc := `# 留学服务百问百答

## 价格相关

1. 你们怎么收费？
我们按服务套餐收费，基础套餐 2 万元起，包含选校、文书、申请全流程。

2. 可以分期付款吗？
支持分期，签约付 50%，录取后付尾款 50%。

3. 保 offer 失败退多少？
保 offer 套餐若未获得任何录取，全额退款。

4. 你们的老师背景如何？
顾问均毕业于 QS 前 100 院校，平均 5 年以上申请经验。`

	s := newTestStructureSplitter()
	chunks, strategy, detail, err := s.SplitWithStrategy(doc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strategy != StrategyStructureFAQ {
		t.Fatalf("expected faq strategy, got %q (detail=%q)", strategy, detail)
	}
	// 4 个问答对，可能与 section 标题段合并，但应 >=3 块、每块含一个问题。
	if len(chunks) < 3 {
		t.Fatalf("expected >=3 chunks, got %d", len(chunks))
	}
	assertCleanContentAndBreadcrumb(t, chunks)

	// 关键：含答案的语义被切到了对应问题块（"分期"应与"50%"在同一块）。
	var foundInstallment bool
	for _, c := range chunks {
		if strings.Contains(c.Content, "分期") && strings.Contains(c.Content, "尾款") {
			foundInstallment = true
		}
	}
	if !foundInstallment {
		t.Errorf("expected an FAQ chunk pairing the installment question with its answer")
	}
	// 面包屑应含 section 标题。
	var hasBreadcrumb bool
	for _, c := range chunks {
		if strings.Contains(c.EmbedText, "价格相关") {
			hasBreadcrumb = true
		}
	}
	if !hasBreadcrumb {
		t.Errorf("expected breadcrumb '价格相关' in some chunk EmbedText")
	}
}

func TestStructureSplitter_Opinion(t *testing.T) {
	doc := `# 销售观点库

观点1：先建立信任再谈产品
客户在没有信任基础时，任何产品介绍都是噪音。先用专业问题让客户感到被理解。

观点2：用提问代替陈述
与其说"我们的服务很好"，不如问"你最担心申请中的哪一步？"，让客户自己说出痛点。

观点3：异议是成交信号
当客户提出"太贵了"，说明他在认真考虑。把异议当作深入对话的入口而非拒绝。

观点4：跟进要给价值不要催
每次跟进都带一个对客户有用的信息（案例、政策、时间节点），而不是简单问"考虑得怎么样"。`

	s := newTestStructureSplitter()
	chunks, strategy, _, err := s.SplitWithStrategy(doc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strategy != StrategyStructureOpinion {
		t.Fatalf("expected opinion strategy, got %q", strategy)
	}
	if len(chunks) < 3 {
		t.Fatalf("expected >=3 opinion chunks, got %d", len(chunks))
	}
	assertCleanContentAndBreadcrumb(t, chunks)
	// 每条观点的标题应保留在 Content（不是只进面包屑）。
	var hasOpinionTitle bool
	for _, c := range chunks {
		if strings.Contains(c.Content, "异议是成交信号") {
			hasOpinionTitle = true
		}
	}
	if !hasOpinionTitle {
		t.Errorf("opinion title should be preserved in chunk Content")
	}
}

func TestStructureSplitter_Case(t *testing.T) {
	doc := `# 成功案例集

## 案例一：预算犹豫的家长
客户背景：孩子高二，家长担心留学费用太高。
解决：用分期方案 + 奖学金申请规划打消顾虑，最终签约英国 G5 申请。

## 案例二：拒绝沟通的学生
客户背景：学生本人抵触留学，家长单方面咨询。
解决：先和学生聊兴趣与职业方向，建立信任后再引入留学路径，最终学生主动配合。

## 案例三：临近 DDL 的紧急申请
客户背景：距离截止仅剩三周，材料几乎为零。
解决：启动加急服务，三天出文书初稿，按时提交并拿到录取。`

	s := newTestStructureSplitter()
	chunks, strategy, _, err := s.SplitWithStrategy(doc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strategy != StrategyStructureCase {
		t.Fatalf("expected case strategy, got %q", strategy)
	}
	if len(chunks) < 3 {
		t.Fatalf("expected >=3 case chunks, got %d", len(chunks))
	}
	assertCleanContentAndBreadcrumb(t, chunks)
}

func TestStructureSplitter_Generic(t *testing.T) {
	// 通用产品文档：长段落，按节切 + 贪心打包到 ~target。
	para := "我们的留学申请服务覆盖选校定位、背景提升、文书创作、网申递交、签证辅导五大模块，每个模块都有专属顾问跟进，确保申请质量。" // ~60 runes
	var b strings.Builder
	b.WriteString("# 产品介绍\n\n")
	b.WriteString("## 服务模块\n\n")
	for i := 0; i < 12; i++ {
		b.WriteString(para)
		b.WriteString("\n\n")
	}
	doc := b.String()

	s := newTestStructureSplitter()
	chunks, strategy, _, err := s.SplitWithStrategy(doc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strategy != StrategyStructureGeneric {
		t.Fatalf("expected generic strategy, got %q", strategy)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple packed chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if rl := runeLenOf(c.Content); rl > s.cfg.MaxRunes {
			t.Errorf("chunk %d exceeds MaxRunes: %d > %d", i, rl, s.cfg.MaxRunes)
		}
	}
	assertCleanContentAndBreadcrumb(t, chunks)
	// 面包屑应含层级链。
	if !strings.Contains(chunks[len(chunks)-1].EmbedText, "产品介绍") {
		t.Errorf("expected hierarchical breadcrumb in generic chunk EmbedText")
	}
}

func TestStructureSplitter_BreadcrumbNotInContent(t *testing.T) {
	doc := `# 顶级标题

## 二级标题

这是一段正文内容，用于验证面包屑只进 EmbedText 而不污染 Content。这段话要足够长以便形成一个独立的块，确保切块器正常工作并产出可检验的结果。`

	s := newTestStructureSplitter()
	chunks, _, _, _ := s.SplitWithStrategy(doc)
	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	for i, c := range chunks {
		// Content 不应以面包屑 "顶级标题 > 二级标题" 开头。
		if strings.HasPrefix(c.Content, "顶级标题 > 二级标题") {
			t.Errorf("chunk %d: breadcrumb leaked into Content: %q", i, c.Content)
		}
		// EmbedText 应含面包屑。
		if !strings.Contains(c.EmbedText, "顶级标题") {
			t.Errorf("chunk %d: breadcrumb missing from EmbedText", i)
		}
	}
}

func TestStructureSplitter_Empty(t *testing.T) {
	s := newTestStructureSplitter()
	chunks, strategy, _, err := s.SplitWithStrategy("   \n  ")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strategy != StrategyNoSplit {
		t.Errorf("expected no_split for empty, got %q", strategy)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty, got %d", len(chunks))
	}
}

func TestStructureSplitter_Validate(t *testing.T) {
	s := newTestStructureSplitter()
	// 正常块集 → 通过。
	good := []SplitChunk{
		{Content: strings.Repeat("正", 300)},
		{Content: strings.Repeat("常", 300)},
	}
	if ok, reason := s.validate(good, 600); !ok {
		t.Errorf("expected good chunks to validate, rejected: %s", reason)
	}
	// 碎块爆炸 → 拒绝。
	var explode []SplitChunk
	for i := 0; i < 10; i++ {
		explode = append(explode, SplitChunk{Content: "x"})
	}
	if ok, _ := s.validate(explode, 1000); ok {
		t.Errorf("expected exploded tiny chunks to be rejected")
	}
}

func TestStructureSplitter_FallbackOnDegenerate(t *testing.T) {
	// 一个超长代码块 → 不可切（sentenceSplit 整体保留）→ 单块且远超 MaxRunes*2
	// → validator "single chunk for oversized doc" → fallback。
	var b strings.Builder
	b.WriteString("```\n")
	for i := 0; i < 80; i++ {
		b.WriteString("const veryLongConfigurationLineNumber = someValueThatIsAlsoQuiteLong;\n")
	}
	b.WriteString("```\n")
	doc := b.String()
	if runeLenOf(doc) <= 620*2 {
		t.Fatalf("test setup: doc not large enough (%d runes)", runeLenOf(doc))
	}

	s := newTestStructureSplitter()
	_, strategy, detail, err := s.SplitWithStrategy(doc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strategy != StrategyFallback {
		t.Fatalf("expected fallback for oversized single-block doc, got %q (detail=%q)", strategy, detail)
	}
	if !strings.Contains(detail, "rejected") {
		t.Errorf("fallback detail should explain rejection, got %q", detail)
	}
}

func TestStructureSplitter_TableNotBroken(t *testing.T) {
	doc := `# 价格表

## 套餐对比

| 套餐 | 价格 | 包含 |
| --- | --- | --- |
| 基础 | 2万 | 选校+文书 |
| 高级 | 5万 | 全流程+保offer |

以上为我们的标准报价，具体以签约合同为准。`

	s := newTestStructureSplitter()
	chunks, _, _, _ := s.SplitWithStrategy(doc)
	// 表格的连续行应留在同一块（不被句界切碎）。
	var tableTogether bool
	for _, c := range chunks {
		if strings.Contains(c.Content, "| 基础 |") && strings.Contains(c.Content, "| 高级 |") {
			tableTogether = true
		}
	}
	if !tableTogether {
		t.Errorf("table rows should stay in one chunk; chunks=%d", len(chunks))
	}
}
