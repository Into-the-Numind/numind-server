package ingest

import (
	"fmt"
	"regexp"
	"strings"
)

// 结构感知切块策略归一值（pipeline 直接消费，不经 SplitterAdapter.normalizeStrategy）。
const (
	StrategyStructureFAQ     = "structure_faq"
	StrategyStructureOpinion = "structure_opinion"
	StrategyStructureCase    = "structure_case"
	StrategyStructureGeneric = "structure_generic"
)

// StructureAwareSplitterConfig 结构感知切块器配置（单位：rune/字符，非字节）。
type StructureAwareSplitterConfig struct {
	TargetRunes int    // 目标块大小（rune），默认 420
	MaxRunes    int    // 硬上限，默认 620；超过则按句切分
	MinRunes    int    // 最小块；相邻同面包屑的小块会被并入，默认 120
	DocName     string // 无标题时面包屑根（可选，预览/重灌时传文档名）
}

// StructureAwareSplitter 结构感知切块器：按文档结构（FAQ 问答对 / 单条观点 /
// 单案例 / 通用按节）切成聚焦小块，并把"标题面包屑 + 正文"写入每块的 EmbedText
// （用于向量化），而 Content 保持干净（返回给 LLM / 展示不含面包屑）。
//
// 退化保护：若某档切出的块明显退化（碎块爆炸 / 大文档只切 1 块），自动 fallback 到
// CompatibilitySplitter（语义/规则兜底），保证"切块层永不让入库失败/永不退化"不变式。
type StructureAwareSplitter struct {
	cfg      StructureAwareSplitterConfig
	fallback *CompatibilitySplitter
}

// NewStructureAwareSplitter 创建结构感知切块器（带默认值 + 兜底切块器）。
func NewStructureAwareSplitter(cfg StructureAwareSplitterConfig) *StructureAwareSplitter {
	if cfg.TargetRunes == 0 {
		cfg.TargetRunes = 420
	}
	if cfg.MaxRunes == 0 {
		cfg.MaxRunes = 620
	}
	if cfg.MinRunes == 0 {
		cfg.MinRunes = 120
	}
	return &StructureAwareSplitter{
		cfg:      cfg,
		fallback: NewCompatibilitySplitter(SplitterConfig{MaxChunkSize: 1000, MinChunkSize: 200}),
	}
}

// docProfile 文档结构档位。
type docProfile string

const (
	profileFAQ     docProfile = "faq"
	profileOpinion docProfile = "opinion"
	profileCase    docProfile = "case"
	profileGeneric docProfile = "generic"
)

var (
	// 编号问题（"1. xxx" / "1、xxx" / "1) xxx"）或 "问：" / "Q:" 起头（容忍前导 markdown 标题号）。
	faqMarkerRe = regexp.MustCompile(`^\s*#{0,6}\s*(\d{1,3}\s*[.、)）]\s*\S|[问Q]\s*[:：])`)
	// "观点1" / "看法二" / "主张3"（容忍前导 "## " —— 观点可能是标题）。
	opinionMarkerRe = regexp.MustCompile(`^\s*#{0,6}\s*(观点|看法|主张)\s*[一二三四五六七八九十百零\d]+`)
	// "案例1" / "客户案例二" / "实例3"（容忍前导 "## "；要求显式编号，避免 "示例"/"案例" 裸词误判）。
	caseMarkerRe = regexp.MustCompile(`^\s*#{0,6}\s*(案例|客户案例|实例|示例)\s*[一二三四五六七八九十百零\d]+`)
	headerRe     = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
)

// Split 实现 TextSplitter 接口。
func (s *StructureAwareSplitter) Split(text string) ([]SplitChunk, error) {
	chunks, _, _, _ := s.SplitWithStrategy(text)
	return chunks, nil
}

