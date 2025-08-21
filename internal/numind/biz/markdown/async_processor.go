package markdown

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"io"
	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/book"
	"numind-server/internal/numind/biz/volc"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/spf13/viper"
)

// MarkdownAsyncProcessor Markdown 异步处理器
type MarkdownAsyncProcessor struct {
	promptManager     *MarkdownPromptManager
	processor         *MarkdownProcessor
	htmlConverter     *HTMLConverter
	paginationAdapter *MarkdownPaginationAdapter
	renderer          *LightweightMarkdownRenderer
	aliBiz            ali.AliBiz
	volcBiz           volc.VolcBiz
	bookBiz           book.AsyncBookBiz
	cardBiz           book.AsyncCardBiz
	config            *ProcessorConfig
}

// ProcessorConfig 处理器配置
type ProcessorConfig struct {
	EnableRetry        bool          `json:"enable_retry"`        // 启用重试
	MaxRetries         int           `json:"max_retries"`         // 最大重试次数
	RetryDelay         time.Duration `json:"retry_delay"`         // 重试延迟
	ProcessTimeout     time.Duration `json:"process_timeout"`     // 处理超时时间
	AIMaxTokens        int           `json:"ai_max_tokens"`       // AI 最大 Token 数
	AITemperature      float64       `json:"ai_temperature"`      // AI 温度参数
	EnableValidation   bool          `json:"enable_validation"`   // 启用内容验证
	EnableOptimization bool          `json:"enable_optimization"` // 启用内容优化
}

// ProcessResult 处理结果
type ProcessResult struct {
	BookID        uint                    `json:"book_id"`
	Title         string                  `json:"title"`
	CoverPrompt   string                  `json:"cover_prompt"`
	TotalCards    int                     `json:"total_cards"`
	RenderedCards []*RenderedMarkdownCard `json:"rendered_cards"`
	ProcessTime   time.Duration           `json:"process_time"`
	Stats         map[string]interface{}  `json:"stats"`
	Success       bool                    `json:"success"`
	ErrorMessage  string                  `json:"error_message,omitempty"`
}

// NewMarkdownAsyncProcessor 创建新的 Markdown 异步处理器
func NewMarkdownAsyncProcessor(
	aliBiz ali.AliBiz,
	volcBiz volc.VolcBiz,
	bookBiz book.AsyncBookBiz,
	cardBiz book.AsyncCardBiz,
) *MarkdownAsyncProcessor {
	return &MarkdownAsyncProcessor{
		promptManager:     NewMarkdownPromptManager(),
		processor:         NewMarkdownProcessor(),
		htmlConverter:     NewHTMLConverter(),
		paginationAdapter: NewMarkdownPaginationAdapter(),
		renderer:          NewLightweightMarkdownRenderer(),
		aliBiz:            aliBiz,
		volcBiz:           volcBiz,
		bookBiz:           bookBiz,
		cardBiz:           cardBiz,
		config: &ProcessorConfig{
			EnableRetry:        true,
			MaxRetries:         3,
			RetryDelay:         5 * time.Second,
			ProcessTimeout:     5 * time.Minute,
			AIMaxTokens:        4000,
			AITemperature:      0.3,
			EnableValidation:   true,
			EnableOptimization: false,
		},
	}
}

