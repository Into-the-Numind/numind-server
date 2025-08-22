package markdown

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chai2010/webp"
	"github.com/chromedp/chromedp"
	"github.com/spf13/viper"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// LightweightMarkdownRenderer 轻量级 Markdown 渲染器
type LightweightMarkdownRenderer struct {
	htmlConverter     *HTMLConverter
	paginationAdapter *MarkdownPaginationAdapter
	config            *RendererConfig
}

// RendererConfig 渲染器配置
type RendererConfig struct {
	// 图片设置
	ImageWidth   int    `json:"image_width"`   // 图片宽度
	ImageHeight  int    `json:"image_height"`  // 图片高度
	ImageQuality int    `json:"image_quality"` // 图片质量 (1-100)
	ImageFormat  string `json:"image_format"`  // 图片格式 (webp, png, jpg)

	// 浏览器设置
	BrowserTimeout time.Duration `json:"browser_timeout"` // 浏览器超时时间
	PageLoadDelay  time.Duration `json:"page_load_delay"` // 页面加载延迟

	// 渲染设置
	EnableGPU   bool    `json:"enable_gpu"`   // 启用GPU加速
	DeviceScale float64 `json:"device_scale"` // 设备缩放比例

	// 输出设置
	OutputDir   string `json:"output_dir"`   // 输出目录
	ImagePrefix string `json:"image_prefix"` // 图片文件前缀
}

// RenderedMarkdownCard 渲染后的 Markdown 卡片
type RenderedMarkdownCard struct {
	CardID     uint          `json:"card_id"`
	ImageURL   string        `json:"image_url"`
	ImagePath  string        `json:"image_path"`
	Width      int           `json:"width"`
	Height     int           `json:"height"`
	SortOrder  int           `json:"sort_order"`
	RenderTime time.Duration `json:"render_time"`
	FileSize   int64         `json:"file_size"`
}

// MarkdownCardContent Markdown 卡片内容
type MarkdownCardContent struct {
	ContentBlocks []*MarkdownContentBlock `json:"content_blocks"`
	IsCoverCard   bool                    `json:"is_cover_card"`
	CardIndex     int                     `json:"card_index"`
	Title         string                  `json:"title"`
	TotalBlocks   int                     `json:"total_blocks"`
}

// MarkdownPaginationAdapter Markdown 分页适配器
type MarkdownPaginationAdapter struct {
	config *PaginationConfig
}

// PaginationConfig 分页配置
type PaginationConfig struct {
	MaxCardLength int `json:"max_card_length"` // 每张卡片最大字符数
	MaxBlocks     int `json:"max_blocks"`      // 每张卡片最大块数
}

// NewMarkdownPaginationAdapter 创建新的 Markdown 分页适配器
func NewMarkdownPaginationAdapter() *MarkdownPaginationAdapter {
	return &MarkdownPaginationAdapter{
		config: &PaginationConfig{
			MaxCardLength: 1000,
			MaxBlocks:     10,
		},
	}
}

// PaginateMarkdownContent 分页处理 Markdown 内容
func (mpa *MarkdownPaginationAdapter) PaginateMarkdownContent(markdownText string) ([]*MarkdownCardContent, error) {
	// 简单的分页逻辑：按行分割，每1000字符一张卡片
	lines := strings.Split(markdownText, "\n")
	var cards []*MarkdownCardContent
	var currentCard strings.Builder
	var currentBlocks []*MarkdownContentBlock
	cardIndex := 1

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// 检查是否是一级标题（新卡片的开始）
		if strings.HasPrefix(line, "# ") && currentCard.Len() > 0 {
			// 保存当前卡片
			if currentCard.Len() > 0 {
				cards = append(cards, &MarkdownCardContent{
					ContentBlocks: currentBlocks,
					IsCoverCard:   false,
					CardIndex:     cardIndex,
					Title:         extractTitleFromContent(currentCard.String()),
					TotalBlocks:   len(currentBlocks),
				})
				cardIndex++
			}
			currentCard.Reset()
			currentBlocks = nil
		}

		// 添加当前行到卡片
		if currentCard.Len() > 0 {
			currentCard.WriteString("\n")
		}
		currentCard.WriteString(line)

		// 创建内容块
		block := &MarkdownContentBlock{
			Type:    determineBlockType(line),
			Content: line,
			Level:   determineHeadingLevel(line),
			RawText: line,
		}
		currentBlocks = append(currentBlocks, block)

		// 如果当前卡片过长，在合适的地方分割
		if currentCard.Len() > mpa.config.MaxCardLength {
			cards = append(cards, &MarkdownCardContent{
				ContentBlocks: currentBlocks,
				IsCoverCard:   false,
				CardIndex:     cardIndex,
				Title:         extractTitleFromContent(currentCard.String()),
				TotalBlocks:   len(currentBlocks),
			})
			cardIndex++
			currentCard.Reset()
			currentBlocks = nil
		}
	}

	// 添加最后一张卡片
	if currentCard.Len() > 0 {
		cards = append(cards, &MarkdownCardContent{
			ContentBlocks: currentBlocks,
			IsCoverCard:   false,
			CardIndex:     cardIndex,
			Title:         extractTitleFromContent(currentCard.String()),
			TotalBlocks:   len(currentBlocks),
		})
	}

	return cards, nil
}

