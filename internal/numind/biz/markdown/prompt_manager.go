package markdown

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/viper"
)

// MarkdownPromptManager Markdown 提示词管理器
type MarkdownPromptManager struct{}

// NewMarkdownPromptManager 创建新的 Markdown 提示词管理器
func NewMarkdownPromptManager() *MarkdownPromptManager {
	return &MarkdownPromptManager{}
}

// GetMarkdownProcessingPrompt 获取 Markdown 处理提示词
func (mpm *MarkdownPromptManager) GetMarkdownProcessingPrompt() string {
	prompt := viper.GetString("ai_prompts.markdown_processing")
	if prompt == "" {
		// 使用简化的默认 Markdown 提示词
		prompt = `请将以下文本内容转换为标准的 Markdown 格式，要求：

1. 使用 # 作为主标题
2. 在主标题后添加封面图片描述：![cover](图片描述)
3. 使用 ## 作为章节标题，### 作为小节标题
4. 使用标准 Markdown 语法：
   - **粗体** 用于强调
   - > 引用块用于重要内容
   - - 列表用于并列项目
   - 表格用于结构化数据

5. 保持内容逻辑清晰，段落分明
6. 直接输出 Markdown 内容，不要添加任何解释

现在请处理以下文本内容：`
	}
	return prompt
}

// GetCoverImagePrompt 获取封面图片生成提示词模板
func (mpm *MarkdownPromptManager) GetCoverImagePrompt() string {
	prompt := viper.GetString("ai_prompts.markdown_cover_generation")
	if prompt == "" {
		prompt = "基于标题和内容主题，生成精美的封面图片：{content}"
	}
	return prompt
}

// FormatCoverPrompt 格式化封面图片提示词
func (mpm *MarkdownPromptManager) FormatCoverPrompt(content string) string {
	template := mpm.GetCoverImagePrompt()
	return strings.ReplaceAll(template, "{content}", content)
}

// ValidateMarkdownFormat 验证 Markdown 格式是否正确
func (mpm *MarkdownPromptManager) ValidateMarkdownFormat(content string) (bool, []string) {
	var errors []string

	// 检查是否有主标题
	if !strings.Contains(content, "# ") {
		errors = append(errors, "缺少主标题（# 开头）")
	}

	// 检查是否有封面提示词
	if !strings.Contains(content, "![cover](") && !strings.Contains(content, "![](") {
		errors = append(errors, "缺少封面图片提示词（![cover]() 格式）")
	}

	// 检查是否有实际内容
	lines := strings.Split(content, "\n")
	contentLines := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "![") {
			contentLines++
		}
	}

	if contentLines == 0 {
		errors = append(errors, "缺少实际内容")
	}

	return len(errors) == 0, errors
}

// ExtractCoverPromptFromMarkdown 从 Markdown 中提取封面提示词
func (mpm *MarkdownPromptManager) ExtractCoverPromptFromMarkdown(markdown string) string {
	// 查找 ![cover](内容) 或 ![](内容) 格式
	patterns := []string{
		`!\[cover\]\(([^)]+)\)`,
		`!\[\]\(([^)]+)\)`,
	}

	for _, pattern := range patterns {
		if match := regexp.MustCompile(pattern).FindStringSubmatch(markdown); len(match) > 1 {
			return strings.TrimSpace(match[1])
		}
	}

	return ""
}

// ExtractTitleFromMarkdown 从 Markdown 中提取标题
func (mpm *MarkdownPromptManager) ExtractTitleFromMarkdown(markdown string) string {
	lines := strings.Split(markdown, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") && len(line) > 2 {
			return strings.TrimSpace(line[2:])
		}
	}
	return "无标题"
}

// CleanMarkdownContent 清理 Markdown 内容
func (mpm *MarkdownPromptManager) CleanMarkdownContent(content string) string {
	// 移除多余的空行
	lines := strings.Split(content, "\n")
	var cleaned []string
	lastWasEmpty := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		isEmpty := trimmed == ""

		if isEmpty && lastWasEmpty {
			continue // 跳过连续的空行
		}

		cleaned = append(cleaned, line)
		lastWasEmpty = isEmpty
	}

	return strings.Join(cleaned, "\n")
}

// AddMarkdownMetadata 为 Markdown 内容添加元数据
func (mpm *MarkdownPromptManager) AddMarkdownMetadata(content, author, timestamp string) string {
	metadata := fmt.Sprintf(`---
author: %s
created: %s
format: markdown
---

`, author, timestamp)

	return metadata + content
}