// ProcessBookAsync 异步处理书籍创建
func (mpa *MarkdownAsyncProcessor) ProcessBookAsync(
	ctx context.Context,
	userID uint,
	inputText string,
	templateID string,
) (*ProcessResult, error) {
	startTime := time.Now()

	log.C(ctx).Infow("开始 Markdown 异步处理",
		"user_id", userID,
		"input_length", len(inputText),
		"template_id", templateID)

	// 创建带超时的上下文
	processCtx, cancel := context.WithTimeout(ctx, mpa.config.ProcessTimeout)
	defer cancel()

	// 1. 创建书籍记录
	book := &model.BookM{
		UserID:     userID,
		TemplateID: templateID,
		Status:     model.BookStatusCreating,
	}

	if err := mpa.bookBiz.Create(processCtx, book); err != nil {
		return nil, fmt.Errorf("failed to create book record: %v", err)
	}

	bookID := book.ID
	log.C(ctx).Infow("书籍记录创建成功", "book_id", bookID)

	// 异步处理（避免阻塞响应）
	go func() {
		result := mpa.processBookContent(context.Background(), bookID, userID, inputText, templateID)

		// 更新书籍状态
		if result.Success {
			mpa.updateBookStatus(context.Background(), bookID, model.BookStatusSuccess, "")
			log.C(ctx).Infow("书籍处理完成",
				"book_id", bookID,
				"total_cards", result.TotalCards,
				"process_time", result.ProcessTime)
		} else {
			mpa.updateBookStatus(context.Background(), bookID, model.BookStatusFailed, result.ErrorMessage)
			log.C(ctx).Errorw("书籍处理失败",
				"book_id", bookID,
				"error", result.ErrorMessage)
		}
	}()

	// 返回初始结果
	return &ProcessResult{
		BookID:      bookID,
		Success:     true,
		ProcessTime: time.Since(startTime),
	}, nil
}

// processBookContent 处理书籍内容的核心逻辑
func (mpa *MarkdownAsyncProcessor) processBookContent(
	ctx context.Context,
	bookID uint,
	userID uint,
	inputText string,
	templateID string,
) *ProcessResult {
	startTime := time.Now()

	log.C(ctx).Infow("开始处理书籍内容",
		"book_id", bookID,
		"user_id", userID)

	result := &ProcessResult{
		BookID: bookID,
	}

	// 1. 调用 AI 生成 Markdown 内容
	markdownContent, err := mpa.generateMarkdownContent(ctx, inputText)
	if err != nil {
		result.Success = false
		result.ErrorMessage = fmt.Sprintf("AI 生成失败: %v", err)
		return result
	}

	log.C(ctx).Infow("AI Markdown 生成完成",
		"book_id", bookID,
		"markdown_length", len(markdownContent))

	// 2. 解析和验证 Markdown 内容
	parsedContent, err := mpa.parseAndValidateMarkdown(ctx, markdownContent)
	if err != nil {
		result.Success = false
		result.ErrorMessage = fmt.Sprintf("Markdown 解析失败: %v", err)
		return result
	}

	result.Title = parsedContent.Title
	result.CoverPrompt = parsedContent.CoverPrompt

	// 更新书籍标题
	mpa.updateBookTitle(ctx, bookID, parsedContent.Title, parsedContent.CoverPrompt)

	// 3. 生成封面图片
	log.C(ctx).Infow("开始生成封面图片", "cover_prompt", parsedContent.CoverPrompt)
	coverImageURL, err := mpa.generateCoverImage(ctx, parsedContent.CoverPrompt, bookID) // 传递封面提示词
	if err != nil {
		log.C(ctx).Warnw("封面图片生成失败，继续处理", "error", err.Error())
		coverImageURL = "" // 使用默认封面
	} else {
		log.C(ctx).Infow("封面图片生成成功", "cover_image_url", coverImageURL)
		// 更新书籍封面图片
		mpa.updateBookCoverImage(ctx, bookID, coverImageURL)
	}

	// 4. 分页和渲染
	log.C(ctx).Infow("开始分页和渲染处理",
		"book_id", bookID,
		"template_id", templateID,
		"markdown_content_length", len(markdownContent))

	// 如果有封面图片，将其添加到Markdown内容的开头
	if coverImageURL != "" {
		markdownContent = fmt.Sprintf("![cover](%s)\n\n%s", coverImageURL, markdownContent)
		log.C(ctx).Infow("已将封面图片添加到Markdown内容", "cover_image_url", coverImageURL, "new_markdown_length", len(markdownContent))
	} else {
		log.C(ctx).Warnw("没有封面图片，将使用占位符")
		// 添加占位符图片
		markdownContent = fmt.Sprintf("![cover](封面图片)\n\n%s", markdownContent)
	}

	renderedCards, err := mpa.renderMarkdownToCards(ctx, markdownContent, bookID, templateID)
	if err != nil {
		log.C(ctx).Errorw("渲染失败", "error", err.Error())
		result.Success = false
		result.ErrorMessage = fmt.Sprintf("渲染失败: %v", err)
		return result
	}

	result.RenderedCards = renderedCards
	result.TotalCards = len(renderedCards)

	// 4. 保存卡片数据
	if err := mpa.saveCardsToDatabase(ctx, renderedCards, bookID, userID, parsedContent); err != nil {
		result.Success = false
		result.ErrorMessage = fmt.Sprintf("保存卡片失败: %v", err)
		return result
	}

	// 5. 生成统计信息
	result.Stats = mpa.renderer.GetRenderStats(renderedCards)
	result.ProcessTime = time.Since(startTime)
	result.Success = true

	log.C(ctx).Infow("书籍内容处理完成",
		"book_id", bookID,
		"total_cards", result.TotalCards,
		"process_time", result.ProcessTime)

	return result
}