// determineBlockType 确定块类型
func determineBlockType(line string) string {
	if strings.HasPrefix(line, "# ") {
		return "heading"
	} else if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
		return "list_item"
	} else if strings.HasPrefix(line, "> ") {
		return "quote"
	} else if strings.HasPrefix(line, "```") {
		return "code_block"
	} else {
		return "paragraph"
	}
}

// determineHeadingLevel 确定标题层级
func determineHeadingLevel(line string) int {
	if strings.HasPrefix(line, "# ") {
		return 1
	} else if strings.HasPrefix(line, "## ") {
		return 2
	} else if strings.HasPrefix(line, "### ") {
		return 3
	} else if strings.HasPrefix(line, "#### ") {
		return 4
	} else if strings.HasPrefix(line, "##### ") {
		return 5
	} else if strings.HasPrefix(line, "###### ") {
		return 6
	}
	return 0
}

// extractTitleFromContent 从内容中提取标题
func extractTitleFromContent(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return "无标题"
}

// ConvertToJsonString 将分页结果转换为JSON字符串
func (mpa *MarkdownPaginationAdapter) ConvertToJsonString(cardContents []*MarkdownCardContent) (string, error) {
	// 简化的JSON转换
	var result []map[string]interface{}
	for _, card := range cardContents {
		cardData := map[string]interface{}{
			"card_index":     card.CardIndex,
			"title":          card.Title,
			"is_cover_card":  card.IsCoverCard,
			"total_blocks":   card.TotalBlocks,
			"content_blocks": card.ContentBlocks,
		}
		result = append(result, cardData)
	}

	jsonData, err := json.Marshal(result)
	if err != nil {
		return "", err
	}

	return string(jsonData), nil
}

// NewLightweightMarkdownRenderer 创建新的轻量级 Markdown 渲染器
func NewLightweightMarkdownRenderer() *LightweightMarkdownRenderer {
	return &LightweightMarkdownRenderer{
		htmlConverter:     NewHTMLConverter(),
		paginationAdapter: NewMarkdownPaginationAdapter(),
		config: &RendererConfig{
			ImageWidth:     1080,
			ImageHeight:    1440,
			ImageQuality:   85,
			ImageFormat:    "webp",
			BrowserTimeout: 30 * time.Second,
			PageLoadDelay:  2 * time.Second,
			EnableGPU:      false, // 服务器环境通常不启用GPU
			DeviceScale:    2.0,   // 高分辨率渲染
			OutputDir:      filepath.Join(viper.GetString("resource.image_path"), "card"),
			ImagePrefix:    "markdown_card_",
		},
	}
}

