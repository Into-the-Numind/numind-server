package book

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	cardbiz "numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/config"
	"numind-server/internal/numind/biz/markdown"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/httpclient"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
	utilpkg "numind-server/pkg/util"

	"github.com/spf13/viper"
)

// 全局渲染并发控制
var (
	// renderSemaphore 控制同时渲染的最大数量，避免资源耗尽
	renderSemaphore chan struct{}
	renderOnce      sync.Once
)

// initRenderSemaphore 初始化渲染并发控制信号量
func initRenderSemaphore() {
	renderOnce.Do(func() {
		// 根据CPU核心数和系统配置动态设置并发数
		// 每个Chrome实例消耗大量资源，建议并发数为 CPU核心数 / 2，最小2，最大10
		maxConcurrent := runtime.NumCPU() / 2
		if maxConcurrent < 2 {
			maxConcurrent = 2
		}
		if maxConcurrent > 10 {
			maxConcurrent = 10
		}

		// 从配置文件读取（如果存在）
		if viper.IsSet("card.rendering.max_concurrent") {
			configConcurrent := viper.GetInt("card.rendering.max_concurrent")
			if configConcurrent > 0 && configConcurrent <= 20 {
				maxConcurrent = configConcurrent
			}
		}

		renderSemaphore = make(chan struct{}, maxConcurrent)
		log.Infow("初始化渲染并发控制", "max_concurrent", maxConcurrent, "cpu_cores", runtime.NumCPU())
	})
}

// acquireRenderSlot 获取渲染槽位（阻塞直到有可用槽位）
func acquireRenderSlot(ctx context.Context) error {
	initRenderSemaphore()
	select {
	case renderSemaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// releaseRenderSlot 释放渲染槽位
func releaseRenderSlot() {
	select {
	case <-renderSemaphore:
	default:
	}
}

// AsyncBookProcessor 异步book处理器
type AsyncBookProcessor struct {
	biz          BizInterface
	configReader *config.ConfigReader
}

// SetConfigReader 设置配置读取器（可选，如果不设置则使用viper）
func (p *AsyncBookProcessor) SetConfigReader(reader *config.ConfigReader) {
	p.configReader = reader
}

// getCoverTitleConfig 获取封面标题配置
func getCoverTitleConfig() (fontSize int, lineHeight int, color string) {
	// 默认值
	fontSize = 48
	lineHeight = 62
	color = "#2c3e50"

	// 从配置中读取
	if viper.IsSet("special_rules.cover_card.title_section.font_size") {
		fontSize = viper.GetInt("special_rules.cover_card.title_section.font_size")
	}
	if viper.IsSet("special_rules.cover_card.title_section.line_height") {
		lineHeight = viper.GetInt("special_rules.cover_card.title_section.line_height")
	}
	if viper.IsSet("special_rules.cover_card.title_section.color") {
		color = viper.GetString("special_rules.cover_card.title_section.color")
	}

	return fontSize, lineHeight, color
}

// getFontPaths 获取字体文件路径（根据运行环境动态调整）
func getFontPaths() (regularPath, boldPath string) {
	// 检测是否在Docker容器中运行
	if isRunningInDocker() {
		// Docker容器环境：使用容器内的字体路径
		regularPath = "file:///usr/share/fonts/truetype/SourceHanSerifSC-Regular.otf"
		boldPath = "file:///usr/share/fonts/truetype/SourceHanSerifSC-Bold.otf"
	} else {
		// 本地开发环境：根据操作系统检测
		if runtime.GOOS == "darwin" {
			// macOS
			regularPath = "file:///Users/" + os.Getenv("USER") + "/Library/Fonts/SourceHanSerifSC-Regular.otf"
			boldPath = "file:///Users/" + os.Getenv("USER") + "/Library/Fonts/SourceHanSerifSC-Bold.otf"
		} else if runtime.GOOS == "linux" {
			// Linux本地环境
			if homeDir, err := os.UserHomeDir(); err == nil {
				regularPath = "file://" + homeDir + "/.local/share/fonts/SourceHanSerifSC-Regular.otf"
				boldPath = "file://" + homeDir + "/.local/share/fonts/SourceHanSerifSC-Bold.otf"
			} else {
				// 回退到容器路径
				regularPath = "file:///usr/share/fonts/truetype/SourceHanSerifSC-Regular.otf"
				boldPath = "file:///usr/share/fonts/truetype/SourceHanSerifSC-Bold.otf"
			}
		} else {
			// Windows或其他系统，使用回退方案
			regularPath = "file:///usr/share/fonts/truetype/SourceHanSerifSC-Regular.otf"
			boldPath = "file:///usr/share/fonts/truetype/SourceHanSerifSC-Bold.otf"
		}
	}

	return regularPath, boldPath
}

// isRunningInDocker 检测是否在Docker容器中运行
func isRunningInDocker() bool {
	// 检查 /.dockerenv 文件（Docker容器的标准标识）
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}

	// 检查容器化环境变量
	if os.Getenv("DOCKER_CONTAINER") == "true" || os.Getenv("container") != "" {
		return true
	}

	// 检查cgroup信息
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		content := string(data)
		if strings.Contains(content, "docker") || strings.Contains(content, "containerd") {
			return true
		}
	}

	return false
}

// BizInterface 业务接口
type BizInterface interface {
	Books() AsyncBookBiz
	Cards() AsyncCardBiz
	Users() AsyncUserBiz
	Images() AsyncImageBiz
	Pagination() AsyncPaginationBiz
	Ali() AsyncAliBiz
	Volc() AsyncVolcBiz // 新增volc支持
	Templates() AsyncTemplateBiz
	Store() AsyncStoreBiz // 新增store层访问
}

// AsyncBookBiz 书籍业务接口
type AsyncBookBiz interface {
	Create(ctx context.Context, book *model.BookM) error
	Update(ctx context.Context, book *model.BookM) error
	GetByID(ctx context.Context, id uint) (*model.BookM, error)
	UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error
}

// AsyncCardBiz 卡片业务接口
type AsyncCardBiz interface {
	Create(ctx context.Context, card *model.CardM) error
	Update(ctx context.Context, card *model.CardM) error
	GetByID(ctx context.Context, id uint) (*model.CardM, error)
}

// AsyncUserBiz 用户业务接口
type AsyncUserBiz interface {
	IncrementUserBookNum(ctx context.Context, userID uint) error
	IncrementUserCardNum(ctx context.Context, userID uint) error
	IncrementMonthlyBookCount(ctx context.Context, userID uint) error
	IncrementFreeUserMonthlyBookCount(ctx context.Context, userID uint) error
}

// AsyncImageBiz 图片业务接口
type AsyncImageBiz interface {
	Create(ctx context.Context, image *model.ImageM) error
	GetByID(ctx context.Context, id uint) (*model.ImageM, error)
	ListByBook(ctx context.Context, bookID uint, offset, limit int) (int64, []*model.ImageM, error)
}

// AsyncPaginationBiz 分页业务接口
type AsyncPaginationBiz interface {
	PaginateText(ctx context.Context, text string) ([]interface{}, error)
}

// AsyncAliBiz 阿里业务接口
type AsyncAliBiz interface {
	QianwenTextStream(messages []map[string]string, maxTokens int, temperature float64) (string, error)
	WanxiangImageAsync(prompt, style, size string) (string, error)
	StableDiffusionImageAsync(prompt, size string) (string, error)
	GetPromptManager() AsyncPromptManager
}

// AsyncVolcBiz 火山引擎业务接口
type AsyncVolcBiz interface {
	VolcTextStream(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64) (string, error)
}

// AsyncTemplateBiz 模板业务接口
type AsyncTemplateBiz interface {
	GetByID(ctx context.Context, id uint) (*model.Template, error)
}

// AsyncPromptManager 提示词管理器接口
type AsyncPromptManager interface {
	GetTextProcessingPrompt() string
}

// AsyncStoreBiz store层业务接口
type AsyncStoreBiz interface {
	UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error
}

// NewAsyncBookProcessor 创建异步book处理器
func NewAsyncBookProcessor(biz BizInterface) *AsyncBookProcessor {
	return &AsyncBookProcessor{
		biz: biz,
	}
}

// validateAndSetBookType 验证并设置笔记类型
func validateAndSetBookType(book *model.BookM, hasImages bool) {
	validTypes := []string{
		model.BookTypeText,
		model.BookTypeTextWithImage,
		model.BookTypeTodo,
		model.BookTypeDone,
	}

	// 如果前端没有传类型，根据是否有图片自动判断
	if book.BookType == "" {
		if hasImages {
			book.BookType = model.BookTypeTextWithImage
		} else {
			book.BookType = model.BookTypeText
		}
		return
	}

	// 验证类型合法性
	isValid := false
	for _, validType := range validTypes {
		if book.BookType == validType {
			isValid = true
			break
		}
	}

	// 如果类型不合法，根据是否有图片设置默认类型
	if !isValid {
		if hasImages {
			book.BookType = model.BookTypeTextWithImage
		} else {
			book.BookType = model.BookTypeText
		}
	}

	// 注意：todo 和 done 类型必须由前端明确指定，不会自动判断
}

// CreateBookAsync 异步创建book
func (p *AsyncBookProcessor) CreateBookAsync(ctx context.Context, userID uint, text, templateID string) (*model.BookM, error) {
	// 立即创建book记录，状态为creating
	now := time.Now()
	book := &model.BookM{
		UserID:   userID,
		Title:    fmt.Sprintf("AI生成卡册 - %s", now.Format("2006-01-02 15:04:05")),
		ViewTime: &now,
		Status:   model.BookStatusCreating,
	}

	if err := p.biz.Books().Create(ctx, book); err != nil {
		log.C(ctx).Errorw("Failed to create initial book record", "error", err.Error())
		return nil, err
	}

	// 创建book后立即更新用户统计
	if err := p.biz.Users().IncrementUserBookNum(ctx, userID); err != nil {
		log.C(ctx).Errorw("Failed to increment user book num", "error", err.Error())
		// 统计更新失败不影响主要流程，但记录错误
	}

	// 增加月度卡册计数（仅对订阅会员和both类型）
	if err := p.biz.Users().IncrementMonthlyBookCount(ctx, userID); err != nil {
		log.C(ctx).Errorw("Failed to increment monthly book count", "error", err.Error())
		// 统计更新失败不影响主要流程，但记录错误
	}

	// 增加免费用户月度卡册计数（仅对免费用户）
	if err := p.biz.Users().IncrementFreeUserMonthlyBookCount(ctx, userID); err != nil {
		log.C(ctx).Errorw("Failed to increment free user monthly book count", "error", err.Error())
		// 统计更新失败不影响主要流程，但记录错误
	}

	// 在后台异步处理book创建
	go func() {
		p.processBookCreationInBackground(ctx, book.ID, userID, text, templateID)
	}()

	return book, nil
}

// CreateBookWithImagesAsync 创建带图片的笔记（新版本）
func (p *AsyncBookProcessor) CreateBookWithImagesAsync(ctx context.Context, userID uint, text string, title string, bookType string, files []*multipart.FileHeader, aiPolish int) (*model.BookM, error) {
	// 立即创建book记录，状态为creating
	now := time.Now()
	book := &model.BookM{
		UserID:       userID,
		Title:        title, // 使用传入的title，如果为空则不设置标题
		OriginalText: text,  // 保存用户原始输入文字
		ViewTime:     &now,
		Status:       model.BookStatusCreating,
		AIPolish:     aiPolish, // 保存AI润色设置
		BookType:     bookType, // 笔记类型
	}

	// 验证并设置笔记类型（根据是否有图片自动判断）
	hasImages := len(files) > 0
	validateAndSetBookType(book, hasImages)

	if err := p.biz.Books().Create(ctx, book); err != nil {
		log.C(ctx).Errorw("Failed to create initial book record", "error", err.Error())
		return nil, err
	}

	// 创建book后立即更新用户统计
	if err := p.biz.Users().IncrementUserBookNum(ctx, userID); err != nil {
		log.C(ctx).Errorw("Failed to increment user book num", "error", err.Error())
		// 统计更新失败不影响主要流程，但记录错误
	}

	// 增加月度卡册计数（仅对订阅会员和both类型）
	if err := p.biz.Users().IncrementMonthlyBookCount(ctx, userID); err != nil {
		log.C(ctx).Errorw("Failed to increment monthly book count", "error", err.Error())
		// 统计更新失败不影响主要流程，但记录错误
	}

	// 增加免费用户月度卡册计数（仅对免费用户）
	if err := p.biz.Users().IncrementFreeUserMonthlyBookCount(ctx, userID); err != nil {
		log.C(ctx).Errorw("Failed to increment free user monthly book count", "error", err.Error())
		// 统计更新失败不影响主要流程，但记录错误
	}

	// 在后台异步处理book创建
	go func() {
		p.processBookCreationWithImagesInBackground(ctx, book.ID, userID, text, title, book.BookType, files, aiPolish)
	}()

	return book, nil
}

// processBookCreationWithImagesInBackground 在后台处理带图片的book创建
func (p *AsyncBookProcessor) processBookCreationWithImagesInBackground(ctx context.Context, bookID uint, userID uint, text string, title string, bookType string, files []*multipart.FileHeader, aiPolish int) {
	startTime := time.Now()
	log.C(ctx).Infow("Starting async book creation with images", "book_id", bookID, "user_id", userID, "ai_polish", aiPolish, "title", title)

	// 获取book记录
	book, err := p.biz.Books().GetByID(ctx, bookID)
	if err != nil {
		log.C(ctx).Errorw("Failed to get book for async processing", "book_id", bookID, "error", err.Error())
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Failed to get book record")
		return
	}

	// 🖼️ 第一步：处理上传的图片（如果有的话）
	log.C(ctx).Infow("🖼️ 开始处理上传的图片", "book_id", bookID, "file_count", len(files))

	// 上传图片到COS并创建ImageM记录
	var imageRecords []*model.ImageM
	if len(files) > 0 {
		for i, file := range files {
			imageRecord, err := p.processAndUploadImage(ctx, bookID, userID, file, i+1)
			if err != nil {
				log.C(ctx).Errorw("Failed to process image", "book_id", bookID, "file_index", i, "error", err.Error())
				// 单个图片处理失败不影响整体流程
				continue
			}
			imageRecords = append(imageRecords, imageRecord)
		}
		log.C(ctx).Infow("✅ 图片处理完成", "book_id", bookID, "processed_count", len(imageRecords))
	} else {
		log.C(ctx).Infow("📝 没有上传图片，跳过图片处理", "book_id", bookID)
	}

	// 🤖 第二步：AI处理文本（根据aiPolish参数决定）
	var markdownContent string
	var bookTitle string

	if aiPolish == 1 {
		log.C(ctx).Infow("🤖 AI处理已启用，开始处理文本", "book_id", bookID)

		// 更新状态为AI处理中
		if err := p.updateBookStatus(ctx, bookID, model.BookStatusAI, ""); err != nil {
			log.C(ctx).Errorw("Failed to update book status to AI", "book_id", bookID, "error", err.Error())
		}

		// 调用AI处理文本
		aiResponse, err := p.processTextWithAI(ctx, text)
		if err != nil {
			log.C(ctx).Errorw("AI处理失败", "book_id", bookID, "error", err.Error())
			p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "AI processing failed: "+err.Error())
			return
		}

		log.C(ctx).Infow("✅ AI处理完成", "book_id", bookID, "response_length", len(aiResponse))

		// 解析AI响应
		markdownContent, _ = p.parseMarkdownResponse(aiResponse)
		if markdownContent == "" {
			log.C(ctx).Errorw("❌ 无法解析markdown内容", "book_id", bookID)
			p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Failed to parse markdown content")
			return
		}

		// 如果用户没有提供title，才从markdown文本中提取title作为book的标题（在删除一级标题之前）
		if title == "" {
			bookTitle = p.extractTitleFromMarkdown(markdownContent)
			if bookTitle == "" {
				// 如果提取不到标题，也不自动生成，保持为空
				bookTitle = ""
			} else {
				log.C(ctx).Infow("✅ 从一级标题中提取标题", "book_id", bookID, "title", bookTitle)
			}
		} else {
			// 用户已经提供了title，使用用户提供的title
			bookTitle = title
			log.C(ctx).Infow("✅ 使用用户提供的标题", "book_id", bookID, "title", bookTitle)
		}

		// 删除一级标题（仅从processed_text中删除，不影响title字段）
		markdownContent = p.removeFirstLevelHeading(markdownContent)
		log.C(ctx).Infow("✅ 已删除一级标题", "book_id", bookID)
	} else {
		log.C(ctx).Infow("📝 AI处理已禁用，processed_text将保持为空", "book_id", bookID)
		markdownContent = "" // 如果未启用AI优化，processed_text应该为空
		// 如果用户没有提供title，也不自动生成，保持为空
		if title == "" {
			bookTitle = ""
		} else {
			bookTitle = title
			log.C(ctx).Infow("✅ 使用用户提供的标题", "book_id", bookID, "title", bookTitle)
		}
	}

	// 📝 第三步：更新book记录
	// 设置封面图片为最先上传的图片（ID最小的图片）
	if len(imageRecords) > 0 {
		firstImage := imageRecords[0]
		book.ImageUrl = firstImage.OriginalURL
		log.C(ctx).Infow("✅ 设置卡册封面为最先上传的图片",
			"book_id", bookID,
			"image_id", firstImage.ID,
			"image_url", firstImage.OriginalURL)
	}

	// 只有在bookTitle不为空时才更新Title，如果为空则保持为空（不设置默认标题）
	if bookTitle != "" {
		book.Title = bookTitle
	}
	book.ProcessedText = markdownContent
	book.Status = model.BookStatusSuccess

	if err := p.biz.Books().Update(ctx, book); err != nil {
		log.C(ctx).Errorw("Failed to update book with processed content", "book_id", bookID, "error", err.Error())
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Failed to update book")
		return
	}

	// 更新用户统计
	if err := p.biz.Users().IncrementUserBookNum(ctx, userID); err != nil {
		log.C(ctx).Errorw("Failed to update user book stats", "book_id", bookID, "error", err.Error())
		// 统计更新失败不影响主要流程
	}

	duration := time.Since(startTime)
	log.C(ctx).Infow("✅ 笔记创建完成", "book_id", bookID, "duration", duration.String(), "image_count", len(imageRecords))
}