// generateMarkdownContent 调用 AI 生成 Markdown 内容（优先火山方舟，降级阿里云百炼）
func (mpa *MarkdownAsyncProcessor) generateMarkdownContent(ctx context.Context, inputText string) (string, error) {
	// 使用AliBiz的PromptManager获取文本处理提示词
	aliPromptManager := mpa.aliBiz.GetPromptManager()
	prompt := aliPromptManager.GetTextProcessingPrompt()

	log.C(ctx).Infow("开始调用 AI 生成 Markdown 内容",
		"input_length", len(inputText),
		"prompt_length", len(prompt),
		"max_tokens", mpa.config.AIMaxTokens,
		"temperature", mpa.config.AITemperature)

	// 构建消息
	messages := []map[string]string{
		{
			"role":    "system",
			"content": prompt,
		},
		{
			"role":    "user",
			"content": inputText,
		},
	}

	log.C(ctx).Infow("AI 消息构建完成",
		"messages_count", len(messages),
		"system_content_length", len(messages[0]["content"]),
		"user_content_length", len(messages[1]["content"]))

	// 优先尝试火山方舟API
	log.C(ctx).Infow("🚀 优先尝试火山方舟API")
	response, err := mpa.volcBiz.VolcTextStream(ctx, messages, mpa.config.AIMaxTokens, mpa.config.AITemperature)
	if err != nil {
		log.C(ctx).Warnw("⚠️ 火山方舟API失败，降级到阿里云百炼", "error", err.Error())

		// 降级到阿里云百炼
		var lastErr error
		for attempt := 1; attempt <= mpa.config.MaxRetries; attempt++ {
			log.C(ctx).Infow("调用阿里云百炼生成 Markdown",
				"attempt", attempt,
				"max_retries", mpa.config.MaxRetries)

			response, err = mpa.aliBiz.QianwenTextStream(
				messages,
				mpa.config.AIMaxTokens,
				mpa.config.AITemperature,
			)

			if err != nil {
				lastErr = err
				log.C(ctx).Warnw("阿里云百炼调用失败，准备重试",
					"attempt", attempt,
					"error", err)

				if attempt < mpa.config.MaxRetries {
					time.Sleep(mpa.config.RetryDelay)
					continue
				}
			} else {
				log.C(ctx).Infow("✅ 阿里云百炼API调用成功")
				break
			}
		}

		if err != nil {
			return "", fmt.Errorf("所有AI API都失败: volc=%w, qianwen=%w", err, lastErr)
		}
	}

	log.C(ctx).Infow("文本大模型响应", "response", response)

	// 清理AI响应
	cleanedResponse := mpa.cleanAIResponse(response)

	log.C(ctx).Infow("AI 响应处理完成",
		"raw_response_length", len(response),
		"cleaned_response_length", len(cleanedResponse),
		"raw_response_preview", truncateString(response, 500),
		"cleaned_response_preview", truncateString(cleanedResponse, 500))

	return cleanedResponse, nil
}