// SplitWithStrategy 实现 StrategyAwareSplitter：探测结构选档 → 切块 → validator
// 校验，退化则 fallback。不变式：err 恒 nil；非空文本必非空 chunk。
func (s *StructureAwareSplitter) SplitWithStrategy(text string) ([]SplitChunk, string, string, error) {
	if strings.TrimSpace(text) == "" {
		return []SplitChunk{}, StrategyNoSplit, "empty", nil
	}

	profile := s.profile(text)
	var (
		chunks   []SplitChunk
		strategy string
	)
	switch profile {
	case profileFAQ:
		chunks = s.segmentsToChunks(s.buildSegments(text, faqMarkerRe, false), false)
		strategy = StrategyStructureFAQ
	case profileOpinion:
		chunks = s.segmentsToChunks(s.buildSegments(text, opinionMarkerRe, false), false)
		strategy = StrategyStructureOpinion
	case profileCase:
		chunks = s.segmentsToChunks(s.buildSegments(text, caseMarkerRe, false), false)
		strategy = StrategyStructureCase
	default:
		// 通用档：仅按标题 + 段落空行切分，再贪心打包到 ~target。
		chunks = s.segmentsToChunks(s.buildSegments(text, nil, true), true)
		strategy = StrategyStructureGeneric
	}

	if ok, reason := s.validate(chunks, runeLenOf(text)); !ok {
		fb, _, fbDetail, _ := s.fallback.SplitWithStrategy(text)
		detail := fmt.Sprintf("structure_%s rejected (%s)", profile, reason)
		if fbDetail != "" {
			detail += " → " + fbDetail
		}
		return fb, StrategyFallback, detail, nil
	}

	return chunks, strategy, "", nil
}

// profile 通过 marker 行密度探测文档结构档位。除绝对计数门槛外，还要求 marker 占
// 非空行的比例达到下限——否则一篇通用产品文档里偶有 3 个编号步骤就被误判成 FAQ。
func (s *StructureAwareSplitter) profile(text string) docProfile {
	lines := strings.Split(text, "\n")
	var faq, op, cs, nonEmpty int
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonEmpty++
		}
		if faqMarkerRe.MatchString(l) {
			faq++
		}
		if opinionMarkerRe.MatchString(l) {
			op++
		}
		if caseMarkerRe.MatchString(l) {
			cs++
		}
	}
	if nonEmpty == 0 {
		return profileGeneric
	}
	ratio := func(n int) float64 { return float64(n) / float64(nonEmpty) }
	switch {
	case faq >= 3 && faq >= cs && faq >= op && ratio(faq) >= 0.04:
		return profileFAQ
	case cs >= 2 && cs >= op && ratio(cs) >= 0.03:
		return profileCase
	case op >= 3 && ratio(op) >= 0.04:
		return profileOpinion
	default:
		return profileGeneric
	}
}

type hdrEntry struct {
	level int
	title string
}

type structSegment struct {
	breadcrumb []string
	lines      []string
}

// buildSegments 把文档切成"语义段"：边界由 markdown 标题、profile 专属 marker 行、
// （通用档时）段落空行触发。每段携带其标题面包屑（祖先标题链）。
//
// markerRe 为 nil 表示不识别非标题 marker（通用档）。paragraphBoundary=true 时
// 空行也作为段边界（通用档按段落切，便于贪心打包；FAQ/观点/案例的单元跨空行不断）。
//
// 用 curIdx（索引）而非指针引用当前段——append 到 segs 可能触发底层数组重分配，
// 持有 *structSegment 会指向旧数组，是经典悬垂指针 bug。
func (s *StructureAwareSplitter) buildSegments(text string, markerRe *regexp.Regexp, paragraphBoundary bool) []structSegment {
	lines := strings.Split(text, "\n")
	var stack []hdrEntry
	var segs []structSegment
	curIdx := -1

	newSeg := func(bc []string) {
		segs = append(segs, structSegment{breadcrumb: bc})
		curIdx = len(segs) - 1
	}
	appendLine := func(line string) {
		if curIdx == -1 {
			newSeg(headerTrailTitles(stack))
		}
		segs[curIdx].lines = append(segs[curIdx].lines, line)
	}

	inCode := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// 代码块围栏：进入/退出，围栏内所有行原样归入当前段，不触发任何边界。
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			appendLine(line)
			continue
		}
		if inCode {
			appendLine(line)
			continue
		}

		// markdown 标题：弹栈到合适层级 → 入栈 → 结束当前段（标题只进面包屑，
		// 不生成"标题-only 段"——避免碎片块，且使本节正文的 bc 一致地含本标题）。
		if m := headerRe.FindStringSubmatch(line); m != nil {
			level := len(m[1])
			title := strings.TrimSpace(m[2])
			for len(stack) > 0 && stack[len(stack)-1].level >= level {
				stack = stack[:len(stack)-1]
			}
			stack = append(stack, hdrEntry{level: level, title: title})
			curIdx = -1 // flush：下一内容行起新段，bc = 含本标题的完整栈
			continue
		}

		// profile 专属 marker 行（编号问题 / 观点N / 案例N）：起新段，marker 行作正文。
		if markerRe != nil && markerRe.MatchString(line) {
			newSeg(headerTrailTitles(stack))
			segs[curIdx].lines = append(segs[curIdx].lines, line)
			continue
		}

		// 段落空行（仅通用档）：结束当前段，下一非空行起新段。
		if paragraphBoundary && trimmed == "" {
			curIdx = -1
			continue
		}

		appendLine(line)
	}

	return segs
}

