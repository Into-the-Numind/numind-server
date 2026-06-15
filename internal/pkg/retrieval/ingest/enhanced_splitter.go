package ingest

import (
	"log"
	"os"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yanyiwu/gojieba"
)

// EnhancedSplitterConfig 增强版切分器配置
type EnhancedSplitterConfig struct {
	MaxChunkSize    int  // 最大切片大小（默认4000）
	MinChunkSize    int  // 最小切片大小（默认500）
	OverlapSize     int  // 前后重叠字符数（默认100）
	EnableJieba     bool // 是否启用中文分词（默认true）
	ProtectMarkdown bool // 是否保护Markdown结构（默认true）
}

// EnhancedSplitChunk 增强版切片
type EnhancedSplitChunk struct {
	Content   string   // 切片内容（包含重叠部分）
	CoreStart int      // 核心内容开始位置（不含前置重叠）
	CoreEnd   int      // 核心内容结束位置（不含后置重叠）
	Headers   []string // 标题继承链
	Level     int      // 层级深度
}

// Boundary 切分边界
type Boundary struct {
	Pos      int    // 位置
	Priority int    // 优先级（1最高，数字越大优先级越低）
	Type     string // 类型：header/paragraph/sentence/word
}

// EnhancedMarkdownSplitter 增强版Markdown切分器
type EnhancedMarkdownSplitter struct {
	cfg   EnhancedSplitterConfig
	jieba *gojieba.Jieba
}

// NewEnhancedMarkdownSplitter 创建增强版切分器
func NewEnhancedMarkdownSplitter(cfg EnhancedSplitterConfig) *EnhancedMarkdownSplitter {
	// 设置默认值
	if cfg.MaxChunkSize == 0 {
		cfg.MaxChunkSize = 4000
	}
	if cfg.MinChunkSize == 0 {
		cfg.MinChunkSize = 500
	}
	if cfg.OverlapSize == 0 {
		cfg.OverlapSize = 100
	}

	s := &EnhancedMarkdownSplitter{cfg: cfg}

	// 初始化中文分词器
	if cfg.EnableJieba {
		// 检查 Docker 环境下的字典文件是否存在
		dictPath := "/app/dict/jieba.dict.utf8"
		if _, err := os.Stat(dictPath); err == nil {
			log.Printf("[EnhancedSplitter] Using custom dictionary path: %s", dictPath)
			s.jieba = gojieba.NewJieba(
				"/app/dict/jieba.dict.utf8",
				"/app/dict/hmm_model.utf8",
				"/app/dict/user.dict.utf8",
				"/app/dict/idf.utf8",
				"/app/dict/stop_words.utf8",
			)
		} else {
			// 本地开发环境或未找到字典文件，使用默认配置
			// 注意：如果在 Docker 中未正确 COPY 字典文件，这里仍然会 Panic，
			// 但这是预期的，因为我们需要确保分词功能正常工作而不是静默失败。
			log.Println("[EnhancedSplitter] using default dictionary path")
			s.jieba = gojieba.NewJieba()
		}
	}

	return s
}

// Split 切分文本
func (s *EnhancedMarkdownSplitter) Split(text string) ([]EnhancedSplitChunk, error) {
	if text == "" {
		return []EnhancedSplitChunk{}, nil
	}

	// 1. 解析Markdown结构
	sections := s.parseMarkdownStructure(text)

	// 2. 对每个章节进行智能切分
	var allChunks []EnhancedSplitChunk
	for _, section := range sections {
		chunks := s.splitSection(section, text)
		allChunks = append(allChunks, chunks...)
	}

	// 3. 添加重叠
	allChunks = s.addOverlap(allChunks, text)

	return allChunks, nil
}

// MarkdownSection Markdown章节结构
type MarkdownSection struct {
	Level      int
	Title      string
	StartPos   int
	EndPos     int
	HeaderPath []string // 完整的标题路径
}