// generateCoverImage 生成封面图片
func (mpa *MarkdownAsyncProcessor) generateCoverImage(ctx context.Context, coverPrompt string, bookID uint) (string, error) {
	log.C(ctx).Infow("开始生成封面图片",
		"cover_prompt", coverPrompt,
		"cover_prompt_length", len(coverPrompt),
		"book_id", bookID)

	// 验证封面提示词
	if coverPrompt == "" {
		log.C(ctx).Errorw("封面提示词为空")
		return "", fmt.Errorf("封面提示词为空")
	}

	// 如果封面提示词是默认的"图片描述"，使用标题生成提示词
	if coverPrompt == "图片描述" {
		log.C(ctx).Warnw("封面提示词是默认值，使用标题生成提示词")
		// 这里需要获取标题，暂时使用默认标题
		title := "AI 智能卡册"
		aliPromptManager := mpa.aliBiz.GetPromptManager()
		coverPrompt = aliPromptManager.FormatImagePrompt(title)
		log.C(ctx).Infow("使用标题生成图片提示词",
			"title", title,
			"formatted_prompt", coverPrompt)
	}

	log.C(ctx).Infow("调用SD生成封面图片", "image_prompt", coverPrompt)

	// 调用SD生成图片
	remoteImageURL, err := mpa.aliBiz.StableDiffusionImageAsync(coverPrompt, "1024*1024")
	if err != nil {
		log.C(ctx).Errorw("SD生成封面图片失败", "error", err.Error())
		return "", err
	}

	log.C(ctx).Infow("SD生成封面图片成功", "remote_image_url", remoteImageURL)

	// 下载并保存到本地
	localImagePath, err := mpa.downloadAndSaveCoverImage(ctx, remoteImageURL, bookID)
	if err != nil {
		log.C(ctx).Errorw("下载封面图片失败", "error", err.Error())
		return "", err
	}

	log.C(ctx).Infow("封面图片保存到本地成功", "local_image_path", localImagePath)
	return localImagePath, nil
}

// downloadAndSaveCoverImage 下载并保存封面图片到本地
func (mpa *MarkdownAsyncProcessor) downloadAndSaveCoverImage(ctx context.Context, remoteURL string, bookID uint) (string, error) {
	// 获取书籍ID（这里需要从上下文或其他方式获取）
	// 暂时使用时间戳作为临时ID
	// bookID := uint(time.Now().Unix()) // This line is removed as bookID is now a parameter

	// 计算本地保存目录：{image_path}/book/{book_id}
	imagePath := viper.GetString("resource.image_path")
	localDir := filepath.Join(imagePath, "book", fmt.Sprintf("%d", bookID))

	log.C(ctx).Infow("开始下载封面图片",
		"remote_url", remoteURL,
		"local_dir", localDir)

	// 确保目录存在
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", localDir, err)
	}

	// 固定文件名：book_{id}.webp
	localFilePath := filepath.Join(localDir, fmt.Sprintf("book_%d.webp", bookID))

	// 下载远程图片
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(remoteURL)
	if err != nil {
		return "", fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download image, status: %d", resp.StatusCode)
	}

	// 创建本地文件并写入
	file, err := os.Create(localFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to create local file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return "", fmt.Errorf("failed to save image: %w", err)
	}

	log.C(ctx).Infow("封面图片下载完成",
		"local_file_path", localFilePath)

	// 返回相对路径，用于HTML中的src属性
	relativePath := strings.TrimPrefix(localFilePath, viper.GetString("resource.image_path"))
	if !strings.HasPrefix(relativePath, "/") {
		relativePath = "/" + relativePath
	}

	return relativePath, nil
}