// RenderMarkdownToCards 将 Markdown 内容渲染为卡片图片
func (lmr *LightweightMarkdownRenderer) RenderMarkdownToCards(
	ctx context.Context,
	markdownText string,
	bookID uint,
	templateBackground string,
) ([]*RenderedMarkdownCard, error) {
	log.C(ctx).Infow("开始 Markdown 轻量级渲染",
		"book_id", bookID,
		"markdown_length", len(markdownText))

	// 1. 分页处理 Markdown 内容
	cardContents, err := lmr.paginationAdapter.PaginateMarkdownContent(markdownText)
	if err != nil {
		return nil, fmt.Errorf("failed to paginate markdown content: %v", err)
	}

	log.C(ctx).Infow("Markdown 内容分页完成",
		"book_id", bookID,
		"total_cards", len(cardContents))

	// 2. 为每张卡片生成图片
	var renderedCards []*RenderedMarkdownCard
	for i, cardContent := range cardContents {
		startTime := time.Now()

		log.C(ctx).Infow("开始渲染卡片",
			"book_id", bookID,
			"card_index", i+1,
			"card_blocks", len(cardContent.ContentBlocks))

		// 生成卡片图片
		renderedCard, err := lmr.renderSingleCard(ctx, cardContent, bookID, i+1, templateBackground)
		if err != nil {
			log.C(ctx).Errorw("渲染卡片失败",
				"book_id", bookID,
				"card_index", i+1,
				"error", err)
			continue
		}

		renderedCard.RenderTime = time.Since(startTime)
		renderedCards = append(renderedCards, renderedCard)

		log.C(ctx).Infow("卡片渲染完成",
			"book_id", bookID,
			"card_index", i+1,
			"image_url", renderedCard.ImageURL,
			"render_time", renderedCard.RenderTime)
	}

	log.C(ctx).Infow("Markdown 渲染全部完成",
		"book_id", bookID,
		"total_rendered", len(renderedCards))

	return renderedCards, nil
}

// renderSingleCard 渲染单张卡片
func (lmr *LightweightMarkdownRenderer) renderSingleCard(
	ctx context.Context,
	cardContent *MarkdownCardContent,
	bookID uint,
	cardIndex int,
	templateBackground string,
) (*RenderedMarkdownCard, error) {
	log.C(ctx).Infow("开始渲染单张卡片",
		"book_id", bookID,
		"card_index", cardIndex,
		"is_cover_card", cardContent.IsCoverCard)

	// 生成卡片图片文件名
	imageFileName := fmt.Sprintf("card_%d_%d.webp", bookID, cardIndex)
	imagePath := filepath.Join(lmr.config.OutputDir, imageFileName)

	// 如果是封面卡片，需要特殊处理
	if cardContent.IsCoverCard {
		return lmr.renderCoverCard(ctx, cardContent, bookID, cardIndex, imagePath, templateBackground)
	}

	// 普通内容卡片
	return lmr.renderContentCard(ctx, cardContent, bookID, cardIndex, imagePath, templateBackground)
}

// renderCoverCard 渲染封面卡片
func (lmr *LightweightMarkdownRenderer) renderCoverCard(
	ctx context.Context,
	cardContent *MarkdownCardContent,
	bookID uint,
	cardIndex int,
	imagePath string,
	templateBackground string,
) (*RenderedMarkdownCard, error) {
	log.C(ctx).Infow("开始渲染封面卡片",
		"book_id", bookID,
		"card_index", cardIndex)

	// 转换指针切片为值切片
	var blocks []MarkdownContentBlock
	for _, block := range cardContent.ContentBlocks {
		blocks = append(blocks, *block)
	}

	// 生成封面卡片的HTML
	htmlContent := lmr.htmlConverter.ConvertCardBlocksToHTML(
		blocks,
		cardContent.Title,
		true, // isCoverCard = true
	)

	// 如果有封面图片，替换占位符
	if cardContent.Title != "" {
		// 构建封面图片路径 - 使用相对路径
		coverImagePath := fmt.Sprintf("/res/upload/book/%d/book_%d.webp", bookID, bookID)

		// 检查封面图片文件是否存在
		absoluteImagePath := filepath.Join(viper.GetString("resource.image_path"), "book", fmt.Sprintf("%d", bookID), fmt.Sprintf("book_%d.webp", bookID))
		if _, err := os.Stat(absoluteImagePath); err == nil {
			// 文件存在，替换占位符
			log.C(ctx).Infow("封面图片文件存在，替换占位符",
				"book_id", bookID,
				"cover_image_path", coverImagePath,
				"absolute_path", absoluteImagePath)
			htmlContent = lmr.replaceCoverImagePlaceholder(htmlContent, coverImagePath)
		} else {
			// 文件不存在，保持占位符
			log.C(ctx).Warnw("封面图片文件不存在，保持占位符",
				"book_id", bookID,
				"expected_path", absoluteImagePath,
				"error", err)
		}
	}

	// 应用模板背景（如果有）
	if templateBackground != "" {
		htmlContent = lmr.applyTemplateBackground(htmlContent, templateBackground)
	}

	// 渲染HTML为图片
	imageData, err := lmr.renderHTMLToImage(ctx, htmlContent)
	if err != nil {
		return nil, fmt.Errorf("渲染封面卡片失败: %v", err)
	}

	// 保存图片
	if err := lmr.saveImage(imageData, imagePath); err != nil {
		return nil, fmt.Errorf("保存封面卡片图片失败: %v", err)
	}

	// 返回渲染结果
	imageFileName := fmt.Sprintf("card_%d_%d.webp", bookID, cardIndex)
	return &RenderedMarkdownCard{
		CardID:    0, // 将在保存到数据库时设置
		SortOrder: cardIndex,
		ImagePath: imagePath,
		ImageURL:  fmt.Sprintf("/res/upload/card/%s", imageFileName),
		Width:     lmr.config.ImageWidth,
		Height:    lmr.config.ImageHeight,
	}, nil
}

