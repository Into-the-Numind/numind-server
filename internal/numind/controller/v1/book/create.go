package book

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// CreateBookRequest 创建卡册的请求结构
type CreateBookRequest struct {
	Text       string `json:"text" binding:"required"`
	TemplateID string `json:"template_id" binding:"required"`
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

// getUserIDFromToken 从JWT token中获取用户ID
func getUserIDFromToken(c *gin.Context) (uint, error) {
	header := c.Request.Header.Get("Authorization")
	if len(header) == 0 {
		return 0, fmt.Errorf("missing authorization header")
	}

	var tokenString string
	fmt.Sscanf(header, "Bearer %s", &tokenString)

	// 使用viper获取JWT密钥
	jwtSecret := viper.GetString("jwt.secret")
	if jwtSecret == "" {
		return 0, fmt.Errorf("jwt secret not configured")
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})

	if err != nil {
		return 0, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		if userID, exists := claims["user_id"]; exists {
			return uint(userID.(float64)), nil
		}
	}

	return 0, fmt.Errorf("invalid token or missing user_id")
}

// extractJSONFromResponse 从通义千问的响应中提取JSON内容
// 处理可能的Markdown代码块格式（```json ... ```）
func extractJSONFromResponse(response string) string {
	// 去除首尾空白字符
	response = strings.TrimSpace(response)

	// 检查是否被Markdown代码块包围
	if strings.HasPrefix(response, "```json") {
		// 找到代码块的结束位置
		endIndex := strings.LastIndex(response, "```")
		if endIndex > 7 { // 7是"```json"的长度
			return strings.TrimSpace(response[7:endIndex])
		}
	} else if strings.HasPrefix(response, "```") {
		// 处理没有语言标识的代码块
		endIndex := strings.LastIndex(response, "```")
		if endIndex > 3 { // 3是"```"的长度
			return strings.TrimSpace(response[3:endIndex])
		}
	}

	// 如果不是代码块格式，直接返回原内容
	return response
}

// downloadAndSaveImage 下载图片并保存到本地
func downloadAndSaveImage(imageURL string, bookID uint) (string, error) {
	// 获取本地保存路径
	localPath := util.GetBookImagePath(bookID)

	// 确保目录存在
	if err := os.MkdirAll(localPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	// 生成文件名
	filename := fmt.Sprintf("book_%d_%d.jpg", bookID, time.Now().Unix())
	localFilePath := filepath.Join(localPath, filename)

	// 下载图片
	resp, err := http.Get(imageURL)
	if err != nil {
		return "", fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download image, status: %d", resp.StatusCode)
	}

	// 创建本地文件
	file, err := os.Create(localFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to create local file: %w", err)
	}
	defer file.Close()

	// 复制内容到本地文件
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to save image: %w", err)
	}

	// 返回本地文件路径
	return localFilePath, nil
}