// processAndUploadImage 处理并上传单个图片
func (p *AsyncBookProcessor) processAndUploadImage(ctx context.Context, bookID, userID uint, file *multipart.FileHeader, sortOrder int) (*model.ImageM, error) {
	log.C(ctx).Infow("开始处理图片", "book_id", bookID, "filename", file.Filename, "size", file.Size)

	// 打开文件
	src, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer src.Close()

	// 读取文件数据
	fileData, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// 生成文件名
	ext := filepath.Ext(file.Filename)
	if ext == "" {
		ext = ".jpg" // 默认扩展名
	}
	fileName := fmt.Sprintf("image_%d_%d%s", bookID, sortOrder, ext)

	// 上传到COS
	objectKey := fmt.Sprintf("image/%d/%s", bookID, fileName)
	cosURL, err := util.UploadBytesToCOS(ctx, objectKey, file.Header.Get("Content-Type"), fileData)
	if err != nil {
		return nil, fmt.Errorf("failed to upload to COS: %w", err)
	}

	log.C(ctx).Infow("✅ 图片上传到COS成功", "book_id", bookID, "cos_url", cosURL)

	// 创建ImageM记录
	imageRecord := &model.ImageM{
		UserID:      userID,
		BookID:      &bookID,
		OriginalURL: cosURL,
		FileName:    fileName,
		FileSize:    file.Size,
		ImageType:   file.Header.Get("Content-Type"),
		Status:      "uploaded",
	}

	if err := p.biz.Images().Create(ctx, imageRecord); err != nil {
		log.C(ctx).Errorw("Failed to create image record", "book_id", bookID, "error", err.Error())
		return nil, fmt.Errorf("failed to create image record: %w", err)
	}

	log.C(ctx).Infow("✅ 图片记录创建成功", "book_id", bookID, "image_id", imageRecord.ID)
	return imageRecord, nil
}

// processTextWithAI 使用AI处理文本
func (p *AsyncBookProcessor) processTextWithAI(ctx context.Context, text string) (string, error) {
	log.C(ctx).Infow("开始AI处理文本", "text_length", len(text))

	// 获取配置文件中的提示词（优先从Redis/数据库读取）
	var textProcessingPrompt string
	if p.configReader != nil {
		textProcessingPrompt = p.configReader.GetTextProcessingPrompt(ctx)
	} else {
		textProcessingPrompt = viper.GetString("ai_prompts.text_processing")
	}
	if textProcessingPrompt == "" {
		return "", fmt.Errorf("missing text_processing prompt in config")
	}

	// 构建消息 - 将用户文本拼接到提示词的 # 待处理的OCR文本 后面
	fullPrompt := textProcessingPrompt + "\n\n" + text
	messages := []map[string]string{
		{"role": "user", "content": fullPrompt},
	}

	// 打印发送给大模型的完整提示词
	log.C(ctx).Infow("发送给大模型的完整提示词",
		"prompt_length", len(fullPrompt),
		"user_text_length", len(text))

	// 调用AI模型处理文本 - 先尝试火山方舟，失败后降级到阿里百炼
	aiResponse, err := p.biz.Volc().VolcTextStream(ctx, messages, 4000, 0.7)
	if err != nil {
		log.C(ctx).Warnw("火山方舟API失败，尝试阿里百炼降级", "error", err.Error())

		// 降级到阿里百炼API
		aiResponse, err = p.biz.Ali().QianwenTextStream(messages, 4000, 0.7)
		if err != nil {
			log.C(ctx).Errorw("所有AI API都失败", "error", err.Error())
			return "", fmt.Errorf("all AI APIs failed: %v", err)
		}
		log.C(ctx).Infow("阿里百炼API降级成功")
	} else {
		log.C(ctx).Infow("火山方舟API调用成功")
	}

	// 验证AI响应
	if aiResponse == "" {
		return "", fmt.Errorf("AI returned empty response")
	}

	log.C(ctx).Infow("AI处理完成", "response_length", len(aiResponse))
	return aiResponse, nil
}

// GenerateLongImageAsync 异步生成长图
func (p *AsyncBookProcessor) GenerateLongImageAsync(ctx context.Context, bookID uint, processedText, templateID string) (*model.CardM, error) {
	log.C(ctx).Infow("开始生成长图", "book_id", bookID, "template_id", templateID)

	// 获取book记录
	book, err := p.biz.Books().GetByID(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("failed to get book: %w", err)
	}

	// 创建CardM记录
	card := &model.CardM{
		UserID:        book.UserID,
		BookID:        bookID,
		ProcessedText: processedText,
		CardType:      "long", // 长图类型
		TemplateID:    templateID,
		SortOrder:     1,
		Tags:          book.Tags,
	}

	if err := p.biz.Cards().Create(ctx, card); err != nil {
		log.C(ctx).Errorw("Failed to create card record", "book_id", bookID, "error", err.Error())
		return nil, fmt.Errorf("failed to create card record: %w", err)
	}

	// 在后台异步生成长图
	go func() {
		p.generateLongImageInBackground(ctx, card.ID, bookID, processedText, templateID)
	}()

	return card, nil
}

// generateLongImageInBackground 在后台生成长图
func (p *AsyncBookProcessor) generateLongImageInBackground(ctx context.Context, cardID, bookID uint, processedText, templateID string) {
	startTime := time.Now()
	log.C(ctx).Infow("开始后台生成长图", "card_id", cardID, "book_id", bookID)

	// 获取模板背景信息
	var templateBackground string
	if templateID != "" {
		if tid, err := strconv.ParseUint(templateID, 10, 64); err == nil {
			template, err := p.biz.Templates().GetByID(ctx, uint(tid))
			if err != nil {
				log.C(ctx).Warnw("Failed to get template, using default white background", "template_id", templateID, "error", err.Error())
				templateBackground = "" // 使用默认白色背景
			} else if template.File != "" {
				templateBackground = template.File
				log.C(ctx).Infow("Template background loaded", "template_id", templateID, "background", templateBackground)
			} else {
				log.C(ctx).Warnw("Template has no file, using default white background", "template_id", templateID)
				templateBackground = "" // 使用默认白色背景
			}
		} else {
			log.C(ctx).Warnw("Invalid template ID format, using default white background", "template_id", templateID, "error", err.Error())
			templateBackground = "" // 使用默认白色背景
		}
	} else {
		log.C(ctx).Infow("No template ID provided, using default white background")
		templateBackground = "" // 使用默认白色背景
	}

	// 生成长图
	imageURL, err := p.generateLongImage(ctx, processedText, templateBackground)
	if err != nil {
		log.C(ctx).Errorw("Failed to generate long image", "card_id", cardID, "error", err.Error())
		return
	}

	// 更新CardM记录
	card, err := p.biz.Cards().GetByID(ctx, cardID)
	if err != nil {
		log.C(ctx).Errorw("Failed to get card for update", "card_id", cardID, "error", err.Error())
		return
	}

	card.RenderedImage = imageURL
	if err := p.biz.Cards().Update(ctx, card); err != nil {
		log.C(ctx).Errorw("Failed to update card with rendered image", "card_id", cardID, "error", err.Error())
		return
	}

	duration := time.Since(startTime)
	log.C(ctx).Infow("✅ 长图生成完成", "card_id", cardID, "book_id", bookID, "duration", duration.String(), "image_url", imageURL)
}

// generateLongImage 生成长图的具体实现
func (p *AsyncBookProcessor) generateLongImage(ctx context.Context, processedText, templateBackground string) (string, error) {
	log.C(ctx).Infow("开始生成长图", "text_length", len(processedText))

	// 这里实现长图生成逻辑
	// 1. 将markdown转换为HTML
	// 2. 应用模板背景
	// 3. 使用wkhtmltoimage生成长图
	// 4. 上传到COS
	// 5. 返回COS链接

	// 为了简化，这里返回一个占位符
	// 实际实现需要调用markdown转换和图片生成逻辑
	imageURL := fmt.Sprintf("/images/long_card_%d.jpg", time.Now().Unix())

	log.C(ctx).Infow("✅ 长图生成完成", "image_url", imageURL)
	return imageURL, nil
}

// GeneratePaginatedImagesAsync 异步生成分页图片
func (p *AsyncBookProcessor) GeneratePaginatedImagesAsync(ctx context.Context, bookID uint, processedText, templateID string) ([]*model.CardM, error) {
	log.C(ctx).Infow("开始生成分页图片", "book_id", bookID, "template_id", templateID)

	// 获取book记录
	book, err := p.biz.Books().GetByID(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("failed to get book: %w", err)
	}

	// 使用现有的分页逻辑
	paginatedData, err := p.biz.Pagination().PaginateText(ctx, processedText)
	if err != nil {
		return nil, fmt.Errorf("failed to paginate text: %w", err)
	}

	var cards []*model.CardM
	for i, pageData := range paginatedData {
		// 将分页数据转换为JSON字符串
		pageJSON, err := json.Marshal(pageData)
		if err != nil {
			log.C(ctx).Errorw("Failed to marshal page data", "book_id", bookID, "page_index", i, "error", err.Error())
			continue
		}

		// 创建CardM记录
		card := &model.CardM{
			UserID:        book.UserID,
			BookID:        bookID,
			ProcessedText: string(pageJSON),
			CardType:      "paginated", // 分页类型
			TemplateID:    templateID,
			SortOrder:     i + 1,
			Tags:          book.Tags,
		}

		if err := p.biz.Cards().Create(ctx, card); err != nil {
			log.C(ctx).Errorw("Failed to create card record", "book_id", bookID, "page_index", i, "error", err.Error())
			continue
		}

		cards = append(cards, card)

		// 在后台异步生成分页图片
		go func(cardID uint, pageIndex int, pageData interface{}) {
			p.generatePaginatedImageInBackground(ctx, cardID, bookID, pageData, templateID, pageIndex)
		}(card.ID, i, pageData)
	}

	log.C(ctx).Infow("✅ 分页卡片创建完成", "book_id", bookID, "card_count", len(cards))
	return cards, nil
}

// generatePaginatedImageInBackground 在后台生成分页图片
func (p *AsyncBookProcessor) generatePaginatedImageInBackground(ctx context.Context, cardID, bookID uint, pageData interface{}, templateID string, pageIndex int) {
	startTime := time.Now()
	log.C(ctx).Infow("开始后台生成分页图片", "card_id", cardID, "book_id", bookID, "page_index", pageIndex)

	// 获取模板背景信息
	var templateBackground string
	if templateID != "" {
		if tid, err := strconv.ParseUint(templateID, 10, 64); err == nil {
			template, err := p.biz.Templates().GetByID(ctx, uint(tid))
			if err != nil {
				log.C(ctx).Warnw("Failed to get template, using default white background", "template_id", templateID, "error", err.Error())
				templateBackground = "" // 使用默认白色背景
			} else if template.File != "" {
				templateBackground = template.File
				log.C(ctx).Infow("Template background loaded", "template_id", templateID, "background", templateBackground)
			} else {
				log.C(ctx).Warnw("Template has no file, using default white background", "template_id", templateID)
				templateBackground = "" // 使用默认白色背景
			}
		} else {
			log.C(ctx).Warnw("Invalid template ID format, using default white background", "template_id", templateID, "error", err.Error())
			templateBackground = "" // 使用默认白色背景
		}
	} else {
		log.C(ctx).Infow("No template ID provided, using default white background")
		templateBackground = "" // 使用默认白色背景
	}

	// 生成分页图片
	imageURL, err := p.generatePaginatedImage(ctx, pageData, templateBackground, pageIndex)
	if err != nil {
		log.C(ctx).Errorw("Failed to generate paginated image", "card_id", cardID, "page_index", pageIndex, "error", err.Error())
		return
	}

	// 更新CardM记录
	card, err := p.biz.Cards().GetByID(ctx, cardID)
	if err != nil {
		log.C(ctx).Errorw("Failed to get card for update", "card_id", cardID, "error", err.Error())
		return
	}

	card.RenderedImage = imageURL
	if err := p.biz.Cards().Update(ctx, card); err != nil {
		log.C(ctx).Errorw("Failed to update card with rendered image", "card_id", cardID, "error", err.Error())
		return
	}

	duration := time.Since(startTime)
	log.C(ctx).Infow("✅ 分页图片生成完成", "card_id", cardID, "book_id", bookID, "page_index", pageIndex, "duration", duration.String(), "image_url", imageURL)
}

// generatePaginatedImage 生成分页图片的具体实现
func (p *AsyncBookProcessor) generatePaginatedImage(ctx context.Context, pageData interface{}, templateBackground string, pageIndex int) (string, error) {
	log.C(ctx).Infow("开始生成分页图片", "page_index", pageIndex)

	// 这里实现分页图片生成逻辑
	// 1. 将分页数据转换为HTML
	// 2. 应用模板背景
	// 3. 使用wkhtmltoimage生成分页图片
	// 4. 上传到COS
	// 5. 返回COS链接

	// 为了简化，这里返回一个占位符
	// 实际实现需要调用现有的分页图片生成逻辑
	imageURL := fmt.Sprintf("/images/paginated_card_%d_%d.jpg", time.Now().Unix(), pageIndex)

	log.C(ctx).Infow("✅ 分页图片生成完成", "page_index", pageIndex, "image_url", imageURL)
	return imageURL, nil
}