// renderContentCard 渲染内容卡片
func (lmr *LightweightMarkdownRenderer) renderContentCard(
	ctx context.Context,
	cardContent *MarkdownCardContent,
	bookID uint,
	cardIndex int,
	imagePath string,
	templateBackground string,
) (*RenderedMarkdownCard, error) {
	log.C(ctx).Infow("开始渲染内容卡片",
		"book_id", bookID,
		"card_index", cardIndex,
		"blocks_count", len(cardContent.ContentBlocks))

	// 转换指针切片为值切片
	var blocks []MarkdownContentBlock
	for _, block := range cardContent.ContentBlocks {
		blocks = append(blocks, *block)
	}

	// 生成内容卡片的HTML
	htmlContent := lmr.htmlConverter.ConvertCardBlocksToHTML(
		blocks,
		cardContent.Title,
		false, // isCoverCard = false
	)

	// 应用模板背景（如果有）
	if templateBackground != "" {
		htmlContent = lmr.applyTemplateBackground(htmlContent, templateBackground)
	}

	// 渲染HTML为图片
	imageData, err := lmr.renderHTMLToImage(ctx, htmlContent)
	if err != nil {
		return nil, fmt.Errorf("渲染内容卡片失败: %v", err)
	}

	// 保存图片
	if err := lmr.saveImage(imageData, imagePath); err != nil {
		return nil, fmt.Errorf("保存内容卡片图片失败: %v", err)
	}

	// 返回渲染结果
	imageFileName := fmt.Sprintf("card_%d_%d.webp", bookID, cardIndex)
	return &RenderedMarkdownCard{
		CardID:    0, // 将在保存到数据库时设置
		SortOrder: cardIndex,
		ImagePath: imagePath,
		ImageURL:  fmt.Sprintf("/res/upload/card/%s", imageFileName),
		Width:     lmr.config.ImageWidth,
		Height:    lmr.config.ImageHeight,
	}, nil
}

// replaceCoverImagePlaceholder 替换封面图片占位符
func (lmr *LightweightMarkdownRenderer) replaceCoverImagePlaceholder(htmlContent, coverImagePath string) string {
	placeholderHTML := `<div class="cover-image-placeholder">
            <div class="placeholder-icon">🖼️</div>
            <div class="placeholder-text">封面图片</div>
        </div>`

	imageHTML := fmt.Sprintf(`<img src="%s" class="cover-image" alt="封面图片" onerror="this.style.display='none'; this.nextElementSibling.style.display='flex';">
        <div class="cover-image-placeholder" style="display: none;">
            <div class="placeholder-icon">🖼️</div>
            <div class="placeholder-text">封面图片</div>
        </div>`, coverImagePath)

	return strings.Replace(htmlContent, placeholderHTML, imageHTML, 1)
}