// segmentsToChunks 把语义段落地为最终 chunk：合并相邻同面包屑的小段（greedyPack
// 时还把同面包屑相邻段贪心拼到 ~target），超长段按句切分。
func (s *StructureAwareSplitter) segmentsToChunks(segs []structSegment, greedyPack bool) []SplitChunk {
	type item struct {
		bc    []string
		text  string
		runes int
	}

	var items []item
	for _, sg := range segs {
		t := strings.TrimSpace(strings.Join(sg.lines, "\n"))
		if t == "" {
			continue
		}
		items = append(items, item{bc: sg.breadcrumb, text: t, runes: runeLenOf(t)})
	}

	// 合并 / 打包（仅同面包屑、合并后不超 MaxRunes）。
	//   - 通用档（greedyPack）：把相邻同面包屑的段贪心拼到 ~TargetRunes。
	//   - FAQ/观点/案例档：保持单元 1:1，仅吸收"碎片"（< MinRunes/3，如孤立短行），
	//     不合并合法短单元（否则多条观点会被糊成一块，破坏聚焦）。
	fragmentRunes := s.fragmentRunes()
	var merged []item
	for _, it := range items {
		if len(merged) > 0 {
			last := &merged[len(merged)-1]
			sameBC := slicesEqualStr(last.bc, it.bc)
			// +2：合并时插入的 "\n\n" 分隔符也占 2 rune，纳入上限判断。
			fitsMax := last.runes+it.runes+2 <= s.cfg.MaxRunes
			var shouldMerge bool
			if greedyPack {
				shouldMerge = sameBC && fitsMax && last.runes < s.cfg.TargetRunes
			} else {
				shouldMerge = sameBC && fitsMax && (last.runes < fragmentRunes || it.runes < fragmentRunes)
			}
			if shouldMerge {
				last.text = last.text + "\n\n" + it.text
				last.runes += it.runes + 2 // 含 "\n\n" 分隔符，使后续上限判断精确
				continue
			}
		}
		merged = append(merged, it)
	}

	var out []SplitChunk
	for _, it := range merged {
		if it.runes <= s.cfg.MaxRunes {
			out = append(out, s.makeChunk(it.text, it.bc))
			continue
		}
		for _, piece := range s.sentenceSplit(it.text, s.cfg.MaxRunes) {
			out = append(out, s.makeChunk(piece, it.bc))
		}
	}
	return out
}

// makeChunk 构造一块：Content 为干净正文，EmbedText = 面包屑 + 正文（用于向量化）。
func (s *StructureAwareSplitter) makeChunk(content string, bc []string) SplitChunk {
	content = strings.TrimSpace(content)
	breadcrumb := s.breadcrumbStr(bc)
	embed := content
	if breadcrumb != "" {
		embed = breadcrumb + "\n\n" + content
	}
	headers := append([]string{}, bc...)
	return SplitChunk{Content: content, Headers: headers, EmbedText: embed}
}

// breadcrumbStr 把祖先标题链拼成 "文档名 > 顶 > 节" 形式（DocName 可选）。
func (s *StructureAwareSplitter) breadcrumbStr(bc []string) string {
	parts := make([]string, 0, len(bc)+1)
	if s.cfg.DocName != "" {
		parts = append(parts, s.cfg.DocName)
	}
	parts = append(parts, bc...)
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " > ")
}