// processBookCreationInBackground 在后台处理book创建
func (p *AsyncBookProcessor) processBookCreationInBackground(ctx context.Context, bookID uint, userID uint, text, templateID string) {
	startTime := time.Now()
	log.C(ctx).Infow("Starting async book creation", "book_id", bookID, "user_id", userID)

	// 获取book记录
	book, err := p.biz.Books().GetByID(ctx, bookID)
	if err != nil {
		log.C(ctx).Errorw("Failed to get book for async processing", "book_id", bookID, "error", err.Error())
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Failed to get book record")
		return
	}

	// 获取模板背景信息
	var templateBackground string
	if templateID != "" {
		// 将string类型的templateID转换为uint
		if tid, err := strconv.ParseUint(templateID, 10, 64); err == nil {
			template, err := p.biz.Templates().GetByID(ctx, uint(tid))
			if err != nil {
				log.C(ctx).Warnw("Failed to get template, using default white background", "template_id", templateID, "error", err.Error())
				templateBackground = "" // 使用默认白色背景
			} else if template.File != "" {
				templateBackground = template.File
				log.C(ctx).Infow("Template background loaded", "template_id", templateID, "background", templateBackground)
			} else {
				log.C(ctx).Warnw("Template has no file, using default white background", "template_id", templateID)
				templateBackground = "" // 使用默认白色背景
			}
		} else {
			log.C(ctx).Warnw("Invalid template ID format, using default white background", "template_id", templateID, "error", err.Error())
			templateBackground = "" // 使用默认白色背景
		}
	} else {
		log.C(ctx).Infow("No template ID provided, using default white background")
		templateBackground = "" // 使用默认白色背景
	}

	// 🚀 第一步：更新状态为AI处理中
	log.C(ctx).Infow("🚀 更新状态为AI处理中", "book_id", bookID)
	if err := p.updateBookStatus(ctx, bookID, model.BookStatusAI, ""); err != nil {
		log.C(ctx).Errorw("Failed to update book status to AI processing", "book_id", bookID, "error", err.Error())
	}

	// 调用文字大模型，获取返回的 markdown 格式内容
	log.C(ctx).Infow("🚀 第一步：调用文字大模型处理文本", "book_id", bookID, "text_length", len(text))

	// 获取配置文件中的提示词（优先从Redis/数据库读取）
	var textProcessingPrompt string
	if p.configReader != nil {
		textProcessingPrompt = p.configReader.GetTextProcessingPrompt(ctx)
	} else {
		textProcessingPrompt = viper.GetString("ai_prompts.text_processing")
	}
	if textProcessingPrompt == "" {
		log.C(ctx).Errorw("❌ 配置文件中未找到ai_prompts.text_processing", "book_id", bookID)
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Missing text_processing prompt in config")
		return
	}

	// 获取模型配置参数（优先从Redis/数据库读取）
	var maxTokens int = 4000
	var temperature float64 = 0.7
	if p.configReader != nil {
		maxTokens = p.configReader.GetVolcTokens(ctx)
		temperature = p.configReader.GetVolcTemperature(ctx)
	}

	// 构建消息 - 将用户文本拼接到提示词的 # 待处理的OCR文本 后面
	fullPrompt := textProcessingPrompt + "\n\n" + text
	messages := []map[string]string{
		{"role": "user", "content": fullPrompt},
	}

	// 打印发送给大模型的完整提示词
	log.C(ctx).Infow("📝 发送给大模型的完整提示词",
		"book_id", bookID,
		"prompt_length", len(fullPrompt),
		"user_text_length", len(text),
		"full_prompt", fullPrompt)

	// 调用AI模型处理文本 - 先尝试火山方舟，失败后降级到阿里百炼
	aiResponse, err := p.biz.Volc().VolcTextStream(ctx, messages, maxTokens, temperature)
	if err != nil {
		log.C(ctx).Warnw("⚠️ 火山方舟API失败，尝试阿里百炼降级", "book_id", bookID, "error", err.Error())

		// 降级到阿里百炼API
		aiResponse, err = p.biz.Ali().QianwenTextStream(messages, maxTokens, temperature)
		if err != nil {
			log.C(ctx).Errorw("❌ 所有AI API都失败", "book_id", bookID, "error", err.Error())
			p.updateBookStatus(ctx, bookID, model.BookStatusFailed, fmt.Sprintf("All AI APIs failed: %v", err))
			return
		}
		log.C(ctx).Infow("✅ 阿里百炼API降级成功", "book_id", bookID)
	} else {
		log.C(ctx).Infow("✅ 火山方舟API调用成功", "book_id", bookID)
	}

	// 验证AI响应
	if aiResponse == "" {
		log.C(ctx).Errorw("❌ AI返回空响应", "book_id", bookID)
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "AI returned empty response")
		return
	}

	log.C(ctx).Infow("📈 AI处理完成", "book_id", bookID, "response_length", len(aiResponse))

	// 解析AI返回的markdown内容
	markdownContent, imagePrompt := p.parseMarkdownResponse(aiResponse)
	if markdownContent == "" {
		log.C(ctx).Errorw("❌ 无法解析markdown内容", "book_id", bookID)
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Failed to parse markdown content")
		return
	}

	log.C(ctx).Infow("🎨 第一步：解析markdown内容", "book_id", bookID, "markdown_content", markdownContent)

	// 从markdown文本中提取title作为book的标题
	bookTitle := p.extractTitleFromMarkdown(markdownContent)
	if bookTitle == "" {
		bookTitle = fmt.Sprintf("AI生成卡册 - %s", time.Now().Format("2006-01-02 15:04:05"))
	}

	// 🎨 第二步：从解析结果中提取 image_prompt 字段，作为文生图大模型的提示词
	log.C(ctx).Infow("🎨 第二步：提取image_prompt字段", "book_id", bookID, "image_prompt", imagePrompt)

	// 🖼️ 第三步：调用文生图大模型生成图片 - 已注释，不需要生成图片
	var imageUrl string
	// if imagePrompt != "" {
	// 	log.C(ctx).Infow("🖼️ 第三步：调用文生图大模型生成图片", "book_id", bookID, "image_prompt", imagePrompt)

	// 	// 打印发送给文生图模型的提示词
	// 	log.C(ctx).Infow("📝 发送给文生图模型的提示词",
	// 		"book_id", bookID,
	// 		"prompt_length", len(imagePrompt),
	// 		"full_prompt", imagePrompt)

	// 	// 调用stable-diffusion API生成图片
	// 	remoteImageUrl, err := p.biz.Ali().StableDiffusionImageAsync(imagePrompt, "1024*1024")
	// 	if err != nil {
	// 		log.C(ctx).Errorw("StableDiffusionImageAsync failed", "book_id", bookID, "error", err.Error())
	// 		// 图片生成失败不影响整体流程，但记录错误
	// 	} else {
	// 		log.C(ctx).Infow("✅ 文生图大模型生成图片成功", "book_id", bookID, "remote_image_url", remoteImageUrl)

	// 		// 📁 第四步：图片存储 - 按照指定路径规则存储
	// 		// 卡册封面图片路径：resource.image_path/{bookid}/book_{id}.webp
	// 		localImagePath, err := p.downloadAndSaveImageWithPath(remoteImageUrl, bookID)
	// 		if err != nil {
	// 			log.C(ctx).Errorw("Failed to download and save image", "book_id", bookID, "error", err.Error())
	// 		} else {
	// 			imageUrl = localImagePath
	// 			log.C(ctx).Infow("✅ 卡册封面图片存储成功", "book_id", bookID, "local_image_path", localImagePath)
	// 		}
	// 	}
	// } else {
	// 	log.C(ctx).Warnw("⚠️ 未找到image_prompt字段，跳过图片生成", "book_id", bookID)
	// }

	// 跳过图片生成，直接设置为空
	log.C(ctx).Infow("⚠️ 跳过图片生成步骤，imageUrl设置为空", "book_id", bookID)

	// 更新book记录
	book.Title = bookTitle
	book.ImageUrl = imageUrl
	if err := p.biz.Books().Update(ctx, book); err != nil {
		log.C(ctx).Errorw("Failed to update book with title and image", "book_id", bookID, "error", err.Error())
	}

	// 🎨 第四步：更新状态为渲染中
	log.C(ctx).Infow("🎨 更新状态为渲染中", "book_id", bookID)
	if err := p.updateBookStatus(ctx, bookID, model.BookStatusRender, ""); err != nil {
		log.C(ctx).Errorw("Failed to update book status to rendering", "book_id", bookID, "error", err.Error())
	}

	// 使用简化的markdown渲染器处理
	// 直接使用markdownContent作为markdown内容，自动分页和渲染
	if markdownContent != "" {
		log.C(ctx).Infow("使用AI返回的markdown文本", "book_id", bookID, "text_length", len(markdownContent))

		// 直接使用markdown渲染器处理
		if err := p.processBookWithMarkdownRenderer(ctx, book, userID, markdownContent, templateBackground); err != nil {
			log.C(ctx).Errorw("markdown渲染器处理失败", "book_id", bookID, "error", err.Error())
			p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "markdown渲染器处理失败: "+err.Error())
			return
		}
	} else {
		// 后备方案：使用原始文本
		log.C(ctx).Infow("AI未返回markdown内容，使用原始文本作为后备方案", "book_id", bookID, "original_text_length", len(text))

		if err := p.processBookWithMarkdownRenderer(ctx, book, userID, text, templateBackground); err != nil {
			log.C(ctx).Errorw("markdown渲染器处理失败", "book_id", bookID, "error", err.Error())
			p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "markdown渲染器处理失败: "+err.Error())
			return
		}
	}

	// 直接完成，不需要其他渲染流程
	log.C(ctx).Infow("🎉 markdown渲染器处理完成，跳过其他渲染流程", "book_id", bookID)
	// 直接进行最后的状态更新并返回
	p.finalizeBookCreation(ctx, bookID, startTime)
}

// updateBookStatus 更新book状态
func (p *AsyncBookProcessor) updateBookStatus(ctx context.Context, bookID uint, status, errorMsg string) error {
	book, err := p.biz.Books().GetByID(ctx, bookID)
	if err != nil {
		return err
	}

	oldStatus := book.Status
	book.Status = status

	if err := p.biz.Books().Update(ctx, book); err != nil {
		return err
	}

	// 如果状态发生变化，需要更新用户统计
	if oldStatus != status {
		// 调用store层的方法来更新用户统计
		if err := p.biz.Store().UpdateUserBookStatsOnStatusChange(ctx, book.UserID, oldStatus, status); err != nil {
			// 记录错误但不影响状态更新操作
			// 这里可以考虑记录日志
		}
	}

	return nil
}

// QianwenResponse 通义千问返回的结构化数据
type QianwenResponse struct {
	Text        string `json:"text"`         // 带markdown格式的文字内容
	ImagePrompt string `json:"image_prompt"` // 文生图提示词
}

// extractJSONWithRetry 带重试的JSON提取（更激进的修复策略）
func extractJSONWithRetry(response string) string {
	fmt.Printf("=== 重试JSON提取（激进策略）===\n")

	// 策略1: 尝试修复不完整的JSON结构
	fixedJSON := fixIncompleteJSON(response)
	if fixedJSON != "" && isValidJSON(fixedJSON) {
		fmt.Printf("重试成功：修复了不完整的JSON结构\n")
		return fixedJSON
	}

	// 策略2: 查找包含关键字段的部分JSON并强制修复
	keyFields := []string{"structured_text_array", "image_prompt"}
	for _, field := range keyFields {
		fieldIndex := strings.Index(response, fmt.Sprintf(`"%s"`, field))
		if fieldIndex != -1 {
			fmt.Printf("重试策略：找到字段 '%s'，尝试强制修复\n", field)

			// 向前查找最近的 { 开始
			braceStart := -1
			for i := fieldIndex; i >= 0; i-- {
				if response[i] == '{' {
					braceStart = i
					break
				}
			}

			if braceStart != -1 {
				partialJSON := response[braceStart:]
				// 使用更激进的修复策略
				aggressiveFixed := aggressiveJSONFix(partialJSON)
				if aggressiveFixed != "" && isValidJSON(aggressiveFixed) {
					fmt.Printf("重试成功：使用激进策略修复了JSON\n")
					return aggressiveFixed
				}
			}
		}
	}

	fmt.Printf("重试失败：所有激进策略都失败了\n")
	return ""
}

// cleanJSONWithSmartFilter 使用智能过滤清理JSON，保留中文字符
func cleanJSONWithSmartFilter(jsonStr string) string {
	var result strings.Builder
	removedCount := 0

	fmt.Printf("开始智能字符过滤，原始长度: %d\n", len(jsonStr))

	for i, char := range jsonStr {
		// 1. 检查是否是无效的Unicode字符
		if char == utf8.RuneError || char == 0xFFFD {
			fmt.Printf("移除无效Unicode字符，位置: %d, 字符: 0x%02x\n", i, char)
			removedCount++
			continue
		}

		// 2. 检查是否是控制字符（除了换行符和制表符）
		if char >= 0 && char <= 31 && char != '\n' && char != '\t' {
			fmt.Printf("移除控制字符，位置: %d, 字符: 0x%02x (rune: %q)\n", i, char, char)
			removedCount++
			continue
		}

		// 3. 检查是否是扩展ASCII字符（128-255）
		if char >= 128 && char <= 255 {
			fmt.Printf("移除扩展ASCII字符，位置: %d, 字符: 0x%02x (rune: %q)\n", i, char, char)
			removedCount++
			continue
		}

		// 4. 检查是否是JSON结构中的问题字符
		if isJSONStructureProblemChar(char, jsonStr, i) {
			fmt.Printf("移除JSON结构问题字符，位置: %d, 字符: 0x%02x (rune: %q)\n", i, char, char)
			removedCount++
			continue
		}

		// 5. 保留所有其他字符（包括中文字符）
		result.WriteRune(char)
	}

	cleaned := result.String()
	fmt.Printf("智能字符过滤完成: %d -> %d 字符，移除了 %d 个字符\n", len(jsonStr), len(cleaned), removedCount)

	if removedCount > 0 {
		// 显示清理后的预览
		if len(cleaned) > 0 {
			preview := cleaned
			if len(preview) > 100 {
				preview = preview[:100] + "..."
			}
			fmt.Printf("清理后预览: %q\n", preview)
		}
	}

	return cleaned
}

// isJSONStructureProblemChar 检查是否是JSON结构中的问题字符
func isJSONStructureProblemChar(char rune, jsonStr string, position int) bool {
	// 检查是否是JSON结构中的常见问题字符
	problemChars := []rune{'i', 'j', 'k', 'l', 'm', 'n', 'o', 'p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z'}

	// 检查是否是问题字符
	for _, problemChar := range problemChars {
		if char == problemChar {
			// 进一步检查上下文，判断是否真的是问题字符
			return isContextuallyProblematicChar(jsonStr, position, char)
		}
	}

	return false
}

// isContextuallyProblematicChar 检查字符在上下文中是否真的有问题
func isContextuallyProblematicChar(jsonStr string, position int, char rune) bool {
	// 检查字符前后的上下文
	before := ""
	after := ""

	if position > 0 {
		before = string(jsonStr[position-1])
	}
	if position < len(jsonStr)-1 {
		after = string(jsonStr[position+1])
	}

	// 如果字符前后都是有效的JSON字符，那么它可能不是问题字符
	validBefore := isValidJSONContextChar(before)
	validAfter := isValidJSONContextChar(after)

	// 如果前后都是有效的，那么这个字符可能不是问题
	if validBefore && validAfter {
		return false
	}

	// 检查是否在JSON字符串中
	inString := isInJSONString(jsonStr, position)
	if inString {
		// 在JSON字符串中的字符通常是有效的
		return false
	}

	// 检查是否在JSON对象或数组的键值对中
	if isInJSONKeyValue(jsonStr, position) {
		// 在键值对中的字符可能是问题字符
		return true
	}

	return false
}

// isValidJSONContextChar 检查字符是否是有效的JSON上下文
func isValidJSONContextChar(char string) bool {
	if char == "" {
		return true
	}

	validChars := []string{`"`, `{`, `}`, `[`, `]`, `:`, `,`, ` `, `\n`, `\t`}
	for _, valid := range validChars {
		if char == valid {
			return true
		}
	}

	return false
}

// isInJSONString 检查位置是否在JSON字符串中
func isInJSONString(jsonStr string, position int) bool {
	// 计算当前位置之前的引号数量
	quoteCount := 0
	escaped := false

	for i := 0; i < position; i++ {
		if jsonStr[i] == '\\' && !escaped {
			escaped = true
			continue
		}

		if jsonStr[i] == '"' && !escaped {
			quoteCount++
		}

		escaped = false
	}

	// 如果引号数量是奇数，说明在字符串中
	return quoteCount%2 == 1
}

// isInJSONKeyValue 检查位置是否在JSON键值对中
func isInJSONKeyValue(jsonStr string, position int) bool {
	// 查找最近的冒号
	colonPos := -1
	for i := position; i >= 0; i-- {
		if jsonStr[i] == ':' {
			colonPos = i
			break
		}
	}

	if colonPos == -1 {
		return false
	}

	// 查找冒号后的下一个引号或大括号
	for i := colonPos + 1; i < len(jsonStr); i++ {
		if jsonStr[i] == '"' || jsonStr[i] == '{' || jsonStr[i] == '[' {
			// 如果当前位置在这个范围内，说明在键值对中
			return position > colonPos && position < i
		}
	}

	return false
}

// aggressiveJSONFix 激进的JSON修复策略
func aggressiveJSONFix(jsonStr string) string {
	fmt.Printf("使用激进策略修复JSON，原始长度: %d\n", len(jsonStr))

	// 策略1: 使用智能字符过滤，保留中文字符
	cleaned := cleanJSONWithSmartFilter(jsonStr)

	// 策略2: 强制修复JSON结构
	repaired := repairJSONStructure(cleaned)

	// 策略3: 如果仍然无效，尝试添加默认值
	if !isValidJSON(repaired) {
		fmt.Printf("激进修复后仍然无效，尝试添加默认值...\n")
		repaired = addDefaultValues(repaired)
	}

	return repaired
}

// addDefaultValues 为不完整的JSON添加默认值
func addDefaultValues(jsonStr string) string {
	// 检查是否包含 structured_text_array
	if !strings.Contains(jsonStr, "structured_text_array") {
		// 在最后一个 } 之前添加默认的 structured_text_array
		lastBrace := strings.LastIndex(jsonStr, "}")
		if lastBrace != -1 {
			defaultArray := `,"structured_text_array":[{"type":"body","content":"内容解析失败，请重试"}]`
			jsonStr = jsonStr[:lastBrace] + defaultArray + jsonStr[lastBrace:]
		}
	}

	// 检查是否包含 image_prompt
	if !strings.Contains(jsonStr, "image_prompt") {
		// 在最后一个 } 之前添加默认的 image_prompt
		lastBrace := strings.LastIndex(jsonStr, "}")
		if lastBrace != -1 {
			defaultPrompt := `,"image_prompt":"默认图片描述"`
			jsonStr = jsonStr[:lastBrace] + defaultPrompt + jsonStr[lastBrace:]
		}
	}

	return jsonStr
}