// parseMarkdownStructure 解析Markdown结构
func (s *EnhancedMarkdownSplitter) parseMarkdownStructure(text string) []MarkdownSection {
	var sections []MarkdownSection

	// 正则匹配Markdown标题
	headerRegex := regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
	lines := strings.Split(text, "\n")

	currentPos := 0
	var headerStack []MarkdownSection

	for _, line := range lines {
		matches := headerRegex.FindStringSubmatch(line)
		if len(matches) > 0 {
			level := len(matches[1])
			title := strings.TrimSpace(matches[2])

			// 计算在文本中的位置
			lineStartPos := currentPos
			lineEndPos := currentPos + len(line)

			// 维护标题栈：弹出层级大于等于当前的标题
			for len(headerStack) > 0 && headerStack[len(headerStack)-1].Level >= level {
				headerStack = headerStack[:len(headerStack)-1]
			}

			// 构建标题路径
			var headerPath []string
			for _, h := range headerStack {
				headerPath = append(headerPath, h.Title)
			}
			headerPath = append(headerPath, title)

			section := MarkdownSection{
				Level:      level,
				Title:      title,
				StartPos:   lineStartPos,
				EndPos:     lineEndPos,
				HeaderPath: headerPath,
			}

			// 设置上一个章节的结束位置
			if len(sections) > 0 {
				lastIdx := len(sections) - 1
				if sections[lastIdx].EndPos == 0 {
					sections[lastIdx].EndPos = lineStartPos
				}
			}

			sections = append(sections, section)
			headerStack = append(headerStack, section)
		}

		currentPos += len(line) + 1 // +1 for newline
	}

	// 设置最后一个章节的结束位置
	if len(sections) > 0 {
		lastIdx := len(sections) - 1
		if sections[lastIdx].EndPos == 0 {
			sections[lastIdx].EndPos = len(text)
		}
	}

	// 如果没有找到任何标题，将整个文本作为一个章节
	if len(sections) == 0 {
		sections = append(sections, MarkdownSection{
			Level:      0,
			Title:      "",
			StartPos:   0,
			EndPos:     len(text),
			HeaderPath: []string{},
		})
	}

	return sections
}

// splitSection 切分单个章节
func (s *EnhancedMarkdownSplitter) splitSection(section MarkdownSection, fullText string) []EnhancedSplitChunk {
	content := fullText[section.StartPos:section.EndPos]

	// 如果内容小于最大切分大小，直接返回
	if len(content) <= s.cfg.MaxChunkSize {
		return []EnhancedSplitChunk{{
			Content:   content,
			CoreStart: 0,
			CoreEnd:   len(content),
			Headers:   section.HeaderPath,
			Level:     section.Level,
		}}
	}

	// 找到所有切分边界
	boundaries := s.findBoundaries(content)

	// 调试日志
	if os.Getenv("SPLITTER_DEBUG") == "1" {
		log.Printf("[EnhancedSplitter] Section length: %d, boundaries found: %d", len(content), len(boundaries))
	}

	// 智能切分
	var chunks []EnhancedSplitChunk
	startPos := 0
	forceSplitCount := 0

	// 预留重叠空间：实际切分的 core content 必须给前后重叠留出空间
	// 每个 core content 会在 addOverlap 中：
	// 1. 在前面增加约 OverlapSize 长度的内容
	// 2. 在后面增加约 OverlapSize 长度的内容
	// 3. 前后各一个空行分隔符 "\n\n" (约 4 bytes * 2 = 8 bytes)
	// 为了确保 Core + Overlap < MaxChunkSize，我们需要严格限制 core。
	reserve := (s.cfg.OverlapSize * 2) + 50
	effectiveMax := s.cfg.MaxChunkSize - reserve

	// 防御性检查：如果 MaxChunkSize 太小导致预留后没有空间，则至少保留 1/3 的空间
	if effectiveMax < s.cfg.MaxChunkSize/3 {
		effectiveMax = s.cfg.MaxChunkSize / 3
	}
	// 确保不小于 MinChunkSize (除非 MinChunkSize 本身就很离谱)
	if effectiveMax < 50 {
		effectiveMax = 50
	}

	// 只要剩余内容大于有效限制，就继续循环
	for startPos < len(content) {
		remainingLen := len(content) - startPos
		if remainingLen <= effectiveMax {
			// 剩余内容已经足够小，作为最后一个 chunk
			chunks = append(chunks, EnhancedSplitChunk{
				Content:   content[startPos:],
				CoreStart: 0,
				CoreEnd:   remainingLen,
				Headers:   section.HeaderPath,
				Level:     section.Level,
			})
			break
		}

		// 需要切分，尝试在 [startPos + MinChunkSize, startPos + effectiveMax] 范围内寻找最优边界
		found := false
		bestBoundary := -1

		// 寻找落入当前窗口内且优先级最高的最后一个边界
		for _, b := range boundaries {
			if b.Pos <= startPos {
				continue
			}
			if b.Pos > startPos+effectiveMax {
				break
			}

			// 我们希望尽可能切在自然边界上
			if b.Pos >= startPos+s.cfg.MinChunkSize {
				bestBoundary = b.Pos
				found = true
			}
		}

		if found {
			// 在找到的最优边界处切分
			chunkContent := content[startPos:bestBoundary]
			chunks = append(chunks, EnhancedSplitChunk{
				Content:   chunkContent,
				CoreStart: 0,
				CoreEnd:   len(chunkContent),
				Headers:   section.HeaderPath,
				Level:     section.Level,
			})
			startPos = bestBoundary
		} else {
			// 没有找到合适的边界，强制切分
			forcePos := s.findForceSplitPoint(content, startPos+effectiveMax)
			// 极端情况防御：如果 findForceSplitPoint 返回的位置没有进展，执行硬截断
			if forcePos <= startPos {
				forcePos = startPos + s.cfg.MaxChunkSize
				if forcePos > len(content) {
					forcePos = len(content)
				}
			}
			chunkContent := content[startPos:forcePos]
			chunks = append(chunks, EnhancedSplitChunk{
				Content:   chunkContent,
				CoreStart: 0,
				CoreEnd:   len(chunkContent),
				Headers:   section.HeaderPath,
				Level:     section.Level,
			})
			forceSplitCount++
			startPos = forcePos
		}
	}

	if os.Getenv("SPLITTER_DEBUG") == "1" {
		log.Printf("[EnhancedSplitter] Split complete: %d chunks, %d forced splits", len(chunks), forceSplitCount)
	}

	return chunks
}