// sentenceSplit 把超长文本按中英文句末标点切成 <= max rune 的片段；含代码块（```）
// 或 markdown 表格时整体保留不切（宁可超长也不破坏代码/表格结构）；无句界则硬切。
func (s *StructureAwareSplitter) sentenceSplit(text string, max int) []string {
	if strings.Contains(text, "```") || looksLikeTable(text) {
		return []string{text}
	}
	runes := []rune(text)
	if len(runes) <= max {
		return []string{text}
	}
	// 句末标点；英文句点 '.' 仅当其后是空白时才算句界（避免切断 "3.14" / "U.S."）。
	enders := map[rune]bool{'。': true, '！': true, '？': true, '；': true, '\n': true, '!': true, '?': true, ';': true}

	var pieces []string
	start := 0
	lastBoundary := -1
	for i := 0; i < len(runes); i++ {
		isEnder := enders[runes[i]]
		if runes[i] == '.' && i+1 < len(runes) && (runes[i+1] == ' ' || runes[i+1] == '\n' || runes[i+1] == '\t') {
			isEnder = true
		}
		if isEnder {
			lastBoundary = i
		}
		if i-start+1 >= max {
			cut := lastBoundary
			if cut < start {
				cut = i // 窗口内无句界 → 硬切
			}
			if piece := strings.TrimSpace(string(runes[start : cut+1])); piece != "" {
				pieces = append(pieces, piece)
			}
			start = cut + 1
			i = cut // 重置扫描位，使 [cut+1, ...) 重新计窗口与句界
			lastBoundary = -1
		}
	}
	if start < len(runes) {
		if piece := strings.TrimSpace(string(runes[start:])); piece != "" {
			pieces = append(pieces, piece)
		}
	}
	if len(pieces) == 0 {
		return []string{text}
	}
	return pieces
}

// validate 退化检测：触发任一条件返回 (false, reason) → 调用方 fallback。
//
// 注意阈值用 fragmentRunes（~40，真碎片）而非 MinRunes（120）——FAQ/观点/案例的
// 合法单元天然短于 MinRunes（一条观点 ~50 字），用 MinRunes 判碎块会误杀正常切块。
// 只有平均接近"单字符"或大量 <40 字碎片才算退化。
func (s *StructureAwareSplitter) validate(chunks []SplitChunk, originalRunes int) (bool, string) {
	if len(chunks) == 0 {
		return false, "no chunks produced"
	}
	frag := s.fragmentRunes()
	tiny := 0
	total := 0
	for _, c := range chunks {
		rl := runeLenOf(c.Content)
		total += rl
		if rl < frag {
			tiny++
		}
		// 单块远超上限（如未闭合代码围栏吞掉全文、巨型不可切块）→ 退化。
		if rl > s.cfg.MaxRunes*2 {
			return false, "oversized chunk (unsplittable block)"
		}
	}
	avg := total / len(chunks)
	// 碎块爆炸：块数 >=3 且平均极小（接近单字符）。
	if len(chunks) >= 3 && avg < 40 {
		return false, "chunk explosion (avg < 40 runes)"
	}
	// 碎片泛滥：>50% 块是真碎片（< fragmentRunes）。
	if len(chunks) >= 3 && float64(tiny)/float64(len(chunks)) > 0.5 {
		return false, "too many fragment chunks (>50%)"
	}
	// 欠切：大文档却只切出 1 块。
	if len(chunks) == 1 && originalRunes > s.cfg.MaxRunes*2 {
		return false, "single chunk for oversized doc"
	}
	return true, ""
}

// fragmentRunes 真碎片阈值（孤立短行等），用于合并与退化判定；下限 30。
func (s *StructureAwareSplitter) fragmentRunes() int {
	f := s.cfg.MinRunes / 3
	if f < 30 {
		f = 30
	}
	return f
}

// --- 小工具 ---

func runeLenOf(s string) int {
	return len([]rune(s))
}

// looksLikeTable 粗判一段文本是否含 markdown 表格（>=3 行以 | 起头：表头+分隔+数据）。
func looksLikeTable(text string) bool {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			n++
			if n >= 3 {
				return true
			}
		}
	}
	return false
}

func headerTrailTitles(stack []hdrEntry) []string {
	t := make([]string, len(stack))
	for i, h := range stack {
		t[i] = h.title
	}
	return t
}

func slicesEqualStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