// extractJSONFromResponse 从响应中提取JSON内容
func extractJSONFromResponse(response string) string {
	// 记录原始响应用于调试
	fmt.Printf("Raw response length: %d\n", len(response))
	if len(response) > 1000 {
		fmt.Printf("Raw response preview (first 500 chars): %q\n", response[:500])
		fmt.Printf("Raw response preview (last 500 chars): %q\n", response[len(response)-500:])
	} else {
		fmt.Printf("Raw response: %q\n", response)
	}

	// 策略1: 尝试直接解析（如果响应本身就是有效的JSON）
	if isValidJSON(response) {
		fmt.Printf("Response is already valid JSON\n")
		return response
	}

	// 策略2: 使用新的JSON响应处理器进行深度修复
	fmt.Printf("使用新的JSON响应处理器进行深度修复...\n")

	// 创建模拟的HTTP响应，使用新的JSON响应处理器
	processor := httpclient.NewJSONResponseProcessor()

	// 模拟HTTP响应结构
	mockResp := &http.Response{
		Body: io.NopCloser(strings.NewReader(response)),
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}

	// 使用新的处理器处理响应
	processedBody, err := processor.ProcessResponse(mockResp)
	if err == nil && len(processedBody) > 0 {
		fmt.Printf("新的JSON响应处理器处理成功，长度: %d\n", len(processedBody))

		// 验证处理后的JSON是否有效
		if isValidJSON(string(processedBody)) {
			fmt.Printf("处理后的JSON验证成功\n")
			return string(processedBody)
		} else {
			fmt.Printf("处理后的JSON验证失败，继续使用旧方法\n")
		}
	} else {
		fmt.Printf("新的JSON响应处理器处理失败: %v，继续使用旧方法\n", err)
	}

	// 策略3: 深度清理响应内容（旧方法作为备选）
	cleanedResponse := deepCleanResponse(response)
	fmt.Printf("Deep cleaned response length: %d\n", len(cleanedResponse))

	// 尝试解析深度清理后的响应
	if isValidJSON(cleanedResponse) {
		fmt.Printf("Deep cleaned response is valid JSON\n")
		return cleanedResponse
	}

	// 策略3: 智能提取JSON内容
	extractedJSON := smartExtractJSON(cleanedResponse)
	if extractedJSON != "" && isValidJSON(extractedJSON) {
		fmt.Printf("Successfully extracted valid JSON, length: %d\n", len(extractedJSON))
		return extractedJSON
	}

	// 策略4: 回退到最基础的提取方法
	fallbackJSON := fallbackExtractJSON(cleanedResponse)
	if fallbackJSON != "" && isValidJSON(fallbackJSON) {
		fmt.Printf("Using fallback JSON extraction, length: %d\n", len(fallbackJSON))
		return fallbackJSON
	}

	// 策略5: 最后尝试修复常见问题
	fixedJSON := fixCommonJSONIssues(response)
	if fixedJSON != "" && isValidJSON(fixedJSON) {
		fmt.Printf("Fixed common JSON issues, length: %d\n", len(fixedJSON))
		return fixedJSON
	}

	// 如果所有方法都失败，记录错误并返回空字符串
	fmt.Printf("All JSON extraction methods failed\n")
	return ""
}

// deepCleanResponse 深度清理响应内容
func deepCleanResponse(response string) string {
	// 第一步：移除所有HTML标签及其内容
	cleaned := response

	// 移除 <think> 标签及其内容
	cleaned = removeTagContent(cleaned, "think")

	// 移除其他可能的HTML标签
	cleaned = removeTagContent(cleaned, "html")
	cleaned = removeTagContent(cleaned, "body")
	cleaned = removeTagContent(cleaned, "div")
	cleaned = removeTagContent(cleaned, "p")
	cleaned = removeTagContent(cleaned, "span")
	cleaned = removeTagContent(cleaned, "script")
	cleaned = removeTagContent(cleaned, "style")
	cleaned = removeTagContent(cleaned, "head")
	cleaned = removeTagContent(cleaned, "title")
	cleaned = removeTagContent(cleaned, "meta")
	cleaned = removeTagContent(cleaned, "link")

	// 第二步：标准化换行符和空格
	cleaned = strings.ReplaceAll(cleaned, "\r\n", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\n\n", "\n")
	cleaned = strings.TrimSpace(cleaned)

	// 第三步：移除BOM标记
	if len(cleaned) > 3 && cleaned[0] == 0xEF && cleaned[1] == 0xBB && cleaned[2] == 0xBF {
		cleaned = cleaned[3:]
	}

	// 第四步：移除控制字符，但保留必要的字符
	var result strings.Builder
	for _, r := range cleaned {
		// 保留：字母、数字、标点符号、空格、换行、制表符
		// 移除：控制字符、零宽字符、其他不可见字符
		if (r >= 32 && r <= 126) || r == '\n' || r == '\t' || r == '\r' {
			result.WriteRune(r)
		}
	}

	// 第五步：移除多余的空白字符
	cleaned = result.String()
	cleaned = strings.ReplaceAll(cleaned, "  ", " ")    // 双空格变单空格
	cleaned = strings.ReplaceAll(cleaned, "\n\n", "\n") // 双换行变单换行
	cleaned = strings.TrimSpace(cleaned)

	return cleaned
}

// removeTagContent 移除指定标签及其内容
func removeTagContent(content, tagName string) string {
	// 移除开始标签
	startTag := fmt.Sprintf("<%s", tagName)
	endTag := fmt.Sprintf("</%s>", tagName)

	// 查找开始标签位置
	startPos := strings.Index(content, startTag)
	if startPos == -1 {
		return content
	}

	// 查找结束标签位置
	endPos := strings.Index(content, endTag)
	if endPos == -1 {
		// 如果没有结束标签，只移除开始标签
		return content[:startPos] + content[startPos+len(startTag):]
	}

	// 移除整个标签及其内容
	return content[:startPos] + content[endPos+len(endTag):]
}

// isValidJSON 验证字符串是否为有效的JSON
func isValidJSON(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}

	var js json.RawMessage
	err := json.Unmarshal([]byte(s), &js)
	if err != nil {
		fmt.Printf("JSON validation failed: %v\n", err)
		return false
	}
	return true
}

// fixCommonJSONIssues 修复常见的JSON问题（保留原有功能）
func fixCommonJSONIssues(response string) string {
	cleaned := response

	// 修复1: 移除JSON末尾的额外内容
	// 查找最后一个有效的 } 或 ]
	lastBrace := strings.LastIndex(cleaned, "}")
	lastBracket := strings.LastIndex(cleaned, "]")

	var endPos int
	if lastBrace > lastBracket {
		endPos = lastBrace + 1
	} else if lastBracket > lastBrace {
		endPos = lastBracket + 1
	} else {
		return cleaned // 没有找到结束符
	}

	// 移除JSON末尾的额外内容
	cleaned = cleaned[:endPos]

	// 修复2: 处理可能的编码问题
	// 移除常见的无效字符序列
	cleaned = strings.ReplaceAll(cleaned, "\\'", "'")
	cleaned = strings.ReplaceAll(cleaned, "\\\"", "\"")

	// 修复3: 确保JSON结构完整
	// 如果以 { 开始但没有对应的 }，尝试添加
	if strings.HasPrefix(cleaned, "{") && !strings.HasSuffix(cleaned, "}") {
		// 计算大括号的平衡
		braceCount := 0
		for _, char := range cleaned {
			if char == '{' {
				braceCount++
			} else if char == '}' {
				braceCount--
			}
		}

		// 如果缺少结束大括号，添加它们
		for i := 0; i < braceCount; i++ {
			cleaned += "}"
		}
	}

	// 修复4: 处理可能的Unicode转义问题
	// 移除无效的Unicode转义序列
	cleaned = removeInvalidUnicodeEscapes(cleaned)

	// 修复5: 修复常见的JSON结构问题
	cleaned = fixJSONStructureIssues(cleaned)

	return cleaned
}

// fixJSONStructureIssues 修复JSON结构问题
func fixJSONStructureIssues(jsonStr string) string {
	// 修复缺失的逗号
	jsonStr = strings.ReplaceAll(jsonStr, "}\n{", "},\n{")
	jsonStr = strings.ReplaceAll(jsonStr, "}\n \"", "},\n \"")
	jsonStr = strings.ReplaceAll(jsonStr, "]\n{", "],\n{")
	jsonStr = strings.ReplaceAll(jsonStr, "]\n \"", "],\n \"")

	// 修复缺失的引号
	jsonStr = strings.ReplaceAll(jsonStr, "content\": \"", "content\": \"")
	jsonStr = strings.ReplaceAll(jsonStr, "type\": \"", "type\": \"")

	// 修复数组元素之间的分隔
	jsonStr = strings.ReplaceAll(jsonStr, "\"}\n{", "\"},\n{")
	jsonStr = strings.ReplaceAll(jsonStr, "\"]\n[", "\"],\n[")

	// 修复对象属性之间的分隔
	jsonStr = strings.ReplaceAll(jsonStr, "\"\n \"", "\",\n \"")

	fmt.Printf("修复了常见的JSON结构问题\n")
	return jsonStr
}