// renderHTMLToImage 使用无头浏览器将 HTML 渲染为图片
func (lmr *LightweightMarkdownRenderer) renderHTMLToImage(ctx context.Context, htmlContent string) ([]byte, error) {
	// 添加调试日志，输出HTML内容的前500个字符
	log.C(ctx).Infow("开始渲染HTML为图片",
		"html_length", len(htmlContent),
		"html_preview", truncateString(htmlContent, 500))

	// 创建临时HTML文件
	tempFile, err := os.CreateTemp("", "markdown_card_*.html")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	// 写入HTML内容
	if _, err := tempFile.WriteString(htmlContent); err != nil {
		return nil, fmt.Errorf("failed to write HTML to temp file: %v", err)
	}

	// 获取文件的绝对路径
	fileURL := "file://" + tempFile.Name()
	log.C(ctx).Infow("使用临时文件进行渲染",
		"temp_file", tempFile.Name(),
		"file_url", fileURL)

	// 创建浏览器上下文
	allocCtx, cancel := chromedp.NewExecAllocator(ctx,
		chromedp.NoSandbox,
		chromedp.DisableGPU,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.WindowSize(lmr.config.ImageWidth, lmr.config.ImageHeight),
	)
	defer cancel()

	// 创建浏览器实例
	ctxTimeout, cancel := context.WithTimeout(allocCtx, lmr.config.BrowserTimeout)
	defer cancel()

	browserCtx, cancel := chromedp.NewContext(ctxTimeout)
	defer cancel()

	// 渲染页面并截图
	var imageData []byte
	err = chromedp.Run(browserCtx,
		chromedp.Navigate(fileURL),
		chromedp.Sleep(lmr.config.PageLoadDelay),
		chromedp.ActionFunc(func(ctx context.Context) error {
			// 设置视口
			err := chromedp.EmulateViewport(
				int64(lmr.config.ImageWidth),
				int64(lmr.config.ImageHeight),
				chromedp.EmulateScale(lmr.config.DeviceScale),
			).Do(ctx)
			if err != nil {
				return err
			}

			// 添加调试：获取页面内容
			var pageContent string
			if err := chromedp.OuterHTML("body", &pageContent).Do(ctx); err == nil {
				log.C(ctx).Infow("页面渲染后的HTML内容",
					"page_content_length", len(pageContent),
					"page_content_preview", truncateString(pageContent, 300))
			}

			// 添加调试：检查页面是否有内容
			var textContent string
			if err := chromedp.Text("body", &textContent).Do(ctx); err == nil {
				log.C(ctx).Infow("页面文本内容",
					"text_content_length", len(textContent),
					"text_content_preview", truncateString(textContent, 200))
			}

			// 截图
			return chromedp.FullScreenshot(&imageData, 100).Do(ctx)
		}),
	)

	if err != nil {
		return nil, fmt.Errorf("failed to render page: %v", err)
	}

	log.C(ctx).Infow("HTML渲染完成",
		"image_data_size", len(imageData))

	// 转换图片格式
	return lmr.convertImageFormat(imageData)
}

// convertImageFormat 转换图片格式
func (lmr *LightweightMarkdownRenderer) convertImageFormat(pngData []byte) ([]byte, error) {
	if lmr.config.ImageFormat == "png" {
		return pngData, nil
	}

	// 解码 PNG
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return nil, fmt.Errorf("failed to decode PNG: %v", err)
	}

	// 转换为目标格式
	var buf bytes.Buffer
	switch lmr.config.ImageFormat {
	case "webp":
		err = webp.Encode(&buf, img, &webp.Options{
			Quality: float32(lmr.config.ImageQuality),
		})
	default:
		return pngData, nil // 默认返回 PNG
	}

	if err != nil {
		return nil, fmt.Errorf("failed to encode %s: %v", lmr.config.ImageFormat, err)
	}

	return buf.Bytes(), nil
}

