package book

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
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
}

// AsyncBookBiz 书籍业务接口
type AsyncBookBiz interface {
	Create(ctx context.Context, book *model.BookM) error
	Update(ctx context.Context, book *model.BookM) error
	GetByID(ctx context.Context, id uint) (*model.BookM, error)
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

// AsyncPromptManager 提示词管理器接口
type AsyncPromptManager interface {
	GetTextProcessingPrompt() string
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

	// 调用阿里千问文字模型处理文本
	prompt := p.biz.Ali().GetPromptManager().GetTextProcessingPrompt() + "\n\n" + text
	messages := []map[string]string{
		{"role": "user", "content": prompt},
	}

	qianwenResult, err := p.biz.Ali().QianwenTextStream(messages, 1024, 0.5)
	if err != nil {
		log.C(ctx).Errorw("QianwenTextStream failed", "book_id", bookID, "error", err.Error())
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "AI text processing failed: "+err.Error())
		return
	}

	log.C(ctx).Infow("QianwenTextStream result", "book_id", bookID, "result", qianwenResult)

	// 提取JSON内容（处理可能的Markdown代码块格式）
	jsonContent := extractJSONFromResponse(qianwenResult)
	log.C(ctx).Infow("Extracted JSON content", "book_id", bookID, "content", jsonContent)

	// 解析通义千问返回的JSON结果
	var qianwenResponse QianwenResponse
	if err := json.Unmarshal([]byte(jsonContent), &qianwenResponse); err != nil {
		log.C(ctx).Errorw("Failed to parse Qianwen response", "book_id", bookID, "error", err.Error())
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Failed to parse AI response: "+err.Error())
		return
	}

	// 使用分页引擎处理文本
	paginationBiz := pagination.NewPaginationBiz()

	// 提取title作为book的标题
	var bookTitle string
	for _, item := range qianwenResponse.StructuredTextArray {
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
	if qianwenResponse.ImagePrompt != "" {
		remoteImageUrl, err := p.biz.Ali().WanxiangImageAsync(qianwenResponse.ImagePrompt, "", "1024*1024")
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
	for _, item := range qianwenResponse.StructuredTextArray {
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

	paginatedContent, err := paginationBiz.PaginateElements(elements)
	if err != nil {
		log.C(ctx).Errorw("Pagination failed", "book_id", bookID, "error", err.Error())
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Failed to paginate content: "+err.Error())
		return
	}

	// 创建无头浏览器渲染器
	renderer := card.NewSimpleHeadlessRenderer(paginationBiz.GetConfig())
	coverRenderer := card.NewCoverRenderer(paginationBiz.GetConfig())

	// 首先创建封面卡片 (sort_order = 0)
	if book.ImageUrl != "" {
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
			// 渲染封面卡片为图片
			renderedCoverCard, err := coverRenderer.RenderCoverCardFromBook(coverCardRecord, bookTitle, book.ImageUrl)
			if err != nil {
				log.C(ctx).Errorw("Failed to render cover card to image", "book_id", bookID, "error", err.Error())
			} else {
				// 更新封面卡片记录，保存渲染后的图片URL
				coverCardRecord.RenderedImage = renderedCoverCard.ImageURL
				if err := p.biz.Cards().Update(ctx, coverCardRecord); err != nil {
					log.C(ctx).Errorw("Failed to update cover card with rendered image", "book_id", bookID, "error", err.Error())
				}
			}

			// 更新用户的卡片数量统计
			if err := p.biz.Users().IncrementUserCardNum(ctx, userID); err != nil {
				log.C(ctx).Errorw("Failed to increment user card num for cover card", "book_id", bookID, "error", err.Error())
			}
		}
	}

	// 为每个分页后的卡片创建单独的CardM记录
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
		renderedCard, err := renderer.RenderCardToImage(cardRecord)
		if err != nil {
			log.C(ctx).Errorw("Failed to render card to image", "book_id", bookID, "card_id", cardRecord.ID, "error", err.Error())
		} else {
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

	// 更新书籍的卡片数量
	book.CardCount = len(paginatedContent.Cards)
	if err := p.biz.Books().Update(ctx, book); err != nil {
		log.C(ctx).Errorw("Failed to update book card count", "book_id", bookID, "error", err.Error())
	}

	// 更新用户的书籍数量统计
	if err := p.biz.Users().IncrementUserBookNum(ctx, userID); err != nil {
		log.C(ctx).Errorw("Failed to increment user book num", "book_id", bookID, "error", err.Error())
	}

	// 更新book状态为成功
	if err := p.updateBookStatus(ctx, bookID, model.BookStatusSuccess, ""); err != nil {
		log.C(ctx).Errorw("Failed to update book status to success", "book_id", bookID, "error", err.Error())
		return
	}

	log.C(ctx).Infow("Async book creation completed", "book_id", bookID, "duration", time.Since(startTime))
}

// updateBookStatus 更新book状态
func (p *AsyncBookProcessor) updateBookStatus(ctx context.Context, bookID uint, status, errorMsg string) error {
	book, err := p.biz.Books().GetByID(ctx, bookID)
	if err != nil {
		return err
	}

	book.Status = status
	return p.biz.Books().Update(ctx, book)
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

// extractJSONFromResponse 从响应中提取JSON内容
func extractJSONFromResponse(response string) string {
	// 查找JSON内容的开始和结束位置
	start := 0
	end := len(response)

	// 查找第一个 { 或 [
	for i, char := range response {
		if char == '{' || char == '[' {
			start = i
			break
		}
	}

	// 从后往前查找最后一个 } 或 ]
	for i := len(response) - 1; i >= 0; i-- {
		char := response[i]
		if char == '}' || char == ']' {
			end = i + 1
			break
		}
	}

	return response[start:end]
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