// removeInvalidUnicodeEscapes 移除无效的Unicode转义序列
func removeInvalidUnicodeEscapes(s string) string {
	// 查找并移除无效的 \u 转义序列
	var result strings.Builder
	i := 0
	for i < len(s) {
		if i+5 < len(s) && s[i] == '\\' && s[i+1] == 'u' {
			// 检查接下来的4个字符是否为有效的十六进制
			hexStr := s[i+2 : i+6]
			if isValidHexString(hexStr) {
				result.WriteString(s[i : i+6])
				i += 6
			} else {
				// 无效的Unicode转义，跳过
				result.WriteByte(s[i])
				i++
			}
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}

// isValidHexString 检查字符串是否为有效的十六进制
func isValidHexString(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, char := range s {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

// smartExtractJSON 智能提取JSON内容
func smartExtractJSON(response string) string {
	// 策略1: 优先查找包含关键字段的JSON（最重要的策略）
	fieldBasedJSON := findJSONByFields(response)
	if fieldBasedJSON != "" {
		fmt.Printf("Found JSON by fields (PRIORITY), length: %d\n", len(fieldBasedJSON))
		return fieldBasedJSON
	}

	// 策略2: 查找最长的JSON对象
	longestJSON := findLongestJSON(response)
	if longestJSON != "" {
		fmt.Printf("Found longest JSON object, length: %d\n", len(longestJSON))
		return longestJSON
	}

	// 策略3: 查找JSON数组
	arrayJSON := findJSONArray(response)
	if arrayJSON != "" {
		fmt.Printf("Found JSON array, length: %d\n", len(arrayJSON))
		return arrayJSON
	}

	// 策略4: 回退到原始提取方法
	return fallbackExtractJSON(response)
}

// findLongestJSON 查找最长的有效JSON对象
func findLongestJSON(response string) string {
	var longestJSON string
	maxLength := 0

	// 查找所有可能的JSON对象
	braceCount := 0
	start := -1

	for i, char := range response {
		if char == '{' {
			if braceCount == 0 {
				start = i
			}
			braceCount++
		} else if char == '}' {
			braceCount--
			if braceCount == 0 && start != -1 {
				// 找到一个完整的JSON对象
				jsonCandidate := response[start : i+1]
				if isValidJSON(jsonCandidate) && len(jsonCandidate) > maxLength {
					longestJSON = jsonCandidate
					maxLength = len(jsonCandidate)
				}
				start = -1
			}
		}
	}

	// 如果没有找到完整的JSON对象，尝试修复不完整的JSON
	if longestJSON == "" {
		fmt.Printf("No complete JSON found, attempting to fix incomplete JSON...\n")
		longestJSON = fixIncompleteJSON(response)
	}

	return longestJSON
}

// findJSONArray 查找JSON数组
func findJSONArray(response string) string {
	var longestArray string
	maxLength := 0

	// 查找所有可能的JSON数组
	bracketCount := 0
	start := -1

	for i, char := range response {
		if char == '[' {
			if bracketCount == 0 {
				start = i
			}
			bracketCount++
		} else if char == ']' {
			bracketCount--
			if bracketCount == 0 && start != -1 {
				// 找到一个完整的JSON数组
				jsonCandidate := response[start : i+1]
				if isValidJSON(jsonCandidate) && len(jsonCandidate) > maxLength {
					longestArray = jsonCandidate
					maxLength = len(jsonCandidate)
				}
				start = -1
			}
		}
	}

	return longestArray
}

// findJSONByFields 根据字段查找JSON
func findJSONByFields(response string) string {
	// 查找包含关键字段的JSON
	keyFields := []string{"structured_text_array", "image_prompt"}

	// 策略1: 查找包含所有关键字段的完整JSON对象
	braceCount := 0
	start := -1

	for i, char := range response {
		if char == '{' {
			if braceCount == 0 {
				start = i
			}
			braceCount++
		} else if char == '}' {
			braceCount--
			if braceCount == 0 && start != -1 {
				// 检查是否包含所有关键字段
				jsonCandidate := response[start : i+1]
				if containsAllFields(jsonCandidate, keyFields) && isValidJSON(jsonCandidate) {
					fmt.Printf("Found complete JSON with all key fields\n")
					return jsonCandidate
				}
				start = -1
			}
		}
	}

	// 策略2: 如果没有找到完整的JSON，尝试查找包含关键字段的部分JSON
	fmt.Printf("No complete JSON with all fields found, searching for partial JSON...\n")

	// 查找包含关键字段的部分JSON
	for _, field := range keyFields {
		fieldIndex := strings.Index(response, fmt.Sprintf(`"%s"`, field))
		if fieldIndex != -1 {
			fmt.Printf("Found field '%s' at position %d\n", field, fieldIndex)

			// 向前查找最近的 { 开始
			braceStart := -1
			for i := fieldIndex; i >= 0; i-- {
				if response[i] == '{' {
					braceStart = i
					break
				}
			}

			if braceStart != -1 {
				// 尝试修复这部分JSON
				partialJSON := response[braceStart:]
				fmt.Printf("Attempting to fix partial JSON starting with field '%s'...\n", field)

				fixedJSON := fixIncompleteJSON(partialJSON)
				if fixedJSON != "" && isValidJSON(fixedJSON) {
					fmt.Printf("Successfully fixed partial JSON containing field '%s'\n", field)
					return fixedJSON
				}
			}
		}
	}

	// 策略3: 如果仍然没有找到，尝试查找包含至少一个关键字段的JSON
	fmt.Printf("No partial JSON found, searching for JSON with at least one key field...\n")

	// 查找包含至少一个关键字段的JSON
	for _, field := range keyFields {
		fieldIndex := strings.Index(response, fmt.Sprintf(`"%s"`, field))
		if fieldIndex != -1 {
			fmt.Printf("Found field '%s' at position %d, attempting to extract surrounding JSON...\n", field, fieldIndex)

			// 向前查找最近的 { 开始
			braceStart := -1
			for i := fieldIndex; i >= 0; i-- {
				if response[i] == '{' {
					braceStart = i
					break
				}
			}

			if braceStart != -1 {
				// 尝试从 { 开始提取到响应末尾，然后修复
				partialJSON := response[braceStart:]
				fmt.Printf("Extracting partial JSON from position %d to end, length: %d\n", braceStart, len(partialJSON))

				// 使用更激进的修复策略
				aggressiveFixed := aggressiveJSONFix(partialJSON)
				if aggressiveFixed != "" && isValidJSON(aggressiveFixed) {
					fmt.Printf("Successfully extracted and fixed JSON containing field '%s'\n", field)
					return aggressiveFixed
				}
			}
		}
	}

	return ""
}

// containsAllFields 检查JSON字符串是否包含所有指定字段
func containsAllFields(jsonStr string, fields []string) bool {
	for _, field := range fields {
		if !strings.Contains(jsonStr, fmt.Sprintf(`"%s"`, field)) {
			return false
		}
	}
	return true
}

// fixIncompleteJSON 修复不完整的JSON
func fixIncompleteJSON(response string) string {
	fmt.Printf("Attempting to fix incomplete JSON...\n")

	// 查找最后一个 { 开始的位置
	lastBraceStart := strings.LastIndex(response, "{")
	if lastBraceStart == -1 {
		fmt.Printf("No opening brace found\n")
		return ""
	}

	// 从最后一个 { 开始，尝试构建完整的JSON
	partialJSON := response[lastBraceStart:]
	fmt.Printf("Found partial JSON starting at position %d, length: %d\n", lastBraceStart, len(partialJSON))

	// 尝试修复常见的JSON结构问题
	fixedJSON := fixCommonJSONIssues(partialJSON)

	// 如果修复后仍然无效，尝试添加缺失的结束符
	if !isValidJSON(fixedJSON) {
		fmt.Printf("JSON still invalid after common fixes, attempting structural repair...\n")
		fixedJSON = repairJSONStructure(fixedJSON)
	}

	// 验证修复后的JSON
	if isValidJSON(fixedJSON) {
		fmt.Printf("Successfully fixed incomplete JSON, length: %d\n", len(fixedJSON))
		return fixedJSON
	}

	fmt.Printf("Failed to fix incomplete JSON\n")
	return ""
}

// repairJSONStructure 修复JSON结构问题
func repairJSONStructure(jsonStr string) string {
	// 首先清理JSON字符串，移除无效字符
	cleaned := cleanJSONStringForStructure(jsonStr)

	// 尝试修复常见的JSON结构问题
	cleaned = fixCommonJSONIssues(cleaned)

	// 计算大括号和方括号的平衡
	braceCount := 0
	bracketCount := 0

	for _, char := range cleaned {
		switch char {
		case '{':
			braceCount++
		case '}':
			braceCount--
		case '[':
			bracketCount++
		case ']':
			bracketCount--
		}
	}

	// 添加缺失的结束符
	var result strings.Builder
	result.WriteString(cleaned)

	// 添加缺失的方括号结束符
	for i := 0; i < bracketCount; i++ {
		result.WriteString("]")
	}

	// 添加缺失的大括号结束符
	for i := 0; i < braceCount; i++ {
		result.WriteString("}")
	}

	fmt.Printf("Repaired JSON structure: added %d brackets and %d braces\n", bracketCount, braceCount)
	return result.String()
}

// cleanJSONStringForStructure 清理JSON字符串，专门用于结构修复
func cleanJSONStringForStructure(jsonStr string) string {
	var result strings.Builder
	removedCount := 0

	fmt.Printf("开始清理JSON结构，原始长度: %d\n", len(jsonStr))

	for i, char := range jsonStr {
		// 1. 保留所有有效的JSON结构字符
		if char == '{' || char == '}' || char == '[' || char == ']' || char == ':' || char == ',' || char == '"' {
			result.WriteRune(char)
			continue
		}

		// 2. 保留所有空白字符
		if char == ' ' || char == '\n' || char == '\t' || char == '\r' {
			result.WriteRune(char)
			continue
		}

		// 3. 保留所有字母数字字符和常用符号（用于键名和字符串值）
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') ||
			char == '_' || char == '-' || char == '.' || char == '!' || char == '?' || char == ';' || char == '(' || char == ')' {
			result.WriteRune(char)
			continue
		}

		// 4. 保留中文字符和其他Unicode字符
		if char > 127 {
			result.WriteRune(char)
			continue
		}

		// 5. 只移除真正有问题的控制字符
		if char >= 0 && char <= 31 && char != '\n' && char != '\t' && char != '\r' {
			fmt.Printf("移除控制字符，位置: %d, 字符: 0x%02x (rune: %q)\n", i, char, char)
			removedCount++
			continue
		}

		// 6. 保留其他字符（包括下划线、感叹号等）
		result.WriteRune(char)
	}

	cleaned := result.String()
	fmt.Printf("JSON结构清理完成: %d -> %d 字符，移除了 %d 个字符\n", len(jsonStr), len(cleaned), removedCount)

	return cleaned
}

// fallbackExtractJSON 回退提取方法
func fallbackExtractJSON(response string) string {
	// 查找第一个 { 和最后一个 }
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")

	if start != -1 && end != -1 && end > start {
		candidate := response[start : end+1]
		fmt.Printf("Fallback extraction: found JSON candidate from %d to %d\n", start, end)
		return candidate
	}

	// 如果没有找到 { }，尝试查找 [ ]
	start = strings.Index(response, "[")
	end = strings.LastIndex(response, "]")

	if start != -1 && end != -1 && end > start {
		candidate := response[start : end+1]
		fmt.Printf("Fallback extraction: found JSON array candidate from %d to %d\n", start, end)
		return candidate
	}

	fmt.Printf("Fallback extraction: no JSON structure found\n")
	return ""
}

// downloadAndSaveImage 下载并保存图片
func downloadAndSaveImage(remoteURL string, bookID uint) (string, error) {
	// 计算本地保存目录：{image_path}/book/{book_id}
	localDir := util.GetBookImagePath(bookID)

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

	return localFilePath, nil
}

// finalizeBookCreation 完成book创建的最终步骤
func (p *AsyncBookProcessor) finalizeBookCreation(ctx context.Context, bookID uint, startTime time.Time) {
	// 更新book状态为成功
	if err := p.updateBookStatus(ctx, bookID, model.BookStatusSuccess, ""); err != nil {
		log.C(ctx).Errorw("Failed to update book status to success", "book_id", bookID, "error", err.Error())
		return
	}

	log.C(ctx).Infow("Async book creation completed", "book_id", bookID, "duration", time.Since(startTime).Seconds())
}

// callVolcWithRetry 带重试的火山引擎API调用（支持动态参数调整）
func (p *AsyncBookProcessor) callVolcWithRetry(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64, bookID uint) (string, error) {
	maxRetries := 5
	baseDelay := 2 * time.Second
	maxDelay := 30 * time.Second

	// 动态参数
	currentMaxTokens := maxTokens
	currentTemperature := temperature

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		log.C(ctx).Infow("🔄 尝试火山引擎API",
			"book_id", bookID,
			"attempt", attempt,
			"max_attempts", maxRetries,
			"max_tokens", currentMaxTokens,
			"temperature", currentTemperature)

		result, err := p.biz.Volc().VolcTextStream(ctx, messages, currentMaxTokens, currentTemperature)
		if err == nil {
			log.C(ctx).Infow("✅ 火山引擎API成功", "book_id", bookID, "attempt", attempt)
			return result, nil
		}

		lastErr = err
		log.C(ctx).Warnw("⚠️ 火山引擎API失败", "book_id", bookID, "attempt", attempt, "error", err.Error())

		// 动态调整参数（在重试时）
		if attempt < maxRetries {
			// 增加max_tokens以应对长文本
			if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "length") {
				currentMaxTokens = int(float64(currentMaxTokens) * 1.5)
				log.C(ctx).Infow("🔧 检测到token相关错误，增加max_tokens",
					"book_id", bookID,
					"old_tokens", maxTokens,
					"new_tokens", currentMaxTokens)
			}

			// 降低temperature提高稳定性
			currentTemperature = currentTemperature * 0.8
			if currentTemperature < 0.1 {
				currentTemperature = 0.1
			}

			delay := time.Duration(attempt-1) * baseDelay
			if delay > maxDelay {
				delay = maxDelay
			}

			log.C(ctx).Infow("⏳ 等待重试",
				"book_id", bookID,
				"delay", delay,
				"next_attempt", attempt+1,
				"adjusted_tokens", currentMaxTokens,
				"adjusted_temperature", currentTemperature)

			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
				// 继续重试
			}
		}
	}

	return "", fmt.Errorf("火山引擎API重试%d次后仍失败: %v", maxRetries, lastErr)
}

// callQianwenWithRetry 带重试的阿里千问API调用（支持动态参数调整）
func (p *AsyncBookProcessor) callQianwenWithRetry(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64, bookID uint) (string, error) {
	maxRetries := 3
	baseDelay := 2 * time.Second

	// 动态参数
	currentMaxTokens := maxTokens
	currentTemperature := temperature

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		log.C(ctx).Infow("🔄 尝试阿里千问API",
			"book_id", bookID,
			"attempt", attempt,
			"max_attempts", maxRetries,
			"max_tokens", currentMaxTokens,
			"temperature", currentTemperature)

		result, err := p.biz.Ali().QianwenTextStream(messages, currentMaxTokens, currentTemperature)
		if err == nil {
			log.C(ctx).Infow("✅ 阿里千问API成功", "book_id", bookID, "attempt", attempt)
			return result, nil
		}

		lastErr = err
		log.C(ctx).Warnw("⚠️ 阿里千问API失败", "book_id", bookID, "attempt", attempt, "error", err.Error())

		// 动态调整参数（在重试时）
		if attempt < maxRetries {
			// 增加max_tokens以应对长文本
			if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "length") || strings.Contains(err.Error(), "too long") {
				currentMaxTokens = int(float64(currentMaxTokens) * 1.5)
				log.C(ctx).Infow("🔧 检测到token相关错误，增加max_tokens",
					"book_id", bookID,
					"old_tokens", maxTokens,
					"new_tokens", currentMaxTokens)
			}

			// 降低temperature提高稳定性
			currentTemperature = currentTemperature * 0.8
			if currentTemperature < 0.1 {
				currentTemperature = 0.1
			}

			delay := time.Duration(attempt) * baseDelay

			log.C(ctx).Infow("⏳ 等待重试",
				"book_id", bookID,
				"delay", delay,
				"next_attempt", attempt+1,
				"adjusted_tokens", currentMaxTokens,
				"adjusted_temperature", currentTemperature)

			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
				// 继续重试
			}
		}
	}

	return "", fmt.Errorf("阿里千问API重试%d次后仍失败: %v", maxRetries, lastErr)
}