// findBoundaries 查找所有切分边界
func (s *EnhancedMarkdownSplitter) findBoundaries(text string) []Boundary {
	var boundaries []Boundary

	// 优先级1：Markdown标题
	headerRegex := regexp.MustCompile(`(?m)^#{1,6}\s+.+$`)
	for _, match := range headerRegex.FindAllStringIndex(text, -1) {
		boundaries = append(boundaries, Boundary{
			Pos:      match[0],
			Priority: 1,
			Type:     "header",
		})
	}

	// 优先级2：段落结束（双换行）
	for i := 0; i < len(text)-1; i++ {
		if text[i] == '\n' && text[i+1] == '\n' {
			// 确保不在代码块内
			if !s.isInsideCodeBlock(text, i) {
				boundaries = append(boundaries, Boundary{
					Pos:      i,
					Priority: 2,
					Type:     "paragraph",
				})
			}
		}
	}

	// 优先级3：句子结束（中英文标点）
	sentenceEnds := []rune{'。', '！', '？', '.', '!', '?'}
	for i, r := range text {
		for _, end := range sentenceEnds {
			if r == end {
				// 对于中文标点，通常不需要后跟空格
				nextPos := i + utf8.RuneLen(r)
				if end == '。' || end == '！' || end == '？' {
					boundaries = append(boundaries, Boundary{
						Pos:      nextPos,
						Priority: 3,
						Type:     "sentence",
					})
				} else {
					// 对于英文标点，通常需要后跟空格或换行，以避免切分如 "3.14" 这样的数字
					if nextPos < len(text) {
						nextChar := text[nextPos]
						if nextChar == ' ' || nextChar == '\n' || nextChar == '\r' {
							boundaries = append(boundaries, Boundary{
								Pos:      nextPos,
								Priority: 3,
								Type:     "sentence",
							})
						}
					}
				}
				break
			}
		}
	}

	// 优先级4：分词边界（中文）
	if s.cfg.EnableJieba && s.jieba != nil {
		words := s.jieba.Cut(text, true)
		pos := 0
		for _, word := range words {
			pos += len(word)
			if pos < len(text) {
				boundaries = append(boundaries, Boundary{
					Pos:      pos,
					Priority: 4,
					Type:     "word",
				})
			}
		}
	}

	// 按位置排序
	sortBoundaries(boundaries)

	return boundaries
}

// isInsideCodeBlock 检查位置是否在代码块内
func (s *EnhancedMarkdownSplitter) isInsideCodeBlock(text string, pos int) bool {
	// 简单检测：统计前方 ``` 的数量
	textBefore := text[:pos]
	count := strings.Count(textBefore, "```")
	return count%2 == 1
}

// findForceSplitPoint 强制切分点（优先句子边界，其次词语边界）
func (s *EnhancedMarkdownSplitter) findForceSplitPoint(text string, targetPos int) int {
	// 首先在 targetPos 附近寻找句子边界
	windowSize := 100
	windowStart := targetPos - windowSize
	if windowStart < 0 {
		windowStart = 0
	}
	windowEnd := targetPos + windowSize
	if windowEnd > len(text) {
		windowEnd = len(text)
	}

	// 句子结束符
	sentenceEndings := []rune{'。', '！', '？', '.', '!', '?', '；', ';', '\n'}

	// 在窗口内寻找最近的句子边界
	bestPos := -1
	minDiff := windowSize

	for i := windowStart; i < windowEnd && i < len(text); i++ {
		for _, ending := range sentenceEndings {
			if rune(text[i]) == ending {
				diff := abs(targetPos - i)
				if diff < minDiff {
					minDiff = diff
					bestPos = i + 1 // 切分点在结束符之后
				}
			}
		}
	}

	// 如果找到了句子边界，优先使用
	if bestPos > 0 && bestPos < len(text) {
		return bestPos
	}

	// 如果没有找到句子边界，使用分词边界（中文）
	if s.cfg.EnableJieba && s.jieba != nil && targetPos < len(text) {
		// 在前后50字符范围内搜索
		windowStart = targetPos - 50
		if windowStart < 0 {
			windowStart = 0
		}
		windowEnd = targetPos + 50
		if windowEnd > len(text) {
			windowEnd = len(text)
		}

		windowText := text[windowStart:windowEnd]
		words := s.jieba.Cut(windowText, true)

		// 找到最接近targetPos的词语边界
		currentPos := windowStart
		bestPos := targetPos
		minDiff = abs(targetPos - currentPos)

		for _, word := range words {
			currentPos += len(word)
			diff := abs(targetPos - currentPos)
			if diff < minDiff {
				minDiff = diff
				bestPos = currentPos
			}
		}

		return bestPos
	}

	// 最后手段：直接返回targetPos
	return targetPos
}

