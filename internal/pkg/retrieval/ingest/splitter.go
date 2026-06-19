package ingest

import (
	"fmt"
	"regexp"
	"strings"
)

type SplitterConfig struct {
	MaxChunkSize int
	MinChunkSize int
}

type MarkdownSplitter struct {
	cfg SplitterConfig
}

type SplitChunk struct {
	Content string
	Headers []string
	// EmbedText 可选：用于向量化的文本（通常 = 标题面包屑 + Content）。
	// 为空时管线/store 回退用 Content 向量化（老切块器零回归）。
	EmbedText string
}

type Section struct {
	Level    int
	Title    string
	Body     strings.Builder
	Children []*Section
	Parent   *Section
}

func NewMarkdownSplitter(cfg SplitterConfig) *MarkdownSplitter {
	if cfg.MaxChunkSize == 0 {
		cfg.MaxChunkSize = 1000
	}
	if cfg.MinChunkSize == 0 {
		cfg.MinChunkSize = 100
	}
	return &MarkdownSplitter{cfg: cfg}
}

func (s *MarkdownSplitter) Split(text string) ([]SplitChunk, error) {
	root := s.parseToTree(text)
	var chunks []SplitChunk
	s.flatten(root, []string{}, &chunks)
	return chunks, nil
}

// parseToTree parses markdown text into a hierarchical tree of Sections
func (s *MarkdownSplitter) parseToTree(text string) *Section {
	root := &Section{Level: 0, Title: "Root"}
	current := root

	lines := strings.Split(text, "\n")
	headerRegex := regexp.MustCompile(`^(#{1,6})\s+(.*)`)

	for _, line := range lines {
		matches := headerRegex.FindStringSubmatch(line)
		if len(matches) > 0 {
			// Found a header
			level := len(matches[1])
			title := strings.TrimSpace(matches[2])

			newSection := &Section{
				Level:  level,
				Title:  title,
				Parent: nil,
			}

			// Find correct parent
			// If level > current.Level, this is a child of current
			// If level <= current.Level, go up until we find a parent with level < level
			if level > current.Level {
				newSection.Parent = current
			} else {
				// Go up
				parent := current.Parent
				for parent != nil && parent.Level >= level {
					parent = parent.Parent
				}
				if parent == nil {
					// Should not happen if root is Level 0
					parent = root
				}
				newSection.Parent = parent
			}

			newSection.Parent.Children = append(newSection.Parent.Children, newSection)
			current = newSection
		} else {
			// Content line
			current.Body.WriteString(line + "\n")
		}
	}
	return root
}

// flatten traverses the tree and generating chunks
func (s *MarkdownSplitter) flatten(node *Section, headerTrail []string, chunks *[]SplitChunk) {
	currentTrail := make([]string, len(headerTrail))
	copy(currentTrail, headerTrail)

	if node.Level > 0 {
		// Only append actual headers (not root)
		// For H1, format might be just Title.
		// Let's store the full header string "# Title" or just "Title"
		// The requirement was: "prepending parent header trail"
		// Let's store "Title" in trail, and re-construct in Content?
		// Or store just the title.
		headerStr := fmt.Sprintf("%s %s", strings.Repeat("#", node.Level), node.Title)
		currentTrail = append(currentTrail, headerStr)
	}

	// Process Body
	body := strings.TrimSpace(node.Body.String())
	if len(body) > 0 {
		// If body is too large, split by paragraph
		if len(body) > s.cfg.MaxChunkSize {
			subChunks := s.splitByParagraphs(body, s.cfg.MaxChunkSize)
			for _, sub := range subChunks {
				*chunks = append(*chunks, s.createChunk(sub, currentTrail))
			}
		} else {
			// Small enough, check overlap/merge?
			// For now just add it.
			*chunks = append(*chunks, s.createChunk(body, currentTrail))
		}
	}

	// Recurse to children
	for _, child := range node.Children {
		s.flatten(child, currentTrail, chunks)
	}
}

func (s *MarkdownSplitter) splitByParagraphs(text string, maxLen int) []string {
	// Simple paragraph split by \n\n
	paras := strings.Split(text, "\n\n")
	var result []string
	var buffer strings.Builder

	for _, p := range paras {
		p = strings.TrimSpace(p)
		if len(p) == 0 {
			continue
		}

		// 如果单个段落本身就超过最大长度，需要对其进行强制切分
		if len(p) > maxLen {
			// 先处理 buffer 中的内容
			if buffer.Len() > 0 {
				result = append(result, buffer.String())
				buffer.Reset()
			}

			// 对超长段落进行切分
			subChunks := s.splitLongText(p, maxLen)
			result = append(result, subChunks...)
			continue
		}

		// 检查加入 buffer 后是否会超长
		if buffer.Len()+len(p)+2 > maxLen {
			if buffer.Len() > 0 {
				result = append(result, buffer.String())
				buffer.Reset()
			}
			buffer.WriteString(p)
		} else {
			if buffer.Len() > 0 {
				buffer.WriteString("\n\n")
			}
			buffer.WriteString(p)
		}
	}
	if buffer.Len() > 0 {
		result = append(result, buffer.String())
	}
	return result
}

// splitLongText 将超长文本强制切分
func (s *MarkdownSplitter) splitLongText(text string, maxLen int) []string {
	var result []string
	runes := []rune(text)
	length := len(runes)

	for i := 0; i < length; i += maxLen {
		end := i + maxLen
		if end > length {
			end = length
		}
		result = append(result, string(runes[i:end]))
	}
	return result
}

func (s *MarkdownSplitter) createChunk(content string, headers []string) SplitChunk {
	// Inject headers into content??
	// Plan: "prepend parent header trail to each chunk text"
	// Example: "# Title > ## SubTitle \n Content"

	fullContent := content
	if len(headers) > 0 {
		headerContext := strings.Join(headers, " > ")
		fullContent = fmt.Sprintf("%s\n\n%s", headerContext, content)
	}

	return SplitChunk{
		Content: fullContent,
		Headers: headers,
	}
}