// extractTitleFromMarkdown 从markdown文本中提取标题
func (p *AsyncBookProcessor) extractTitleFromMarkdown(markdown string) string {
	lines := strings.Split(markdown, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			// 找到一级标题，返回标题内容（去掉# 前缀）
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

// removeFirstLevelHeading 删除markdown文本中的一级标题
func (p *AsyncBookProcessor) removeFirstLevelHeading(text string) string {
	lines := strings.Split(text, "\n")
	var result []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// 跳过空行或以单个#开头的一级标题
		if trimmed == "" || !strings.HasPrefix(trimmed, "# ") {
			result = append(result, line)
		}
	}

	return strings.Join(result, "\n")
}

// convertMarkdownToElements 将markdown文本转换为分页元素
func (p *AsyncBookProcessor) convertMarkdownToElements(markdown string) []pagination.Element {
	var elements []pagination.Element
	lines := strings.Split(markdown, "\n")

	var currentContent strings.Builder
	var currentType pagination.ElementType = pagination.ElementTypeBody

	for i, line := range lines {
		line = strings.TrimSpace(line)

		// 跳过空行
		if line == "" {
			// 如果当前有内容，先保存当前元素
			if currentContent.Len() > 0 {
				content := strings.TrimSpace(currentContent.String())
				if content != "" {
					elements = append(elements, pagination.Element{
						Type:    currentType,
						Content: content,
					})
				}
				currentContent.Reset()
			}
			continue
		}

		// 检查是否是标题
		if strings.HasPrefix(line, "# ") {
			// 保存之前的内容
			if currentContent.Len() > 0 {
				content := strings.TrimSpace(currentContent.String())
				if content != "" {
					elements = append(elements, pagination.Element{
						Type:    currentType,
						Content: content,
					})
				}
				currentContent.Reset()
			}

			// 跳过一级标题（已经在book title中处理）
			continue
		}

		// 检查是否是二级标题
		if strings.HasPrefix(line, "## ") {
			// 保存之前的内容
			if currentContent.Len() > 0 {
				content := strings.TrimSpace(currentContent.String())
				if content != "" {
					elements = append(elements, pagination.Element{
						Type:    currentType,
						Content: content,
					})
				}
				currentContent.Reset()
			}

			// 二级标题
			title := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			if title != "" {
				elements = append(elements, pagination.Element{
					Type:    pagination.ElementTypeSubtitle,
					Content: title,
				})
			}
			continue
		}

		// 检查是否是列表项
		if strings.HasPrefix(line, "- ") {
			// 保存之前的内容
			if currentContent.Len() > 0 {
				content := strings.TrimSpace(currentContent.String())
				if content != "" {
					elements = append(elements, pagination.Element{
						Type:    currentType,
						Content: content,
					})
				}
				currentContent.Reset()
			}

			// 收集列表项
			var listItems []string
			listItems = append(listItems, strings.TrimSpace(strings.TrimPrefix(line, "- ")))

			// 继续收集后续的列表项
			for j := i + 1; j < len(lines); j++ {
				nextLine := strings.TrimSpace(lines[j])
				if strings.HasPrefix(nextLine, "- ") {
					listItems = append(listItems, strings.TrimSpace(strings.TrimPrefix(nextLine, "- ")))
				} else if nextLine == "" {
					// 空行表示列表结束
					break
				} else {
					// 非列表项，停止收集
					break
				}
			}

			if len(listItems) > 0 {
				elements = append(elements, pagination.Element{
					Type:    pagination.ElementTypeList,
					Content: listItems,
				})
			}
			continue
		}

		// 检查是否是引用
		if strings.HasPrefix(line, "> ") {
			// 保存之前的内容
			if currentContent.Len() > 0 {
				content := strings.TrimSpace(currentContent.String())
				if content != "" {
					elements = append(elements, pagination.Element{
						Type:    currentType,
						Content: content,
					})
				}
				currentContent.Reset()
			}

			// 收集引用内容
			quote := strings.TrimSpace(strings.TrimPrefix(line, "> "))
			if quote != "" {
				elements = append(elements, pagination.Element{
					Type:    pagination.ElementTypeQuote,
					Content: quote,
				})
			}
			continue
		}

		// 普通段落内容
		if currentContent.Len() > 0 {
			currentContent.WriteString("\n")
		}
		currentContent.WriteString(line)
	}

	// 保存最后的内容
	if currentContent.Len() > 0 {
		content := strings.TrimSpace(currentContent.String())
		if content != "" {
			elements = append(elements, pagination.Element{
				Type:    currentType,
				Content: content,
			})
		}
	}

	return elements
}

// processBookWithMarkdownRenderer 使用markdown渲染器处理书籍
// 直接使用aiResponse.Text作为markdown内容，自动分页和渲染
func (p *AsyncBookProcessor) processBookWithMarkdownRenderer(
	ctx context.Context,
	book *model.BookM,
	userID uint,
	markdownText string,
	coverBackground string,
) error {
	log.C(ctx).Infow("开始使用增强版markdown渲染器处理", "book_id", book.ID, "text_length", len(markdownText))

	// 1. 首先创建封面卡片（使用上下布局）
	if err := p.createEnhancedCoverCard(ctx, book, userID, coverBackground); err != nil {
		log.C(ctx).Errorw("创建增强版封面卡片失败", "book_id", book.ID, "error", err.Error())
		// 封面创建失败不影响主流程，继续处理内容卡片
	}

	// 2. 处理markdown内容，分割为多张卡片
	markdownCards, err := p.splitAndCreateMarkdownCards(ctx, book, userID, markdownText, coverBackground)
	if err != nil {
		return fmt.Errorf("创建markdown内容卡片失败: %v", err)
	}

	log.C(ctx).Infow("markdown内容卡片创建成功", "book_id", book.ID, "card_count", len(markdownCards))

	// 3. 更新book统计信息
	totalCards := 1 + len(markdownCards) // 1个封面卡片 + markdown内容卡片
	book.CardCount = totalCards
	if err := p.biz.Books().Update(ctx, book); err != nil {
		log.C(ctx).Warnw("更新book卡片统计失败", "book_id", book.ID, "error", err.Error())
	}

	// 4. 更新用户卡片统计
	for i := 0; i < totalCards; i++ {
		if err := p.biz.Users().IncrementUserCardNum(ctx, userID); err != nil {
			log.C(ctx).Errorw("更新用户卡片统计失败", "book_id", book.ID, "user_id", userID, "error", err.Error())
		}
	}

	log.C(ctx).Infow("markdown渲染器处理完成", "book_id", book.ID)
	return nil
}

// createEnhancedCoverCard 创建增强版封面卡片（上下布局）
func (p *AsyncBookProcessor) createEnhancedCoverCard(
	ctx context.Context,
	book *model.BookM,
	userID uint,
	coverBackground string,
) error {
	log.C(ctx).Infow("🎨 创建增强版封面卡片", "book_id", book.ID, "title", book.Title)

	// 创建封面卡片记录
	coverCard := &model.CardM{
		UserID:    userID,
		BookID:    book.ID,
		SortOrder: 0, // 封面卡片排序为0
	}

	// 设置简化的markdown格式内容，而不是完整的HTML
	coverMarkdown := fmt.Sprintf("# %s", book.Title)
	coverCard.ProcessedText = coverMarkdown
	log.C(ctx).Infow("封面markdown内容设置完成", "book_id", book.ID, "markdown_length", len(coverMarkdown))

	// 创建封面卡片记录
	if err := p.biz.Cards().Create(ctx, coverCard); err != nil {
		return fmt.Errorf("创建封面卡片记录失败: %v", err)
	}

	log.C(ctx).Infow("✅ 增强版封面卡片创建成功", "book_id", book.ID, "card_id", coverCard.ID)

	// 生成封面HTML内容用于渲染
	coverHTML := p.generateCoverHTML(book.Title, book.ImageUrl, coverBackground, book.ID)

	// 生成封面图片（使用封面HTML，不重新生成）
	if err := p.generateCoverImageOnly(ctx, coverCard.ID, coverHTML); err != nil {
		log.C(ctx).Errorw("封面图片生成失败", "card_id", coverCard.ID, "error", err.Error())
	} else {
		log.C(ctx).Infow("✅ 封面图片生成成功", "card_id", coverCard.ID)
	}

	return nil
}

// splitAndCreateMarkdownCards 分割markdown内容并创建卡片
func (p *AsyncBookProcessor) splitAndCreateMarkdownCards(
	ctx context.Context,
	book *model.BookM,
	userID uint,
	markdownText string,
	templateBackground string,
) ([]*model.CardM, error) {
	log.C(ctx).Infow("📄 分割markdown内容为多张卡片", "book_id", book.ID, "text_length", len(markdownText))

	// 优先使用流式分页渲染器：多页 -> 多卡
	if cardbiz.IsFlowRendererEnabled() {
		flow := cardbiz.NewFlowRenderer(pagination.LoadConfigFromViper())
		log.C(ctx).Infow("🔍 调用FlowRenderer分页", "book_id", book.ID, "markdown_preview", markdownText[:min(len(markdownText), 200)])
		pages, err := flow.PaginateMarkdownWithBackground(ctx, markdownText, templateBackground)
		if err != nil || len(pages) == 0 {
			log.C(ctx).Warnw("流式分页失败，回退到按文本切分", "book_id", book.ID, "error", err)
		} else {
			log.C(ctx).Infow("✅ FlowRenderer分页成功", "book_id", book.ID, "pages_count", len(pages))
			var createdCards []*model.CardM
			// 为每一页创建一条卡片记录（从1开始，0为封面）
			for i, inner := range pages {
				log.C(ctx).Infow("🔍 FlowRenderer返回的页面HTML片段", "book_id", book.ID, "page", i+1, "html_preview", inner[:min(len(inner), 200)])
				// 生成完整页面HTML（带背景图）
				pageHTML := p.wrapFlowPageHTMLWithBackground(inner, templateBackground)
				// 先创建卡片记录
				cardRecord := &model.CardM{
					UserID:        userID,
					BookID:        book.ID,
					ProcessedText: inner, // 使用FlowRenderer分页后的HTML内容
					SortOrder:     i + 1,
				}
				if err := p.biz.Cards().Create(ctx, cardRecord); err != nil {
					log.C(ctx).Errorw("创建分页卡片失败", "book_id", book.ID, "page", i+1, "error", err.Error())
					continue
				}

				// 渲染图片
				imagePath, rErr := p.renderHTMLToImage(ctx, cardRecord.ID, pageHTML)
				if rErr != nil {
					log.C(ctx).Warnw("分页卡片渲染失败", "card_id", cardRecord.ID, "error", rErr.Error())
				} else {
					cardRecord.RenderedImage = imagePath
					if uErr := p.biz.Cards().Update(ctx, cardRecord); uErr != nil {
						log.C(ctx).Warnw("更新分页卡片图片路径失败", "card_id", cardRecord.ID, "error", uErr.Error())
					}
				}

				createdCards = append(createdCards, cardRecord)
				log.C(ctx).Infow("✅ 分页卡片创建成功", "book_id", book.ID, "card_id", cardRecord.ID, "sort_order", cardRecord.SortOrder)
			}
			log.C(ctx).Infow("✅ 流式分页多卡创建完成", "book_id", book.ID, "total_cards", len(createdCards))
			return createdCards, nil
		}
	}

	// 回退：仍按文本估算切分
	cardContents := p.splitMarkdownIntoCards(markdownText)
	var createdCards []*model.CardM
	for i, content := range cardContents {
		if strings.TrimSpace(content) == "" {
			continue
		}
		cardRecord := &model.CardM{
			UserID:        userID,
			BookID:        book.ID,
			ProcessedText: content,
			SortOrder:     i + 1,
		}
		if err := p.biz.Cards().Create(ctx, cardRecord); err != nil {
			log.C(ctx).Errorw("创建markdown卡片失败", "book_id", book.ID, "card_index", i+1, "error", err.Error())
			continue
		}
		createdCards = append(createdCards, cardRecord)
		if err := p.generateCardImageAndHTML(ctx, cardRecord.ID, content, templateBackground); err != nil {
			log.C(ctx).Errorw("卡片图片和HTML生成失败", "card_id", cardRecord.ID, "error", err.Error())
		}
		log.C(ctx).Infow("📄 markdown卡片创建成功", "book_id", book.ID, "card_id", cardRecord.ID, "sort_order", cardRecord.SortOrder)
	}
	log.C(ctx).Infow("✅ 所有markdown卡片创建完成", "book_id", book.ID, "total_cards", len(createdCards))
	return createdCards, nil
}

// splitMarkdownIntoCards 将markdown内容分割为多张卡片（基于固定边距的精准分页版本）
func (p *AsyncBookProcessor) splitMarkdownIntoCards(content string) []string {
	// 在分页前移除所有一级标题行，避免生成只含H1的空白内容卡
	content = stripH1OnlyFromMarkdown(content)

	// 使用HTML转换器的固定边距分页逻辑
	htmlConverter := p.getHTMLConverter()
	cards, err := htmlConverter.SplitContentByHeight(content)
	if err != nil {
		// 如果分页失败，回退到原有的分页逻辑
		log.C(context.Background()).Warnw("固定边距分页失败，回退到原有逻辑", "error", err)
		return p.splitMarkdownIntoCardsFallback(content)
	}

	log.C(context.Background()).Infow("固定边距分页完成", "original_content_length", len(content), "cards_count", len(cards))
	return cards
}

// stripH1OnlyFromMarkdown 去除所有以 "# " 开头的一级标题行
func stripH1OnlyFromMarkdown(markdown string) string {
	lines := strings.Split(markdown, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "# ") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// splitMarkdownIntoCardsFallback 回退的分页逻辑（优化实现）
func (p *AsyncBookProcessor) splitMarkdownIntoCardsFallback(content string) []string {
	// 预处理：清理输入文本，移除无效内容
	content = strings.TrimSpace(content)
	if content == "" || content == "\"" || content == "'" {
		return []string{}
	}

	lines := strings.Split(content, "\n")

	// 使用统一的配置读取
	cardConfig := util.GetCardRenderingConfig()
	fontConfig := util.GetFontConfig()

	// 卡片配置
	cardHeight := cardConfig.Height
	cardPadding := cardConfig.GetTotalPadding()
	const bottomMarginLimit = 80 // 优化的底部边距限制，与HTML转换器一致
	availableHeight := cardHeight - cardPadding
	maxFillHeight := availableHeight - bottomMarginLimit // 有效内容高度

	// 字体和行高配置
	titleFontSize := fontConfig.TitleSize
	subtitleFontSize := fontConfig.SubtitleSize
	bodyFontSize := fontConfig.BodySize
	availableWidth := cardConfig.GetAvailableWidth()
	titleLineHeight := fontConfig.TitleLineHeight
	bodyLineHeight := fontConfig.BodyLineHeight
	const titleMarginBottom = 16
	const bodyMarginBottom = 16

	// 第一步：预处理，计算所有行的高度
	type LineInfo struct {
		content      string
		height       int
		marginBottom int
		totalHeight  int
		isTitle      bool
		titleLevel   int
	}

	var lineInfos []LineInfo
	totalContentHeight := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// 过滤无效行
		if line == "" || line == "\"" || line == "'" || len(line) < 2 {
			continue
		}

		lineInfo := LineInfo{content: line}

		if strings.HasPrefix(line, "# ") {
			lineInfo.height = p.calculateTextHeight(line[2:], titleFontSize, availableWidth, titleLineHeight)
			lineInfo.marginBottom = titleMarginBottom
			lineInfo.isTitle = true
			lineInfo.titleLevel = 1
		} else if strings.HasPrefix(line, "## ") {
			lineInfo.height = p.calculateTextHeight(line[3:], subtitleFontSize, availableWidth, titleLineHeight)
			lineInfo.marginBottom = titleMarginBottom
			lineInfo.isTitle = true
			lineInfo.titleLevel = 2
		} else if strings.HasPrefix(line, "### ") {
			lineInfo.height = p.calculateTextHeight(line[4:], subtitleFontSize, availableWidth, titleLineHeight)
			lineInfo.marginBottom = titleMarginBottom
			lineInfo.isTitle = true
			lineInfo.titleLevel = 3
		} else {
			lineInfo.height = p.calculateTextHeight(line, bodyFontSize, availableWidth, bodyLineHeight)
			lineInfo.marginBottom = bodyMarginBottom
		}

		lineInfo.totalHeight = lineInfo.height + lineInfo.marginBottom
		totalContentHeight += lineInfo.totalHeight
		lineInfos = append(lineInfos, lineInfo)
	}

	// 第二步：估算需要的卡片数量
	estimatedCards := int(math.Ceil(float64(totalContentHeight) / float64(maxFillHeight)))
	targetHeightPerCard := float64(totalContentHeight) / float64(estimatedCards)

	// 第三步：智能分页
	var cards []string
	var currentCard strings.Builder
	currentHeight := 0
	cardIndex := 0

	for i, lineInfo := range lineInfos {
		needNewCard := false

		// 1. 一级标题强制新卡片（除了第一行）
		if lineInfo.isTitle && lineInfo.titleLevel == 1 && currentCard.Len() > 0 {
			needNewCard = true
		}

		// 2. 硬性限制：超出最大高度
		if currentHeight+lineInfo.totalHeight > maxFillHeight {
			needNewCard = true
		}

		// 3. 平衡内容分布：避免第一张卡片过空，后续卡片过满
		if !needNewCard && currentCard.Len() > 0 {
			currentUtilization := float64(currentHeight) / float64(maxFillHeight)
			remainingHeight := 0
			for j := i; j < len(lineInfos); j++ {
				remainingHeight += lineInfos[j].totalHeight
			}
			remainingCards := estimatedCards - cardIndex - 1
			if remainingCards <= 0 {
				remainingCards = 1
			}
			avgRemainingHeight := float64(remainingHeight) / float64(remainingCards)

			if lineInfo.isTitle && lineInfo.titleLevel == 2 {
				// 二级标题：动态阈值，如果是第一张卡片则要求更高的利用率才分页
				threshold := 0.80
				if cardIndex == 0 {
					threshold = 0.85 // 第一张卡片要求更高利用率，避免过早分页
				}
				if currentUtilization > threshold && avgRemainingHeight > targetHeightPerCard*0.6 {
					needNewCard = true
				}
			} else if lineInfo.isTitle && lineInfo.titleLevel == 3 {
				// 三级标题：对第一张卡片要求更高利用率
				threshold := 0.85
				if cardIndex == 0 {
					threshold = 0.90
				}
				if currentUtilization > threshold {
					needNewCard = true
				}
			} else {
				// 普通内容：对第一张卡片要求接近满载才分页
				threshold := 0.90
				if cardIndex == 0 {
					threshold = 0.95
				}
				if currentUtilization > threshold && avgRemainingHeight > targetHeightPerCard*0.4 {
					needNewCard = true
				}
			}
		}

		// 执行分页
		if needNewCard && currentCard.Len() > 0 {
			cards = append(cards, strings.TrimSpace(currentCard.String()))
			currentCard.Reset()
			currentHeight = 0
			cardIndex++
		}

		// 添加当前行
		if currentCard.Len() > 0 {
			currentCard.WriteString("\n")
		}
		currentCard.WriteString(lineInfo.content)
		currentHeight += lineInfo.totalHeight
	}

	// 添加最后一张卡片
	if currentCard.Len() > 0 {
		cardContent := strings.TrimSpace(currentCard.String())
		// 确保最后一张卡片也有有效内容
		if cardContent != "" && cardContent != "\"" && cardContent != "'" && len(cardContent) > 2 {
			cards = append(cards, cardContent)
		}
	}

	// 确保至少有一张卡片
	if len(cards) == 0 && content != "" {
		// 如果没有有效卡片，将所有内容合并到一张卡片
		fallbackContent := strings.TrimSpace(content)
		if fallbackContent != "" && fallbackContent != "\"" && fallbackContent != "'" {
			cards = append(cards, fallbackContent)
		}
	}

	return cards
}

// calculateTextHeight 计算文本高度
func (p *AsyncBookProcessor) calculateTextHeight(text string, fontSize int, availableWidth int, lineHeightMultiplier float64) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}

	// 计算字符宽度（中文字符约为字体大小的1.05倍，英文字符约为0.6倍）
	charWidth := float64(fontSize) * 0.8 // 更准确的字符宽度
	charsPerLine := int(float64(availableWidth) / charWidth)

	if charsPerLine <= 0 {
		charsPerLine = 1
	}

	// 计算行数
	textLength := utf8.RuneCountInString(text)
	lines := int(math.Ceil(float64(textLength) / float64(charsPerLine)))

	if lines == 0 {
		lines = 1
	}

	// 计算总高度：行数 × 字体大小 × 行高倍数
	totalHeight := int(float64(lines) * float64(fontSize) * lineHeightMultiplier)

	return totalHeight
}

// getHTMLConverter 获取HTML转换器实例
func (p *AsyncBookProcessor) getHTMLConverter() *markdown.HTMLConverter {
	return markdown.NewHTMLConverter()
}

// getHTMLConverterWithBackground 获取带背景的HTML转换器实例
func (p *AsyncBookProcessor) getHTMLConverterWithBackground(templateBackground string) *markdown.HTMLConverter {
	converter := markdown.NewHTMLConverter()
	if templateBackground != "" {
		converter.SetBackgroundImage(templateBackground)
	}
	return converter
}

// downloadAndSaveImageWithPath 按照指定路径规则下载并保存图片
func (p *AsyncBookProcessor) downloadAndSaveImageWithPath(remoteURL string, bookID uint) (string, error) {
	// 计算本地保存目录：{image_path}/book/{book_id}
	localDir := util.GetBookImagePath(bookID)

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

	// 返回绝对路径，用于数据库存储
	return localFilePath, nil
}

// downloadAndSaveCardImageWithPath 按照指定路径规则下载并保存卡片图片
func (p *AsyncBookProcessor) downloadAndSaveCardImageWithPath(remoteURL string, cardID uint) (string, error) {
	// 计算本地保存目录：{image_path}/card/{card_id}
	localDir := util.GetCardImagePath(cardID)

	// 确保目录存在
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", localDir, err)
	}

	// 固定文件名：card_{id}.webp
	localFilePath := filepath.Join(localDir, fmt.Sprintf("card_%d.webp", cardID))

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

	// 返回相对路径，用于数据库存储
	imagePath := util.GetImagePath()
	relativePath := strings.TrimPrefix(localFilePath, imagePath)
	if !strings.HasPrefix(relativePath, "/") {
		relativePath = "/" + relativePath
	}

	return relativePath, nil
}

// createCardHTMLFile 创建卡片的临时HTML文件
func (p *AsyncBookProcessor) createCardHTMLFile(cardID uint, htmlContent string) (string, error) {
	// 计算本地保存目录：{image_path}/card/{card_id}
	localDir := util.GetCardImagePath(cardID)

	// 确保目录存在
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", localDir, err)
	}

	// 固定文件名：card_{id}.html
	localFilePath := filepath.Join(localDir, fmt.Sprintf("card_%d.html", cardID))

	// 创建HTML文件并写入
	file, err := os.Create(localFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to create HTML file: %w", err)
	}
	defer file.Close()

	if _, err := file.WriteString(htmlContent); err != nil {
		return "", fmt.Errorf("failed to write HTML content: %w", err)
	}

	// 返回相对路径，用于数据库存储
	imagePath := util.GetImagePath()
	relativePath := strings.TrimPrefix(localFilePath, imagePath)
	if !strings.HasPrefix(relativePath, "/") {
		relativePath = "/" + relativePath
	}

	return relativePath, nil
}