// updateBookCoverImage 更新书籍封面图片
func (mpa *MarkdownAsyncProcessor) updateBookCoverImage(ctx context.Context, bookID uint, imageURL string) {
	book, err := mpa.bookBiz.GetByID(ctx, bookID)
	if err != nil {
		log.C(ctx).Errorw("获取书籍记录失败", "book_id", bookID, "error", err.Error())
		return
	}

	book.ImageUrl = imageURL
	if err := mpa.bookBiz.Update(ctx, book); err != nil {
		log.C(ctx).Errorw("更新书籍封面图片失败", "book_id", bookID, "error", err.Error())
	}
}

// truncateString 截断字符串用于日志显示
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// parseAndValidateMarkdown 解析和验证 Markdown 内容
func (mpa *MarkdownAsyncProcessor) parseAndValidateMarkdown(ctx context.Context, markdownText string) (*MarkdownContent, error) {
	log.C(ctx).Infow("开始解析 Markdown 内容",
		"markdown_length", len(markdownText),
		"markdown_preview", truncateString(markdownText, 300))

	// 解析 Markdown
	content, err := mpa.processor.ParseMarkdown(markdownText)
	if err != nil {
		log.C(ctx).Errorw("解析 Markdown 失败", "error", err.Error())
		return nil, fmt.Errorf("解析 Markdown 失败: %v", err)
	}

	log.C(ctx).Infow("Markdown 解析完成",
		"title", content.Title,
		"cover_prompt", content.CoverPrompt,
		"cover_prompt_length", len(content.CoverPrompt),
		"content_blocks_count", len(content.ContentBlocks))

	// 验证内容完整性
	if content.Title == "" || content.Title == "无标题" {
		log.C(ctx).Warnw("Markdown 内容缺少有效标题，使用默认标题")
		content.Title = "AI 智能卡册"
	}

	if len(content.ContentBlocks) == 0 {
		log.C(ctx).Errorw("Markdown 内容为空")
		return nil, fmt.Errorf("Markdown 内容为空")
	}

	if content.CoverPrompt == "" {
		log.C(ctx).Warnw("Markdown 内容缺少封面提示词，使用默认提示词")
		content.CoverPrompt = fmt.Sprintf("根据标题'%s'生成精美的封面图片", content.Title)
	} else if content.CoverPrompt == "图片描述" {
		log.C(ctx).Warnw("封面提示词是默认值'图片描述'，使用基于标题的提示词")
		content.CoverPrompt = fmt.Sprintf("根据标题'%s'生成精美的封面图片，现代简约风格，高质量渲染", content.Title)
	}

	log.C(ctx).Infow("Markdown 解析和验证完成",
		"title", content.Title,
		"cover_prompt", content.CoverPrompt,
		"cover_prompt_length", len(content.CoverPrompt),
		"content_blocks", len(content.ContentBlocks))

	return content, nil
}

// renderMarkdownToCards 将 Markdown 渲染为卡片
func (mpa *MarkdownAsyncProcessor) renderMarkdownToCards(
	ctx context.Context,
	markdownText string,
	bookID uint,
	templateID string,
) ([]*RenderedMarkdownCard, error) {
	// 获取模板背景（如果有）
	templateBackground := mpa.getTemplateBackground(templateID)

	// 渲染
	renderedCards, err := mpa.renderer.RenderMarkdownToCards(
		ctx,
		markdownText,
		bookID,
		templateBackground,
	)

	if err != nil {
		return nil, fmt.Errorf("渲染 Markdown 失败: %v", err)
	}

	if len(renderedCards) == 0 {
		return nil, fmt.Errorf("没有生成任何卡片")
	}

	return renderedCards, nil
}