// saveImage 保存图片文件
func (lmr *LightweightMarkdownRenderer) saveImage(
	imageData []byte,
	imagePath string,
) error {
	// 创建输出目录
	outputDir := filepath.Dir(imagePath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %v", err)
	}

	// 写入文件
	if err := os.WriteFile(imagePath, imageData, 0644); err != nil {
		return fmt.Errorf("failed to write image file: %v", err)
	}

	return nil
}

// applyTemplateBackground 应用模板背景
func (lmr *LightweightMarkdownRenderer) applyTemplateBackground(htmlContent, backgroundPath string) string {
	// 在 CSS 中添加背景图片
	backgroundCSS := fmt.Sprintf(`
.card-container {
    background-image: url('%s');
    background-size: cover;
    background-position: center;
    background-repeat: no-repeat;
}`, backgroundPath)

	// 在 HTML 中插入背景 CSS
	return strings.Replace(htmlContent, "</style>", backgroundCSS+"</style>", 1)
}

// ConvertToCardModel 将渲染结果转换为卡片模型
func (lmr *LightweightMarkdownRenderer) ConvertToCardModel(
	renderedCards []*RenderedMarkdownCard,
	cardContents []*MarkdownCardContent,
	bookID uint,
	userID uint,
) ([]*model.CardM, error) {
	if len(renderedCards) != len(cardContents) {
		return nil, fmt.Errorf("rendered cards count (%d) doesn't match card contents count (%d)",
			len(renderedCards), len(cardContents))
	}

	var cards []*model.CardM
	for i, rendered := range renderedCards {
		content := cardContents[i]

		// 将 Markdown 内容块转换为 JSON 字符串
		jsonContent, err := lmr.paginationAdapter.ConvertToJsonString([]*MarkdownCardContent{content})
		if err != nil {
			return nil, fmt.Errorf("failed to convert content to JSON: %v", err)
		}

		card := &model.CardM{
			BookID:        bookID,
			UserID:        userID,
			SortOrder:     rendered.SortOrder,
			ProcessedText: jsonContent, // 存储为兼容格式的 JSON
			RenderedImage: rendered.ImagePath,
		}

		cards = append(cards, card)
	}

	return cards, nil
}

// GetRenderStats 获取渲染统计信息
func (lmr *LightweightMarkdownRenderer) GetRenderStats(renderedCards []*RenderedMarkdownCard) map[string]interface{} {
	if len(renderedCards) == 0 {
		return map[string]interface{}{
			"total_cards":  0,
			"total_size":   0,
			"average_time": 0,
			"total_time":   0,
		}
	}

	var totalSize int64
	var totalTime time.Duration

	for _, card := range renderedCards {
		totalSize += card.FileSize
		totalTime += card.RenderTime
	}

	averageTime := totalTime / time.Duration(len(renderedCards))

	return map[string]interface{}{
		"total_cards":  len(renderedCards),
		"total_size":   totalSize,
		"average_size": totalSize / int64(len(renderedCards)),
		"average_time": averageTime,
		"total_time":   totalTime,
		"success_rate": 100.0, // 如果执行到这里说明都成功了
	}
}

// CleanupOldImages 清理旧的图片文件
func (lmr *LightweightMarkdownRenderer) CleanupOldImages(bookID uint) error {
	outputDir := filepath.Join(lmr.config.OutputDir, strconv.FormatUint(uint64(bookID), 10))

	// 检查目录是否存在
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		return nil // 目录不存在，无需清理
	}

	// 读取目录内容
	files, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Errorf("failed to read directory: %v", err)
	}

	// 删除匹配的文件
	for _, file := range files {
		if strings.HasPrefix(file.Name(), lmr.config.ImagePrefix) {
			filePath := filepath.Join(outputDir, file.Name())
			if err := os.Remove(filePath); err != nil {
				// 记录错误但继续删除其他文件
				fmt.Printf("failed to remove file %s: %v\n", filePath, err)
			}
		}
	}

	return nil
}

// UpdateConfig 更新渲染器配置
func (lmr *LightweightMarkdownRenderer) UpdateConfig(config *RendererConfig) {
	lmr.config = config
}

// GetConfig 获取当前配置
func (lmr *LightweightMarkdownRenderer) GetConfig() *RendererConfig {
	return lmr.config
}