// generateCardImageAndHTML 为卡片生成图片和HTML文件
func (p *AsyncBookProcessor) generateCardImageAndHTML(ctx context.Context, cardID uint, markdownContent string, templateBackground string) error {
	log.C(ctx).Infow("开始为卡片生成图片和HTML文件", "card_id", cardID, "content_length", len(markdownContent))

	// 1. 获取卡片信息以确定排序和标题
	card, err := p.biz.Cards().GetByID(ctx, cardID)
	if err != nil {
		return fmt.Errorf("获取卡片信息失败: %v", err)
	}

	// 2. 将markdown转换为HTML 或使用流式分页渲染器生成分页HTML片段
	var pages []string
	useFlow := cardbiz.IsFlowRendererEnabled()
	if useFlow {
		// 使用流式分页：得到每页的 innerHTML 片段
		flow := cardbiz.NewFlowRenderer(pagination.LoadConfigFromViper())
		var errFlow error
		pages, errFlow = flow.PaginateMarkdown(ctx, markdownContent)
		if errFlow != nil || len(pages) == 0 {
			log.C(ctx).Warnw("流式分页失败，回退到旧渲染", "card_id", cardID, "error", errFlow)
			useFlow = false
		}
	}

	var htmlContent string
	if !useFlow {
		htmlConverter := p.getHTMLConverterWithBackground(templateBackground)
		htmlContent = htmlConverter.ConvertMarkdownCardToHTML(markdownContent, "卡片内容", card.SortOrder)
	}

	// 3. 渲染为图片
	if useFlow {
		// 对于流式分页：逐页渲染，当前 cardID 对应第一页；如需扩展为多张卡，需要在上游创建多条卡记录。
		// 这里保持单卡单页输出：渲染第一页
		first := pages[0]
		pageHTML := p.wrapFlowPageHTMLWithBackground(first, templateBackground)
		imagePath, err := p.renderHTMLToImage(ctx, cardID, pageHTML)
		if err != nil {
			log.C(ctx).Warnw("HTML转图片失败", "card_id", cardID, "error", err.Error())
		} else {
			log.C(ctx).Infow("✅ 卡片图片生成成功", "card_id", cardID, "image_path", imagePath)
			card.RenderedImage = imagePath
			if err := p.biz.Cards().Update(ctx, card); err != nil {
				log.C(ctx).Warnw("更新卡片图片路径失败", "card_id", cardID, "error", err.Error())
			}
		}
		return nil
	}

	// 使用轻量级渲染器将HTML转换为图片
	imagePath, err := p.renderHTMLToImage(ctx, cardID, htmlContent)
	if err != nil {
		log.C(ctx).Warnw("HTML转图片失败", "card_id", cardID, "error", err.Error())
		// 图片生成失败不影响整体流程
	} else {
		log.C(ctx).Infow("✅ 卡片图片生成成功", "card_id", cardID, "image_path", imagePath)

		// 5. 上传图片到COS
		if util.IsCOSEnabled() && imagePath != "" {
			// 读取生成的图片文件
			if imageData, err := os.ReadFile(imagePath); err == nil {
				// 构建COS对象键：card/{card_id}/card_{card_id}.webp
				objectKey := fmt.Sprintf("card/%d/card_%d.webp", cardID, cardID)

				// 上传到COS
				cosURL, uploadErr := util.UploadBytesToCOS(ctx, objectKey, "image/webp", imageData)
				if uploadErr != nil {
					log.C(ctx).Warnw("上传图片到COS失败", "card_id", cardID, "error", uploadErr.Error())
				} else if cosURL != "" {
					log.C(ctx).Infow("✅ 卡片图片已上传到COS", "card_id", cardID, "cos_url", cosURL)

					// 生成签名URL（可选，如果需要的话）
					if signedURL, err := util.GenerateSignedURL(ctx, objectKey, 600); err == nil && signedURL != "" {
						log.C(ctx).Infow("COS签名URL生成成功", "card_id", cardID, "signed_url", signedURL)
					}
				}
			} else {
				log.C(ctx).Warnw("读取图片文件失败", "card_id", cardID, "path", imagePath, "error", err.Error())
			}
		}

		// 6. 更新卡片记录，保存图片路径
		card.RenderedImage = imagePath
		if err := p.biz.Cards().Update(ctx, card); err != nil {
			log.C(ctx).Warnw("更新卡片图片路径失败", "card_id", cardID, "error", err.Error())
		}
	}

	log.C(ctx).Infow("✅ 卡片处理完成", "card_id", cardID, "image_path", imagePath)
	return nil
}

// renderHTMLToImage 将HTML转换为图片
func (p *AsyncBookProcessor) renderHTMLToImage(ctx context.Context, cardID uint, htmlContent string) (string, error) {
	log.C(ctx).Infow("开始将HTML转换为图片", "card_id", cardID)

	// 获取图片输出路径配置
	imagePath := viper.GetString("resource.image_path")
	if imagePath == "" {
		return "", fmt.Errorf("未配置resource.image_path")
	}

	// 创建卡片图片目录
	cardImageDir := filepath.Join(imagePath, "card", fmt.Sprintf("%d", cardID))
	if err := os.MkdirAll(cardImageDir, 0755); err != nil {
		return "", fmt.Errorf("创建卡片图片目录失败: %v", err)
	}

	// 生成图片文件名和完整路径
	imageFileName := fmt.Sprintf("card_%d.webp", cardID)
	fullImagePath := filepath.Join(cardImageDir, imageFileName)

	// 直接使用内部的wkhtmltoimage工具（总是可用）
	return p.renderWithWkhtmltoimage(ctx, cardID, htmlContent, fullImagePath)
}

// wrapFlowPageHTML 将流式分页得到的单页 innerHTML 包装为完整的固定尺寸HTML（无背景）
func (p *AsyncBookProcessor) wrapFlowPageHTML(inner string) string {
	return p.wrapFlowPageHTMLWithBackground(inner, "")
}

// wrapFlowPageHTMLWithBackground 将流式分页得到的单页 innerHTML 包装为完整的固定尺寸HTML（带背景支持）
func (p *AsyncBookProcessor) wrapFlowPageHTMLWithBackground(inner, backgroundImage string) string {
	// 从分页配置读取排版（与 FlowRenderer 同源）
	pg := pagination.LoadConfigFromViper()
	// 使用统一的CSS生成函数，确保与分页环境完全一致
	css := cardbiz.GenerateUnifiedCSS(pg, backgroundImage)

	// 提取页码HTML（如果存在）
	var pageNumberHTML, contentHTML string
	if strings.Contains(inner, "page-number") {
		// 查找页码HTML
		start := strings.Index(inner, `<div class="page-number">`)
		if start >= 0 {
			end := strings.Index(inner[start:], `</div>`) + start + 6
			if end > start {
				pageNumberHTML = inner[start:end]
				contentHTML = strings.TrimSpace(inner[:start] + inner[end:])
			} else {
				contentHTML = inner
			}
		} else {
			contentHTML = inner
		}
	} else {
		contentHTML = inner
	}

	doc := fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Flow Page</title>
  <style>%s</style>
</head>
<body>
  <div class="page">
    <div class="content">%s</div>
    %s
  </div>
</body>
</html>`, css, contentHTML, pageNumberHTML)
	return doc
}

func colorOrDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func alignOrDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// renderWithWkhtmltoimage 使用wkhtmltoimage渲染
func (p *AsyncBookProcessor) renderWithWkhtmltoimage(ctx context.Context, cardID uint, htmlContent, fullImagePath string) (string, error) {
	// 获取渲染槽位，避免同时启动过多Chrome实例导致资源耗尽
	if err := acquireRenderSlot(ctx); err != nil {
		return "", fmt.Errorf("获取渲染槽位失败: %v", err)
	}
	defer releaseRenderSlot()

	log.C(ctx).Infow("使用wkhtmltoimage渲染（已获取渲染槽位）", "card_id", cardID)

	// 检查原始HTML内容
	originalOverflowCount := strings.Count(htmlContent, "overflow: visible")
	log.C(ctx).Infow("原始HTML内容检查", "card_id", cardID, "overflow_visible_count", originalOverflowCount)

	// 修复HTML内容中的CSS样式
	fixedHTMLContent := p.fixHTMLContentForRendering(htmlContent)

	// 检查修复后的HTML内容
	fixedOverflowCount := strings.Count(fixedHTMLContent, "overflow: visible !important")
	log.C(ctx).Infow("修复后HTML内容检查", "card_id", cardID, "overflow_visible_count", fixedOverflowCount)

	// 保存修复后的HTML文件用于调试
	debugHTMLPath := strings.Replace(fullImagePath, ".webp", "_fixed.html", 1)
	if err := os.WriteFile(debugHTMLPath, []byte(fixedHTMLContent), 0644); err != nil {
		log.C(ctx).Warnw("保存调试HTML文件失败", "card_id", cardID, "error", err.Error())
	} else {
		log.C(ctx).Infow("调试HTML文件已保存", "card_id", cardID, "debug_path", debugHTMLPath)
	}

	// 使用新的wkhtmltoimage工具
	rendererConfig := p.getRendererConfig()
	renderer := utilpkg.NewWkhtmltoimageRenderer(rendererConfig)

	// 添加重试机制（容器环境可能资源紧张）
	maxRetries := 3
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := renderer.RenderHTMLToImage(ctx, fixedHTMLContent, fullImagePath)
		if err == nil {
			// 渲染成功
			log.C(ctx).Infow("wkhtmltoimage渲染成功", "card_id", cardID, "image_path", fullImagePath, "attempt", attempt)
			return fullImagePath, nil
		}

		lastErr = err
		log.C(ctx).Warnw("wkhtmltoimage转换失败",
			"card_id", cardID,
			"attempt", attempt,
			"max_retries", maxRetries,
			"error", err.Error())

		// 如果不是最后一次尝试，等待后重试
		if attempt < maxRetries {
			// 检查是否是可重试的错误
			errStr := err.Error()
			isRetryable := strings.Contains(errStr, "fork") ||
				strings.Contains(errStr, "Resource temporarily unavailable") ||
				strings.Contains(errStr, "failed to start") ||
				strings.Contains(errStr, "context deadline exceeded")

			if !isRetryable {
				// 不可重试的错误，直接返回
				log.C(ctx).Warnw("遇到不可重试的错误，停止重试", "card_id", cardID, "error", errStr)
				return "", fmt.Errorf("wkhtmltoimage转换失败: %v", err)
			}

			// 等待后重试（指数退避：2s, 4s）
			waitTime := time.Duration(attempt) * 2 * time.Second
			log.C(ctx).Infow("等待后重试", "card_id", cardID, "wait_seconds", waitTime.Seconds())
			time.Sleep(waitTime)
		}
	}

	// 所有重试都失败
	return "", fmt.Errorf("wkhtmltoimage转换失败，已重试%d次: %v", maxRetries, lastErr)
}

// fixHTMLContentForRendering 修复HTML内容以适配渲染
func (p *AsyncBookProcessor) fixHTMLContentForRendering(htmlContent string) string {
	// 保持overflow: visible，不强制改为hidden，解决底部文字被遮盖问题
	// htmlContent = strings.ReplaceAll(htmlContent, "overflow: visible;", "overflow: hidden !important;")
	// htmlContent = strings.ReplaceAll(htmlContent, "overflow: visible", "overflow: hidden !important")

	// 添加固定尺寸的CSS，但保持overflow: visible以显示完整内容
	rendererConfig := p.getRendererConfig()
	fixedCSS := fmt.Sprintf(`
		body { 
			width: %dpx !important; 
			height: %dpx !important; 
			overflow: visible !important; 
		}
		.markdown-card-container { 
			width: %dpx !important; 
			height: %dpx !important; 
			overflow: visible !important; 
		}
		.markdown-content { 
			overflow: visible !important; 
		}
	`, rendererConfig.Width, rendererConfig.Height, rendererConfig.Width, rendererConfig.Height)

	// 在</style>标签前插入修复的CSS
	htmlContent = strings.Replace(htmlContent, "</style>", fixedCSS+"\n</style>", 1)
	// 如果没有style标签，在head标签内添加
	htmlContent = strings.Replace(htmlContent, "</head>", "<style>"+fixedCSS+"</style>\n</head>", 1)

	return htmlContent
}

// renderWithAlternativeMethod 使用备选方案渲染
func (p *AsyncBookProcessor) renderWithAlternativeMethod(ctx context.Context, cardID uint, htmlContent, fullImagePath string) (string, error) {
	log.C(ctx).Infow("使用备选方案渲染", "card_id", cardID)

	// 备选方案1: 使用chromedp（如果可用）
	if p.isChromedpAvailable() {
		return p.renderWithChromedp(ctx, cardID, htmlContent, fullImagePath)
	}

	// 备选方案2: 创建占位符图片
	log.C(ctx).Infow("使用占位符图片方案", "card_id", cardID)
	return p.createPlaceholderImage(ctx, cardID, fullImagePath)
}

// isChromedpAvailable 检查chromedp是否可用
func (p *AsyncBookProcessor) isChromedpAvailable() bool {
	// 检查Chrome是否可用
	cmd := exec.Command("google-chrome", "--version")
	if err := cmd.Run(); err != nil {
		// 尝试其他Chrome路径
		cmd = exec.Command("chromium", "--version")
		if err := cmd.Run(); err != nil {
			cmd = exec.Command("chrome", "--version")
			if err := cmd.Run(); err != nil {
				return false
			}
		}
	}
	return true
}

// renderWithChromedp 使用chromedp渲染（简化版）
func (p *AsyncBookProcessor) renderWithChromedp(ctx context.Context, cardID uint, htmlContent, fullImagePath string) (string, error) {
	log.C(ctx).Infow("使用chromedp渲染", "card_id", cardID)

	// 这里可以集成chromedp的完整实现
	// 暂时返回占位符，实际项目中应该实现完整的chromedp渲染
	return p.createPlaceholderImage(ctx, cardID, fullImagePath)
}

// createPlaceholderImage 创建占位符图片
func (p *AsyncBookProcessor) createPlaceholderImage(ctx context.Context, cardID uint, imagePath string) (string, error) {
	log.C(ctx).Infow("创建占位符图片", "card_id", cardID, "image_path", imagePath)

	// 确保目录存在
	dir := filepath.Dir(imagePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("创建图片目录失败: %v", err)
	}

	// 创建一个简单的HTML占位符，然后使用wkhtmltoimage转换
	placeholderHTML := fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <title>卡片 %d 占位符</title>
    <style>
        body {
            margin: 0;
            padding: 0;
            width: 1080px;
            height: 1440px;
            background: linear-gradient(135deg, #f5f7fa 0%%, #c3cfe2 100%%);
            display: flex;
            align-items: center;
            justify-content: center;
            font-family: 'Microsoft YaHei', Arial, sans-serif;
        }
        .placeholder {
            text-align: center;
            color: #666;
        }
        .placeholder-icon {
            font-size: 48px;
            margin-bottom: 20px;
        }
        .placeholder-text {
            font-size: 24px;
            margin-bottom: 10px;
        }
        .placeholder-subtext {
            font-size: 16px;
            opacity: 0.7;
        }
    </style>
</head>
<body>
    <div class="placeholder">
        <div class="placeholder-icon">📄</div>
        <div class="placeholder-text">卡片 %d</div>
        <div class="placeholder-subtext">图片生成中...</div>
    </div>
</body>
</html>`, cardID, cardID)

	// 创建临时HTML文件
	tempHTMLFile := filepath.Join(dir, "placeholder.html")
	if err := os.WriteFile(tempHTMLFile, []byte(placeholderHTML), 0644); err != nil {
		return "", fmt.Errorf("创建占位符HTML文件失败: %v", err)
	}
	defer os.Remove(tempHTMLFile) // 清理临时文件

	// 使用内部的wkhtmltoimage工具生成占位符图片
	rendererConfig := p.getRendererConfig()
	renderer := utilpkg.NewWkhtmltoimageRenderer(rendererConfig)

	if err := renderer.RenderHTMLToImage(ctx, placeholderHTML, imagePath); err != nil {
		log.C(ctx).Warnw("占位符图片生成失败", "card_id", cardID, "error", err.Error())
		// 如果内部工具也失败，创建一个简单的文本文件
		placeholderContent := fmt.Sprintf("Placeholder for card %d", cardID)
		if err := os.WriteFile(imagePath, []byte(placeholderContent), 0644); err != nil {
			return "", fmt.Errorf("创建占位符文件失败: %v", err)
		}
	}

	return imagePath, nil
}