// saveCardsToDatabase 保存卡片到数据库
func (mpa *MarkdownAsyncProcessor) saveCardsToDatabase(
	ctx context.Context,
	renderedCards []*RenderedMarkdownCard,
	bookID uint,
	userID uint,
	parsedContent *MarkdownContent,
) error {
	// 获取卡片内容
	cardContents, err := mpa.paginationAdapter.PaginateMarkdownContent(
		fmt.Sprintf("# %s\n\n![cover](%s)\n\n%s",
			parsedContent.Title,
			parsedContent.CoverPrompt,
			mpa.contentBlocksToMarkdown(parsedContent.ContentBlocks)))
	if err != nil {
		return fmt.Errorf("重新分页失败: %v", err)
	}

	if len(renderedCards) != len(cardContents) {
		return fmt.Errorf("渲染卡片数量与内容数量不匹配")
	}

	// 转换为卡片模型
	cards, err := mpa.renderer.ConvertToCardModel(renderedCards, cardContents, bookID, userID)
	if err != nil {
		return fmt.Errorf("转换卡片模型失败: %v", err)
	}

	// 保存到数据库
	for i, card := range cards {
		if err := mpa.cardBiz.Create(ctx, card); err != nil {
			log.C(ctx).Errorw("保存卡片失败",
				"book_id", bookID,
				"card_index", i+1,
				"error", err)
			return fmt.Errorf("保存卡片 %d 失败: %v", i+1, err)
		}

		// 更新渲染结果中的卡片ID
		renderedCards[i].CardID = card.ID
	}

	log.C(ctx).Infow("所有卡片保存完成",
		"book_id", bookID,
		"total_cards", len(cards))

	return nil
}

// 辅助方法

// cleanAIResponse 清理 AI 响应
func (mpa *MarkdownAsyncProcessor) cleanAIResponse(response string) string {
	// 移除可能的解释性文字
	cleaned := strings.TrimSpace(response)

	// 查找第一个 # 开始的位置
	lines := strings.Split(cleaned, "\n")
	var markdownStart int = -1

	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			markdownStart = i
			break
		}
	}

	if markdownStart >= 0 {
		cleaned = strings.Join(lines[markdownStart:], "\n")
	}

	return mpa.promptManager.CleanMarkdownContent(cleaned)
}

// updateBookStatus 更新书籍状态
func (mpa *MarkdownAsyncProcessor) updateBookStatus(ctx context.Context, bookID uint, status string, errorMsg string) {
	// 先获取现有的书籍记录，然后更新状态
	book, err := mpa.bookBiz.GetByID(ctx, bookID)
	if err != nil {
		log.C(ctx).Errorw("获取书籍记录失败", "book_id", bookID, "error", err.Error())
		return
	}

	book.Status = status

	if err := mpa.bookBiz.Update(ctx, book); err != nil {
		log.C(ctx).Errorw("更新书籍状态失败",
			"book_id", bookID,
			"status", status,
			"error", err.Error())
	}
}

// updateBookTitle 更新书籍标题
func (mpa *MarkdownAsyncProcessor) updateBookTitle(ctx context.Context, bookID uint, title, coverPrompt string) {
	// 先获取现有的书籍记录，然后更新标题
	book, err := mpa.bookBiz.GetByID(ctx, bookID)
	if err != nil {
		log.C(ctx).Errorw("获取书籍记录失败", "book_id", bookID, "error", err.Error())
		return
	}

	book.Title = title

	if err := mpa.bookBiz.Update(ctx, book); err != nil {
		log.C(ctx).Errorw("更新书籍标题失败",
			"book_id", bookID,
			"title", title,
			"error", err.Error())
	}
}

// getTemplateBackground 获取模板背景
func (mpa *MarkdownAsyncProcessor) getTemplateBackground(templateID string) string {
	// 这里可以从模板配置中获取背景图片路径
	// 暂时返回空字符串，表示使用默认背景
	return ""
}

// UpdateConfig 更新配置
func (mpa *MarkdownAsyncProcessor) UpdateConfig(config *ProcessorConfig) {
	mpa.config = config
}

// GetConfig 获取当前配置
func (mpa *MarkdownAsyncProcessor) GetConfig() *ProcessorConfig {
	return mpa.config
}

// contentBlocksToMarkdown 将内容块转换为 Markdown 文本
func (mpa *MarkdownAsyncProcessor) contentBlocksToMarkdown(blocks []MarkdownContentBlock) string {
	var parts []string
	for _, block := range blocks {
		parts = append(parts, block.RawText)
	}
	return strings.Join(parts, "\n\n")
}