// addOverlap 添加前后重叠
func (s *EnhancedMarkdownSplitter) addOverlap(chunks []EnhancedSplitChunk, fullText string) []EnhancedSplitChunk {
	if len(chunks) <= 1 || s.cfg.OverlapSize <= 0 {
		return chunks
	}

	var result []EnhancedSplitChunk

	// 重叠片段之间只用纯空行分隔。历史曾在拼接处插入字面 "[上下文衔接]" 标记，但该
	// 标记会随 chunk.Content 一起被嵌入并泄漏进 LLM prompt，故不再写入；重叠区域由
	// CoreStart/CoreEnd 标定，不依赖任何可见标记。旧 chunk 的遗留标记在渲染时由
	// domain.StripContextJoinMarker 兜底剥除。
	const sep = "\n\n"

	for i, chunk := range chunks {
		newChunk := chunk
		content := chunk.Content

		// 前置重叠：从前一个chunk的末尾取
		if i > 0 {
			prevChunk := chunks[i-1]
			prevContent := prevChunk.Content
			overlapLen := s.cfg.OverlapSize
			if len(prevContent) < overlapLen {
				overlapLen = len(prevContent)
			}

			// 取前一个chunk的最后overlapLen个字符
			prefix := prevContent[len(prevContent)-overlapLen:]

			// 确保在词语边界切分
			prefix = s.trimToWordBoundary(prefix, false)

			if prefix != "" {
				newChunk.Content = prefix + sep + content
				newChunk.CoreStart = len(prefix) + len(sep)
			}
		}

		// 后置重叠：从后一个chunk的开头取
		if i < len(chunks)-1 {
			nextChunk := chunks[i+1]
			nextContent := nextChunk.Content
			overlapLen := s.cfg.OverlapSize
			if len(nextContent) < overlapLen {
				overlapLen = len(nextContent)
			}

			// 取后一个chunk的前overlapLen个字符
			suffix := nextContent[:overlapLen]

			// 确保在词语边界切分
			suffix = s.trimToWordBoundary(suffix, true)

			if suffix != "" {
				newChunk.Content = newChunk.Content + sep + suffix
				newChunk.CoreEnd = len(newChunk.Content) - len(sep) - len(suffix)
			}
		}

		result = append(result, newChunk)
	}

	return result
}

// trimToWordBoundary 修剪到词语边界
func (s *EnhancedMarkdownSplitter) trimToWordBoundary(text string, fromStart bool) string {
	if fromStart {
		// 从开头修剪，找到第一个完整的词语
		for i, r := range text {
			if i > 0 && unicode.IsSpace(r) {
				return text[i:]
			}
		}
	} else {
		// 从末尾修剪，找到最后一个完整的词语
		runes := []rune(text)
		for i := len(runes) - 1; i >= 0; i-- {
			if unicode.IsSpace(runes[i]) {
				return string(runes[:i])
			}
		}
	}
	return text
}

// sortBoundaries 按位置排序边界
func sortBoundaries(boundaries []Boundary) {
	// 简单的冒泡排序
	for i := 0; i < len(boundaries); i++ {
		for j := i + 1; j < len(boundaries); j++ {
			if boundaries[i].Pos > boundaries[j].Pos {
				boundaries[i], boundaries[j] = boundaries[j], boundaries[i]
			}
		}
	}
}

// abs 绝对值
func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// ConvertToSplitChunk 转换为旧的 SplitChunk 格式（兼容原有接口）
func (c EnhancedSplitChunk) ConvertToSplitChunk() SplitChunk {
	return SplitChunk{
		Content: c.Content,
		Headers: c.Headers,
	}
}

// ConvertToEnhancedChunks 批量转换
func ConvertToEnhancedChunks(chunks []SplitChunk) []EnhancedSplitChunk {
	var result []EnhancedSplitChunk
	for _, c := range chunks {
		result = append(result, EnhancedSplitChunk{
			Content:   c.Content,
			CoreStart: 0,
			CoreEnd:   len(c.Content),
			Headers:   c.Headers,
			Level:     0,
		})
	}
	return result
}