// generateCardHTML 生成卡片的HTML内容
func (p *AsyncBookProcessor) generateCardHTML(cardID uint, content string) string {
	// 简单的HTML模板
	htmlTemplate := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>卡片 %d</title>
    <style>
        body {
            font-family: 'Microsoft YaHei', Arial, sans-serif;
            margin: 0;
            padding: 20px;
            background-color: #f5f5f5;
        }
        .card {
            max-width: 800px;
            margin: 0 auto;
            background: white;
            border-radius: 10px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
            padding: 30px;
        }
        .content {
            line-height: 1.6;
            color: #333;
            font-size: 16px;
        }
        .timestamp {
            color: #666;
            font-size: 12px;
            text-align: center;
            margin-top: 20px;
        }
    </style>
</head>
<body>
    <div class="card">
        <div class="content">
            %s
        </div>
        <div class="timestamp">
            生成时间: %s
        </div>
    </div>
</body>
</html>`

	// 转义HTML内容
	escapedContent := strings.ReplaceAll(content, "<", "&lt;")
	escapedContent = strings.ReplaceAll(escapedContent, ">", "&gt;")
	escapedContent = strings.ReplaceAll(escapedContent, "\n", "<br>")

	return fmt.Sprintf(htmlTemplate, cardID, escapedContent, time.Now().Format("2006-01-02 15:04:05"))
}

// generateCardImagePrompt 为卡片内容生成图片提示词
func (p *AsyncBookProcessor) generateCardImagePrompt(content string) string {
	// 简单的提示词生成逻辑
	// 可以根据内容类型生成不同的提示词

	// 如果内容包含特定关键词，生成相应的图片提示词
	if strings.Contains(content, "技术") || strings.Contains(content, "科技") {
		return "现代科技办公环境，电脑屏幕显示代码，高科技感，蓝色调，专业商务风格"
	} else if strings.Contains(content, "学习") || strings.Contains(content, "教育") {
		return "温馨的学习环境，书本、笔记本、笔，温暖的灯光，知识氛围浓厚"
	} else if strings.Contains(content, "思考") || strings.Contains(content, "思维") {
		return "抽象思维概念图，大脑、灯泡、连接线，创意灵感迸发，现代简约风格"
	} else if strings.Contains(content, "未来") || strings.Contains(content, "创新") {
		return "未来科技城市，人工智能与人类协作，数字化世界，科幻风格"
	} else {
		// 默认提示词
		return "现代简约办公环境，专业商务风格，蓝色调，高质量渲染"
	}
}

// parseMarkdownResponse 解析AI返回的markdown格式响应
func (p *AsyncBookProcessor) parseMarkdownResponse(response string) (string, string) {
	// 尝试解析JSON格式的响应（如果AI返回的是JSON）
	var jsonResponse struct {
		Text        string `json:"text"`
		ImagePrompt string `json:"image_prompt"`
	}

	if err := json.Unmarshal([]byte(response), &jsonResponse); err == nil {
		// 成功解析JSON，返回解析结果
		return jsonResponse.Text, jsonResponse.ImagePrompt
	}

	// 如果不是JSON格式，尝试从markdown中提取图片提示词
	imagePrompt := p.extractImagePromptFromMarkdown(response)

	// 如果没有找到图片提示词，使用默认的提示词生成逻辑
	if imagePrompt == "" {
		imagePrompt = p.generateDefaultImagePrompt(response)
	}

	// 返回markdown内容和图片提示词
	return response, imagePrompt
}

// extractImagePromptFromMarkdown 从markdown中提取图片提示词
func (p *AsyncBookProcessor) extractImagePromptFromMarkdown(markdown string) string {
	// 查找可能的图片提示词标记
	patterns := []string{
		"图片提示词[:：]\\s*(.+)",
		"image_prompt[:：]\\s*(.+)",
		"图片描述[:：]\\s*(.+)",
		"<!--\\s*image_prompt[:：]\\s*(.+?)\\s*-->",
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(markdown)
		if len(matches) > 1 {
			return strings.TrimSpace(matches[1])
		}
	}

	return ""
}

// formatBackgroundStyle 将背景图路径转为内联 CSS 样式，支持 http(s)、data、本地绝对/相对路径
func formatBackgroundStyle(background string) string {
	if strings.TrimSpace(background) == "" {
		return ""
	}
	src := background
	lower := strings.ToLower(background)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:") {
		// remote or data url
		src = background
	} else if filepath.IsAbs(background) {
		src = "file://" + background
	} else {
		if absPath, err := filepath.Abs(background); err == nil {
			src = "file://" + absPath
		}
	}
	// 背景图居中、cover 铺满
	return fmt.Sprintf("background: url('%s') center center / cover no-repeat;", src)
}

// generateDefaultImagePrompt 生成默认的图片提示词
func (p *AsyncBookProcessor) generateDefaultImagePrompt(content string) string {
	// 简单的提示词生成逻辑
	if strings.Contains(content, "技术") || strings.Contains(content, "科技") {
		return "现代科技办公环境，电脑屏幕显示代码，高科技感，蓝色调，专业商务风格"
	} else if strings.Contains(content, "学习") || strings.Contains(content, "教育") {
		return "温馨的学习环境，书本、笔记本、笔，温暖的灯光，知识氛围浓厚"
	} else if strings.Contains(content, "思考") || strings.Contains(content, "思维") {
		return "抽象思维概念图，大脑、灯泡、连接线，创意灵感迸发，现代简约风格"
	} else if strings.Contains(content, "未来") || strings.Contains(content, "创新") {
		return "未来科技城市，人工智能与人类协作，数字化世界，科幻风格"
	} else {
		// 默认提示词
		return "现代简约办公环境，专业商务风格，蓝色调，高质量渲染"
	}
}

// generateCoverHTML 生成封面HTML内容（背景图在底层，图片和标题在上层）
func (p *AsyncBookProcessor) generateCoverHTML(title, imageURL, background string, bookID uint) string {
	// 处理无标题的情况
	titleContent := ""
	if title == "" {
		// 无标题时，显示一个占位符
		titleContent = `<div class="title-text" style="opacity: 0.5; font-style: italic;">无标题笔记</div>`
	} else {
		titleContent = fmt.Sprintf(`<h1 class="title-text">%s</h1>`, title)
	}

	// 获取封面标题配置
	fontSize, lineHeight, color := getCoverTitleConfig()

	// 获取字体路径（根据环境动态调整）
	regularFontPath, boldFontPath := getFontPaths()

	// 处理背景样式 - 优先使用模板背景，如果没有则使用默认模板背景
	var backgroundStyle string
	if background != "" {
		// 使用模板背景图片，覆盖整个卡片 - 使用正确的URL处理逻辑
		backgroundStyle = formatBackgroundStyle(background)
		log.C(context.Background()).Infow("使用指定模板背景", "background", background)
	} else if imageURL != "" && imageURL != "null" && imageURL != "undefined" {
		// 构建完整的图片路径
		imagePath := viper.GetString("resource.image_path")
		if imagePath == "" {
			imagePath = "res/upload" // 默认路径
		}

		// 如果imageURL是相对路径，转换为完整路径
		var fullImageURL string
		if strings.HasPrefix(imageURL, "/") {
			// 移除开头的斜杠，构建完整路径
			fullImageURL = "file://" + filepath.Join(imagePath, strings.TrimPrefix(imageURL, "/"))
		} else {
			// 已经是完整路径或相对路径
			fullImageURL = imageURL
		}

		backgroundStyle = fmt.Sprintf("background: url('%s') center center / cover no-repeat;", fullImageURL)

		// 添加调试日志
		log.C(context.Background()).Infow("使用book图片作为背景",
			"original_url", imageURL,
			"image_path", imagePath,
			"full_url", fullImageURL)
	} else {
		// 使用默认模板背景 - 从数据库获取ID为1的模板
		defaultTemplateURL := ""
		if defaultTemplate, err := p.biz.Templates().GetByID(context.Background(), 1); err == nil && defaultTemplate != nil && defaultTemplate.File != "" {
			defaultTemplateURL = defaultTemplate.File
			log.C(context.Background()).Infow("使用数据库默认模板", "template_id", 1, "file", defaultTemplateURL)
		} else {
			// 如果数据库获取失败，使用本地默认路径
			defaultTemplatePath := "res/template/default.webp"
			currentDir, _ := os.Getwd()
			fullDefaultPath := filepath.Join(currentDir, defaultTemplatePath)
			defaultTemplateURL = "file://" + fullDefaultPath
			log.C(context.Background()).Infow("数据库模板获取失败，使用本地默认模板", "local_path", fullDefaultPath, "error", err)
		}
		backgroundStyle = fmt.Sprintf("background: url('%s') center center / cover no-repeat;", defaultTemplateURL)
	}

	// 处理book图片 - 已注释，不需要图片
	// var imageHTML string
	// imagePath := viper.GetString("resource.image_path")
	// if imagePath == "" {
	// 	imagePath = "res/upload" // 默认路径
	// }

	// // 构建book图片路径
	// bookImagePath := filepath.Join(imagePath, "book", fmt.Sprintf("%d", bookID), fmt.Sprintf("book_%d.webp", bookID))
	// fullBookImagePath := fmt.Sprintf("file://%s", bookImagePath)

	// // 检查book图片文件是否存在
	// if _, err := os.Stat(bookImagePath); err == nil {
	// 	// 文件存在，使用实际图片
	// 	imageHTML = fmt.Sprintf(`<img src="%s" class="cover-image" alt="封面图片" onerror="this.style.display='none'; this.nextElementSibling.style.display='flex';">
	//             <div class="cover-image-placeholder" style="display: none;">
	//                 <div class="placeholder-icon">🖼️</div>
	//                 <div class="placeholder-text">封面图片</div>
	//             </div>`, fullBookImagePath)
	// 	log.C(context.Background()).Infow("Book图片文件存在，使用实际图片", "book_id", bookID, "image_path", fullBookImagePath)
	// } else {
	// 	// 文件不存在，使用占位符
	// 	imageHTML = `<div class="cover-image-placeholder">
	//             <div class="placeholder-icon">🖼️</div>
	//             <div class="placeholder-text">封面图片</div>
	//         </div>`
	// 	log.C(context.Background()).Warnw("Book图片文件不存在，使用占位符", "book_id", bookID, "expected_path", bookImagePath, "error", err)
	// }

	// 跳过图片处理，直接使用空字符串
	log.C(context.Background()).Infow("⚠️ 跳过图片处理，封面只显示标题", "book_id", bookID)

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>封面 - %s</title>
    <style>
        /* 思源宋体字体定义 - 环境自适应 */
        @font-face {
            font-family: "SourceHanSerifSC";
            src: url("%s") format("opentype"),
                 local("Source Han Serif SC"),
                 local("SourceHanSerifSC"),
                 local("STFangsong"),
                 local("Source Han Sans CN"),
                 local("Noto Sans CJK SC"),
                 local("PingFang SC"),
                 local("Hiragino Sans GB"),
                 local("Microsoft YaHei"),
                 local("sans-serif");
            font-weight: normal;
            font-style: normal;
        }

        @font-face {
            font-family: "SourceHanSerifSC";
            src: url("%s") format("opentype"),
                 local("Source Han Serif SC Bold"),
                 local("SourceHanSerifSC-Bold"),
                 local("STFangsong"),
                 local("Source Han Sans CN Bold"),
                 local("Noto Sans CJK SC Semibold"),
                 local("PingFang SC"),
                 local("Hiragino Sans GB"),
                 local("Microsoft YaHei Bold"),
                 local("sans-serif");
            font-weight: 700;
            font-style: normal;
        }

        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }

        body {
            font-family: "SourceHanSerifSC", "STFangsong", "Noto Sans CJK SC", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Helvetica Neue", Arial, sans-serif;
            width: 1080px;
            height: 1440px;
            overflow: visible !important;
            %s
            position: relative;
        }

        /* 封面容器 - 背景图在底层，内容在上层 */
        .cover-container {
            width: 100%%;
            height: 100%%;
            position: relative;
            %s
            background-size: cover !important;
            background-position: center center !important;
            background-repeat: no-repeat !important;
        }

        /* 背景层：背景图在最后一层 */
        .cover-background-layer {
            position: absolute;
            top: 0;
            left: 0;
            width: 100%%;
            height: 100%%;
            z-index: 1;
            background: inherit;
            background-size: cover !important;
            background-position: center center !important;
            background-repeat: no-repeat !important;
        }

        /* 内容层：只显示标题 */
        .cover-content-layer {
            position: relative;
            width: 100%%;
            height: 100%%;
            z-index: 2;
            display: flex;
            align-items: center;
            justify-content: center;
        }

        /* 标题区域 - 完全居中 */
        .title-section {
            display: flex;
            align-items: center;
            justify-content: center;
            position: relative;
            width: 100%%;
            height: 100%%;
        }

        .title-content {
            text-align: justify;
            max-width: 800px;
            padding: 40px;
        }

        .title-text {
            font-family: "SourceHanSerifSC", "STFangsong", "Noto Sans CJK SC", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Helvetica Neue", Arial, sans-serif;
            font-size: %dpx;
            font-weight: 700;
            color: %s;
            line-height: %dpx;
            margin-bottom: 20px;
            text-shadow: none;
        }

        .subtitle {
            font-size: 24px;
            color: #7f8c8d;
            font-weight: 400;
            line-height: 1.4;
        }

        .decoration {
            display: none;
        }

        @media (max-width: 1080px) {
            body {
                width: 100vw;
                height: 100vh;
            }
        }
    </style>
</head>
<body>
    <div class="cover-container">
        <!-- 背景层：背景图在最后一层 -->
        <div class="cover-background-layer"></div>
        
        <!-- 内容层：只显示标题 -->
        <div class="cover-content-layer">
            <div class="title-section">
                <div class="title-content">
                    %s
                </div>
            </div>
        </div>
    </div>
</body>
</html>`, title, regularFontPath, boldFontPath, backgroundStyle, backgroundStyle, fontSize, color, lineHeight, titleContent)
}

// generateCoverImageOnly 仅生成封面图片，不重新生成HTML
func (p *AsyncBookProcessor) generateCoverImageOnly(ctx context.Context, cardID uint, coverHTML string) error {
	log.C(ctx).Infow("开始生成封面图片", "card_id", cardID)

	// 获取图片输出路径配置
	imagePath := viper.GetString("resource.image_path")
	if imagePath == "" {
		return fmt.Errorf("未配置resource.image_path")
	}

	// 创建卡片图片目录
	cardImageDir := filepath.Join(imagePath, "card", fmt.Sprintf("%d", cardID))
	if err := os.MkdirAll(cardImageDir, 0755); err != nil {
		return fmt.Errorf("创建卡片图片目录失败: %v", err)
	}

	// 生成图片文件名和完整路径
	imageFileName := fmt.Sprintf("card_%d.webp", cardID)
	fullImagePath := filepath.Join(cardImageDir, imageFileName)

	// 保存封面HTML文件
	htmlFilePath := filepath.Join(cardImageDir, fmt.Sprintf("card_%d.html", cardID))
	if err := os.WriteFile(htmlFilePath, []byte(coverHTML), 0644); err != nil {
		return fmt.Errorf("保存封面HTML文件失败: %v", err)
	}

	log.C(ctx).Infow("封面HTML文件保存成功", "card_id", cardID, "html_path", htmlFilePath)

	// 使用修复后的HTML内容进行渲染
	_, err := p.renderWithWkhtmltoimage(ctx, cardID, coverHTML, fullImagePath)
	if err != nil {
		return fmt.Errorf("封面图片渲染失败: %v", err)
	}

	// 更新数据库中的RenderedImage字段
	relativeImagePath := fmt.Sprintf("%s/card/%d/%s", imagePath, cardID, imageFileName)

	// 上传封面图片到COS
	if util.IsCOSEnabled() && fullImagePath != "" {
		// 读取生成的图片文件
		if imageData, err := os.ReadFile(fullImagePath); err == nil {
			// 构建COS对象键：card/{card_id}/card_{card_id}.webp
			objectKey := fmt.Sprintf("card/%d/card_%d.webp", cardID, cardID)

			// 上传到COS
			cosURL, uploadErr := util.UploadBytesToCOS(ctx, objectKey, "image/webp", imageData)
			if uploadErr != nil {
				log.C(ctx).Warnw("上传封面图片到COS失败", "card_id", cardID, "error", uploadErr.Error())
			} else if cosURL != "" {
				log.C(ctx).Infow("✅ 封面图片已上传到COS", "card_id", cardID, "cos_url", cosURL)

				// 生成签名URL（可选，如果需要的话）
				if signedURL, err := util.GenerateSignedURL(ctx, objectKey, 600); err == nil && signedURL != "" {
					log.C(ctx).Infow("封面COS签名URL生成成功", "card_id", cardID, "signed_url", signedURL)
				}
			}
		} else {
			log.C(ctx).Warnw("读取封面图片文件失败", "card_id", cardID, "path", fullImagePath, "error", err.Error())
		}
	}

	// 获取卡片记录并更新
	card, err := p.biz.Cards().GetByID(ctx, cardID)
	if err != nil {
		log.C(ctx).Errorw("获取卡片记录失败", "card_id", cardID, "error", err.Error())
		return fmt.Errorf("获取卡片记录失败: %v", err)
	}

	card.RenderedImage = relativeImagePath
	if err := p.biz.Cards().Update(ctx, card); err != nil {
		log.C(ctx).Errorw("更新卡片RenderedImage字段失败", "card_id", cardID, "error", err.Error())
		return fmt.Errorf("更新卡片RenderedImage字段失败: %v", err)
	}

	log.C(ctx).Infow("✅ 封面图片生成完成并更新数据库", "card_id", cardID, "image_path", relativeImagePath)
	return nil
}

// getRendererConfig 获取渲染器配置
func (p *AsyncBookProcessor) getRendererConfig() *utilpkg.WkhtmltoimageConfig {
	// 使用统一的配置读取
	cardConfig := util.GetCardRenderingConfig()
	timeout := time.Duration(cardConfig.TimeoutSeconds) * time.Second

	return &utilpkg.WkhtmltoimageConfig{
		Width:   cardConfig.Width,
		Height:  cardConfig.Height,
		Quality: cardConfig.Quality,
		Format:  cardConfig.Format,
		Zoom:    cardConfig.Zoom,
		Timeout: timeout,
	}
}