func (ctrl *BookController) Create(c *gin.Context) {
	log.C(c).Infow("Create book function called")

	// 处理text和template_id参数
	var req CreateBookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 从JWT token中获取用户ID
	userID, err := getUserIDFromToken(c)
	if err != nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("Failed to get user ID from token: "+err.Error()), nil)
		return
	}

	// 调用阿里千问文字模型处理文本
	// 从配置中获取文本处理提示词
	prompt := ctrl.b.Ali().GetPromptManager().GetTextProcessingPrompt() + "\n\n" + req.Text

	messages := []map[string]string{
		{"role": "user", "content": prompt},
	}

	qianwenResult, err := ctrl.b.Ali().QianwenTextStream(messages, 1024, 0.5)
	if err != nil {
		log.C(c).Errorw("QianwenTextStream failed", "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to process text with AI: "+err.Error()), nil)
		return
	}

	log.C(c).Infow("QianwenTextStream result", "result", qianwenResult)

	// 提取JSON内容（处理可能的Markdown代码块格式）
	jsonContent := extractJSONFromResponse(qianwenResult)
	log.C(c).Infow("Extracted JSON content", "content", jsonContent)

	// 解析通义千问返回的JSON结果
	var qianwenResponse QianwenResponse
	if err := json.Unmarshal([]byte(jsonContent), &qianwenResponse); err != nil {
		log.C(c).Errorw("Failed to parse Qianwen response", "error", err.Error(), "original", qianwenResult, "extracted", jsonContent)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to parse AI response: "+err.Error()), nil)
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
				break // 找到第一个title就使用
			}
		}
	}

	// 如果没有找到title，使用默认标题
	if bookTitle == "" {
		bookTitle = fmt.Sprintf("AI生成卡册 - %s", time.Now().Format("2006-01-02 15:04:05"))
	}

	// 使用解析出的image_prompt调用万相生成图片
	var imageUrl string
	var book *model.BookM

	if qianwenResponse.ImagePrompt != "" {
		remoteImageUrl, err := ctrl.b.Ali().WanxiangImageAsync(qianwenResponse.ImagePrompt, "", "1024*1024")
		if err != nil {
			log.C(c).Errorw("WanxiangImageAsync failed", "error", err.Error())
			// 图片生成失败不影响整体流程
		} else {
			// 先创建book记录以获取ID
			now := time.Now()
			book = &model.BookM{
				UserID:     userID,
				Title:      bookTitle, // 使用从千问返回的title
				TemplateID: req.TemplateID,
				ViewTime:   &now,
			}

			if err := ctrl.b.Books().Create(c, book); err != nil {
				log.C(c).Errorw("Failed to create book", "error", err.Error())
				core.WriteResponse(c, err, nil)
				return
			}

			// 下载并保存图片到本地
			localImagePath, err := downloadAndSaveImage(remoteImageUrl, book.ID)
			if err != nil {
				log.C(c).Errorw("Failed to download and save image", "error", err.Error())
				// 图片保存失败不影响整体流程，但记录错误
			} else {
				// 更新book记录的ImageUrl为本地路径
				book.ImageUrl = localImagePath
				if err := ctrl.b.Books().Update(c, book); err != nil {
					log.C(c).Errorw("Failed to update book with local image path", "error", err.Error())
				}
			}
		}
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
		log.C(c).Errorw("Pagination failed", "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to paginate content: "+err.Error()), nil)
		return
	}

	// 如果没有图片生成，创建book记录
	if book == nil {
		now := time.Now()
		book = &model.BookM{
			UserID:     userID,
			Title:      bookTitle, // 使用从千问返回的title
			TemplateID: req.TemplateID,
			ViewTime:   &now,
			ImageUrl:   imageUrl, // 保存生成的图片URL
		}

		if err := ctrl.b.Books().Create(c, book); err != nil {
			log.C(c).Errorw("Failed to create book", "error", err.Error())
			core.WriteResponse(c, err, nil)
			return
		}
	}

	// 更新用户的书籍数量统计
	if err := ctrl.b.Users().IncrementUserBookNum(c, userID); err != nil {
		log.C(c).Errorw("Failed to increment user book num", "error", err.Error())
		// 统计更新失败不影响主要流程，但记录错误
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
		if err := ctrl.b.Cards().Create(c, coverCardRecord); err != nil {
			log.C(c).Errorw("Failed to create cover card", "error", err.Error())
		} else {
			// 渲染封面卡片为图片
			renderedCoverCard, err := coverRenderer.RenderCoverCardFromBook(coverCardRecord, bookTitle, book.ImageUrl)
			if err != nil {
				log.C(c).Errorw("Failed to render cover card to image", "error", err.Error())
			} else {
				// 更新封面卡片记录，保存渲染后的图片URL
				coverCardRecord.RenderedImage = renderedCoverCard.ImageURL
				if err := ctrl.b.Cards().Update(c, coverCardRecord); err != nil {
					log.C(c).Errorw("Failed to update cover card with rendered image", "error", err.Error())
				}
			}

			// 更新用户的卡片数量统计
			if err := ctrl.b.Users().IncrementUserCardNum(c, userID); err != nil {
				log.C(c).Errorw("Failed to increment user card num for cover card", "error", err.Error())
			}
		}
	}

	// 为每个分页后的卡片创建单独的CardM记录
	for i, card := range paginatedContent.Cards {
		// 将当前卡片的数据转换为JSON格式
		var cardElements []map[string]interface{}
		for _, element := range card.Elements {
			cardElements = append(cardElements, map[string]interface{}{
				"type":    element.Type,
				"content": element.Content,
			})
		}

		// 将当前卡片数据转换为JSON字符串
		cardJSONStr, err := json.Marshal(cardElements)
		if err != nil {
			log.C(c).Errorw("Failed to marshal card JSON", "error", err.Error(), "card_index", i)
			continue // 跳过这个卡片，继续处理下一个
		}

		// 创建卡片记录，将当前卡片数据存储到ProcessedText字段
		cardRecord := &model.CardM{
			UserID:        userID,
			BookID:        book.ID,
			ProcessedText: string(cardJSONStr), // 将当前卡片数据存储到ProcessedText字段
			SortOrder:     i + 1,               // 使用索引+1作为排序顺序，从1开始
		}

		// 先创建卡片记录
		if err := ctrl.b.Cards().Create(c, cardRecord); err != nil {
			log.C(c).Errorw("Failed to create card", "error", err.Error(), "card_index", i)
			continue // 跳过这个卡片，继续处理下一个
		}

		// 渲染卡片为图片
		renderedCard, err := renderer.RenderCardToImage(cardRecord)
		if err != nil {
			log.C(c).Errorw("Failed to render card to image", "error", err.Error(), "card_index", i)
			// 渲染失败不影响主要流程，但记录错误
		} else {
			// 更新卡片记录，保存渲染后的图片URL
			cardRecord.RenderedImage = renderedCard.ImageURL
			if err := ctrl.b.Cards().Update(c, cardRecord); err != nil {
				log.C(c).Errorw("Failed to update card with rendered image", "error", err.Error(), "card_index", i)
			}
		}

		// 更新用户的卡片数量统计
		if err := ctrl.b.Users().IncrementUserCardNum(c, userID); err != nil {
			log.C(c).Errorw("Failed to increment user card num", "error", err.Error())
			// 统计更新失败不影响主要流程，但记录错误
		}
	}

	// 更新书籍的卡片数量
	book.CardCount = len(paginatedContent.Cards)
	if err := ctrl.b.Books().Update(c, book); err != nil {
		log.C(c).Errorw("Failed to update book card count", "error", err.Error())
	}

	// 获取更新后的书籍信息
	updatedBook, err := ctrl.b.Books().GetByID(c, book.ID)
	if err != nil {
		log.C(c).Errorw("Failed to get updated book", "error", err.Error())
		// 如果获取失败，返回原始书籍信息
		core.WriteResponse(c, nil, book)
		return
	}

	// 获取该书籍的所有卡片
	_, cards, err := ctrl.b.Cards().ListByBook(c, book.ID, 0, 1000)
	if err != nil {
		log.C(c).Errorw("Failed to get book cards", "error", err.Error())
		// 卡片获取失败不影响整体流程
	}

	// 创建BookResponse
	bookResponse := model.NewBookResponse(updatedBook)
	if len(cards) > 0 {
		bookResponse.AddCards(cards)
	}

	// 返回BookResponse结构
	core.WriteResponse(c, nil, bookResponse)
}
