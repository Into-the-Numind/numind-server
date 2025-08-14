package book

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/httpclient"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// AsyncBookProcessor 异步book处理器
type AsyncBookProcessor struct {
	biz BizInterface
}

// BizInterface 业务接口
type BizInterface interface {
	Books() AsyncBookBiz
	Cards() AsyncCardBiz
	Users() AsyncUserBiz
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
}

// AsyncUserBiz 用户业务接口
type AsyncUserBiz interface {
	IncrementUserBookNum(ctx context.Context, userID uint) error
	IncrementUserCardNum(ctx context.Context, userID uint) error
}

// AsyncAliBiz 阿里业务接口
type AsyncAliBiz interface {
	QianwenTextStream(messages []map[string]string, maxTokens int, temperature float64) (string, error)
	WanxiangImageAsync(prompt, style, size string) (string, error)
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

// CreateBookAsync 异步创建book
func (p *AsyncBookProcessor) CreateBookAsync(ctx context.Context, userID uint, text, templateID string) (*model.BookM, error) {
	// 立即创建book记录，状态为creating
	now := time.Now()
	book := &model.BookM{
		UserID:     userID,
		Title:      fmt.Sprintf("AI生成卡册 - %s", now.Format("2006-01-02 15:04:05")),
		TemplateID: templateID,
		ViewTime:   &now,
		Status:     model.BookStatusCreating,
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

	// 在后台异步处理book创建
	go func() {
		p.processBookCreationInBackground(ctx, book.ID, userID, text, templateID)
	}()

	return book, nil
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

	// 调用火山引擎文字模型处理文本（替换原来的千问）
	prompt := p.biz.Ali().GetPromptManager().GetTextProcessingPrompt() + "\n\n" + text

	messages := []map[string]string{
		{"role": "user", "content": prompt},
	}

	// 首先尝试调用volc
	volcResult, err := p.biz.Volc().VolcTextStream(ctx, messages, 1024, 0.5)
	if err != nil {
		log.C(ctx).Warnw("VolcTextStream failed, falling back to Qianwen", "book_id", bookID, "error", err.Error())

		// Fallback到qianwen
		qianwenResult, err := p.biz.Ali().QianwenTextStream(messages, 1024, 0.5)
		if err != nil {
			log.C(ctx).Errorw("Both Volc and Qianwen failed", "book_id", bookID, "volc_error", err.Error(), "qianwen_error", err.Error())
			p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "AI text processing failed: both volc and qianwen failed")
			return
		}

		log.C(ctx).Infow("QianwenTextStream fallback result", "book_id", bookID, "result", qianwenResult)
		volcResult = qianwenResult
	} else {
		log.C(ctx).Infow("VolcTextStream result", "book_id", bookID, "result", volcResult)
	}

	// 提取JSON内容（处理可能的Markdown代码块格式）
	jsonContent := extractJSONFromResponse(volcResult)
	log.C(ctx).Infow("Extracted JSON content", "book_id", bookID, "content", jsonContent)

	// 检查提取的JSON是否为空
	if jsonContent == "" {
		log.C(ctx).Errorw("Failed to extract JSON content from Volc response",
			"book_id", bookID,
			"volc_response_length", len(volcResult),
			"volc_response_preview", volcResult[:min(len(volcResult), 200)])

		// 尝试重试机制：如果JSON提取失败，可能是API响应不完整
		log.C(ctx).Infow("Attempting retry mechanism for incomplete JSON response", "book_id", bookID)

		// 重试一次，使用更激进的JSON修复策略
		retryContent := extractJSONWithRetry(volcResult)
		if retryContent != "" {
			log.C(ctx).Infow("Retry successful, extracted JSON content", "book_id", bookID, "content", retryContent)
			jsonContent = retryContent
		} else {
			log.C(ctx).Errorw("Retry failed, both attempts failed to extract JSON",
				"book_id", bookID,
				"volc_response_length", len(volcResult))
			p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Failed to extract JSON content from AI response after retry")
			return
		}
	}

	// 解析火山引擎返回的JSON结果
	var volcResponse QianwenResponse
	if err := json.Unmarshal([]byte(jsonContent), &volcResponse); err != nil {
		log.C(ctx).Errorw("Failed to parse Volc response",
			"book_id", bookID,
			"error", err.Error(),
			"json_content_length", len(jsonContent),
			"json_content_preview", jsonContent[:min(len(jsonContent), 200)])
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Failed to parse AI response: "+err.Error())
		return
	}

	// 验证解析后的数据结构
	if volcResponse.StructuredTextArray == nil || len(volcResponse.StructuredTextArray) == 0 {
		log.C(ctx).Errorw("Volc response has no structured text array",
			"book_id", bookID,
			"response", volcResponse)
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "AI response has no structured content")
		return
	}

	// 使用分页引擎处理文本
	paginationBiz := pagination.NewPaginationBiz()

	// 提取title作为book的标题
	var bookTitle string
	for _, item := range volcResponse.StructuredTextArray {
		if item.Type == "title" {
			if titleContent, ok := item.Content.(string); ok {
				bookTitle = titleContent
				break
			}
		}
	}

	// 如果没有找到title，使用默认标题
	if bookTitle == "" {
		bookTitle = fmt.Sprintf("AI生成卡册 - %s", time.Now().Format("2006-01-02 15:04:05"))
	}

	// 使用解析出的image_prompt调用万相生成图片
	var imageUrl string
	if volcResponse.ImagePrompt != "" {
		// 直接使用原始提示词调用万相API
		remoteImageUrl, err := p.biz.Ali().WanxiangImageAsync(volcResponse.ImagePrompt, "", "1024*1024")
		if err != nil {
			log.C(ctx).Errorw("WanxiangImageAsync failed", "book_id", bookID, "error", err.Error())
			// 图片生成失败不影响整体流程
		} else {
			// 下载并保存图片到本地
			localImagePath, err := downloadAndSaveImage(remoteImageUrl, bookID)
			if err != nil {
				log.C(ctx).Errorw("Failed to download and save image", "book_id", bookID, "error", err.Error())
			} else {
				imageUrl = localImagePath
			}
		}
	}

	// 更新book记录
	book.Title = bookTitle
	book.ImageUrl = imageUrl
	if err := p.biz.Books().Update(ctx, book); err != nil {
		log.C(ctx).Errorw("Failed to update book with title and image", "book_id", bookID, "error", err.Error())
	}

	// 将结构化文本转换为分页元素，排除title类型
	var elements []pagination.Element
	
	// 检查是否有结构化内容
	if volcResponse.StructuredTextArray != nil && len(volcResponse.StructuredTextArray) > 0 {
		log.C(ctx).Infow("使用AI返回的结构化内容", "book_id", bookID, "element_count", len(volcResponse.StructuredTextArray))
		
		for _, item := range volcResponse.StructuredTextArray {
			if item.Type == "title" {
				continue // 跳过title类型
			}

			// 根据类型映射到分页引擎的元素类型
			var elementType pagination.ElementType
			switch item.Type {
			case "body":
				elementType = pagination.ElementTypeBody
			case "subtitle":
				elementType = pagination.ElementTypeSubtitle
			case "list":
				elementType = pagination.ElementTypeList
			case "quote":
				elementType = pagination.ElementTypeQuote
			default:
				elementType = pagination.ElementTypeBody // 默认使用body类型
			}

			// 处理content内容
			var content interface{}
			switch v := item.Content.(type) {
			case string:
				content = v
			case []interface{}:
				// 如果是列表，保持为字符串数组格式
				var listItems []string
				for _, listItem := range v {
					if str, ok := listItem.(string); ok {
						listItems = append(listItems, str)
					}
				}
				content = listItems
			default:
				content = fmt.Sprintf("%v", v)
			}

			elements = append(elements, pagination.Element{
				Type:    elementType,
				Content: content,
			})
		}
	} else {
		// 如果没有结构化内容，将原始文本按段落分割并转换为body元素
		log.C(ctx).Infow("AI未返回结构化内容，使用原始文本作为后备方案", "book_id", bookID, "original_text_length", len(text))
		
		// 按段落分割文本
		paragraphs := strings.Split(text, "\n\n")
		for _, paragraph := range paragraphs {
			paragraph = strings.TrimSpace(paragraph)
			if paragraph != "" {
				elements = append(elements, pagination.Element{
					Type:    pagination.ElementTypeBody,
					Content: paragraph,
				})
			}
		}
		
		// 如果段落分割后仍然没有内容，将整个文本作为一个元素
		if len(elements) == 0 {
			elements = append(elements, pagination.Element{
				Type:    pagination.ElementTypeBody,
				Content: text,
			})
		}
	}

	paginatedContent, err := paginationBiz.PaginateElements(elements)
	if err != nil {
		log.C(ctx).Errorw("Pagination failed", "book_id", bookID, "error", err.Error())
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Failed to paginate content: "+err.Error())
		return
	}

	// 根据配置创建渲染器
	var renderer card.RendererInterface
	var useRenderAndMeasure bool

	// 检查配置是否启用渲染-测量方案
	if card.IsRenderAndMeasureEnabled() {
		// 优先使用渲染-测量方案
		renderAndMeasureRenderer := card.NewRenderAndMeasureRenderer(paginationBiz.GetConfig())
		if renderAndMeasureRenderer != nil {
			renderer = renderAndMeasureRenderer
			useRenderAndMeasure = true
			log.C(ctx).Infow("使用渲染-测量方案", "book_id", bookID)
		} else {
			log.C(ctx).Warnw("渲染-测量渲染器创建失败，降级到传统渲染器", "book_id", bookID)
			useRenderAndMeasure = false
		}
	} else {
		log.C(ctx).Infow("配置禁用渲染-测量方案，使用传统渲染器", "book_id", bookID)
		useRenderAndMeasure = false
	}

	// 如果渲染-测量方案不可用，使用传统渲染器
	if !useRenderAndMeasure {
		renderer = card.NewSimpleHeadlessRenderer(paginationBiz.GetConfig())

		// 若有模板背景图，设置给传统渲染器
		if templateID != "" {
			if tid, err := strconv.ParseUint(templateID, 10, 64); err == nil {
				if tmpl, err := p.biz.Templates().GetByID(ctx, uint(tid)); err == nil && tmpl != nil {
					if simpleRenderer, ok := renderer.(*card.SimpleHeadlessRenderer); ok {
						simpleRenderer.SetBackground(tmpl.File)
					}
				}
			}
		}
	}

	coverRenderer := card.NewCoverRenderer(paginationBiz.GetConfig())

	// 首先创建封面卡片 (sort_order = 0)
	// 背景图：如果传入了 template_id，则尝试从模板中读取背景图绝对路径
	var coverBackground string
	if templateID != "" {
		if tid, err := strconv.ParseUint(templateID, 10, 64); err == nil {
			if tmpl, err := p.biz.Templates().GetByID(ctx, uint(tid)); err == nil && tmpl != nil {
				// 这里假设 template.File 字段保存的是背景图的绝对路径
				coverBackground = tmpl.File
				log.C(ctx).Infow("获取到模板背景图", "book_id", bookID, "template_id", templateID, "background_path", coverBackground)
			}
		}
	}

	// 总是创建封面卡片，即使没有图片或背景
	// 创建封面卡片记录
	coverCardRecord := &model.CardM{
		UserID:    userID,
		BookID:    book.ID,
		SortOrder: 0, // 封面卡片排序为0
	}

	// 先创建封面卡片记录
	if err := p.biz.Cards().Create(ctx, coverCardRecord); err != nil {
		log.C(ctx).Errorw("Failed to create cover card", "book_id", bookID, "error", err.Error())
	} else {
		// 构造封面数据（包含可选的背景图）
		var coverElements []map[string]interface{}
		coverElements = append(coverElements, map[string]interface{}{"type": "title", "content": bookTitle})
		if book.ImageUrl != "" {
			coverElements = append(coverElements, map[string]interface{}{"type": "image", "content": book.ImageUrl})
		}
		if coverBackground != "" {
			coverElements = append(coverElements, map[string]interface{}{"type": "background", "content": coverBackground})
		}
		if b, err := json.Marshal(coverElements); err == nil {
			coverCardRecord.ProcessedText = string(b)
		}

		// 渲染封面卡片为图片（支持背景图）
		renderedCoverCard, err := coverRenderer.RenderCoverCardToImage(coverCardRecord)
		if err != nil {
			log.C(ctx).Errorw("Failed to render cover card to image", "book_id", bookID, "error", err.Error())
		} else {
			// 更新封面卡片记录，保存渲染后的图片URL
			coverCardRecord.RenderedImage = renderedCoverCard.ImageURL
			if err := p.biz.Cards().Update(ctx, coverCardRecord); err != nil {
				log.C(ctx).Errorw("Failed to update cover card with rendered image", "book_id", bookID, "error", err.Error())
			}

			// 使用封面卡片的rendered_image更新book的image_url
			// 业务要求：book.image_url 取该book对应、processed_text为null的卡片（封面卡）rendered_image
			book.ImageUrl = coverCardRecord.RenderedImage
			if err := p.biz.Books().Update(ctx, book); err != nil {
				log.C(ctx).Errorw("Failed to update book image_url from cover card rendered image", "book_id", bookID, "error", err.Error())
			}
		}

		// 更新用户的卡片数量统计
		if err := p.biz.Users().IncrementUserCardNum(ctx, userID); err != nil {
			log.C(ctx).Errorw("Failed to increment user card num for cover card", "book_id", bookID, "error", err.Error())
		}
	}

	// 根据渲染器类型选择不同的渲染策略
	if useRenderAndMeasure {
		// 使用渲染-测量方案：批量渲染所有卡片
		log.C(ctx).Infow("使用渲染-测量方案批量渲染", "book_id", bookID, "card_count", len(paginatedContent.Cards))

		// 为每个分页后的卡片创建单独的CardM记录
		var allCards []*model.CardM
		for i, cardContent := range paginatedContent.Cards {
			// 将卡片内容转换为JSON格式
			var cardElements []map[string]interface{}
			for _, element := range cardContent.Elements {
				cardElements = append(cardElements, map[string]interface{}{
					"type":    element.Type,
					"content": element.Content,
				})
			}

			// 将JSON数据转换为字符串
			cardJSONStr, err := json.Marshal(cardElements)
			if err != nil {
				log.C(ctx).Errorw("Failed to marshal card JSON", "book_id", bookID, "card_index", i, "error", err.Error())
				continue
			}

			// 创建卡片记录
			cardRecord := &model.CardM{
				UserID:        userID,
				BookID:        book.ID,
				ProcessedText: string(cardJSONStr),
				SortOrder:     i + 1, // 从1开始计数，0是封面卡片
			}

			if err := p.biz.Cards().Create(ctx, cardRecord); err != nil {
				log.C(ctx).Errorw("Failed to create card", "book_id", bookID, "card_index", i, "error", err.Error())
				continue
			}

			allCards = append(allCards, cardRecord)
		}

		// 批量渲染所有卡片
		if len(allCards) > 0 {
			renderedCards, err := renderer.(*card.RenderAndMeasureRenderer).RenderBookToImages(book, allCards)
			if err != nil {
				log.C(ctx).Errorw("Failed to batch render cards", "book_id", bookID, "error", err.Error())
			} else {
				// 更新所有卡片的渲染图片
				for i, renderedCard := range renderedCards {
					if i < len(allCards) {
						allCards[i].RenderedImage = renderedCard.ImageURL
						if err := p.biz.Cards().Update(ctx, allCards[i]); err != nil {
							log.C(ctx).Errorw("Failed to update card with rendered image", "book_id", bookID, "card_id", allCards[i].ID, "error", err.Error())
						}
					}
				}
			}
		}

		// 更新用户卡片统计
		if err := p.biz.Users().IncrementUserCardNum(ctx, userID); err != nil {
			log.C(ctx).Errorw("Failed to increment user card num", "book_id", bookID, "error", err.Error())
			// 统计更新失败不影响主要流程
		}
	} else {
		// 使用传统方案：逐张卡片渲染
		log.C(ctx).Infow("使用传统方案逐张渲染", "book_id", bookID, "card_count", len(paginatedContent.Cards))

		for i, cardContent := range paginatedContent.Cards {
			// 将卡片内容转换为JSON格式
			var cardElements []map[string]interface{}
			for _, element := range cardContent.Elements {
				cardElements = append(cardElements, map[string]interface{}{
					"type":    element.Type,
					"content": element.Content,
				})
			}

			// 将JSON数据转换为字符串
			cardJSONStr, err := json.Marshal(cardElements)
			if err != nil {
				log.C(ctx).Errorw("Failed to marshal card JSON", "book_id", bookID, "card_index", i, "error", err.Error())
				continue
			}

			// 创建卡片记录
			cardRecord := &model.CardM{
				UserID:        userID,
				BookID:        book.ID,
				ProcessedText: string(cardJSONStr),
				SortOrder:     i + 1, // 从1开始计数，0是封面卡片
			}

			if err := p.biz.Cards().Create(ctx, cardRecord); err != nil {
				log.C(ctx).Errorw("Failed to create card", "book_id", bookID, "card_index", i, "error", err.Error())
				continue
			}

			// 渲染卡片为图片
			log.C(ctx).Infow("Starting to render card", "book_id", bookID, "card_id", cardRecord.ID, "card_index", i)

			renderedCard, err := renderer.RenderCardToImage(cardRecord)
			if err != nil {
				log.C(ctx).Errorw("Failed to render card to image",
					"book_id", bookID,
					"card_id", cardRecord.ID,
					"card_index", i,
					"error", err.Error(),
					"card_content_length", len(cardRecord.ProcessedText),
					"card_content_preview", string(cardRecord.ProcessedText[:min(100, len(cardRecord.ProcessedText))]))

				// 修复：卡片渲染失败时，将整个 book 标记为失败
				p.updateBookStatus(ctx, bookID, model.BookStatusFailed, fmt.Sprintf("Failed to render card %d to image: %v", cardRecord.ID, err.Error()))
				return
			} else {
				log.C(ctx).Infow("Card rendered successfully",
					"book_id", bookID,
					"card_id", cardRecord.ID,
					"image_url", renderedCard.ImageURL,
					"image_size", fmt.Sprintf("%dx%d", renderedCard.Width, renderedCard.Height))

				// 更新卡片记录，保存渲染后的图片URL
				cardRecord.RenderedImage = renderedCard.ImageURL
				if err := p.biz.Cards().Update(ctx, cardRecord); err != nil {
					log.C(ctx).Errorw("Failed to update card with rendered image", "book_id", bookID, "card_id", cardRecord.ID, "error", err.Error())
				}
			}

			// 更新用户的卡片数量统计
			if err := p.biz.Users().IncrementUserCardNum(ctx, userID); err != nil {
				log.C(ctx).Errorw("Failed to increment user card num", "book_id", bookID, "card_id", cardRecord.ID, "error", err.Error())
			}
		}
	}

	// 更新书籍的卡片数量
	book.CardCount = len(paginatedContent.Cards)
	if err := p.biz.Books().Update(ctx, book); err != nil {
		log.C(ctx).Errorw("Failed to update book card count", "book_id", bookID, "error", err.Error())
	}

	// 注意：用户统计已在创建book时更新，这里不需要再次更新

	// 更新book状态为成功
	if err := p.updateBookStatus(ctx, bookID, model.BookStatusSuccess, ""); err != nil {
		log.C(ctx).Errorw("Failed to update book status to success", "book_id", bookID, "error", err.Error())
		return
	}

	log.C(ctx).Infow("Async book creation completed", "book_id", bookID, "duration", time.Since(startTime).Seconds())
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
	StructuredTextArray []StructuredTextItem `json:"structured_text_array"`
	ImagePrompt         string               `json:"image_prompt"`
}

// StructuredTextItem 结构化文本项
type StructuredTextItem struct {
	Type    string      `json:"type"`
	Content interface{} `json:"content"`
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

	// 固定文件名：book_{id}.png
	localFilePath := filepath.Join(localDir, fmt.Sprintf("book_%d.png", bookID))

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

// min 返回两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
