package book

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// CreateBookRequest 创建卡册的请求结构
type CreateBookRequest struct {
	Text       string `json:"text" binding:"required"`
	TemplateID string `json:"template_id" binding:"required"`
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

// Create 创建一本卡册
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

	// 调用万相生成图片
	imagePrompt := ctrl.b.Ali().GetPromptManager().FormatImagePrompt(qianwenResult)
	imageUrl, err := ctrl.b.Ali().WanxiangImageAsync(imagePrompt, "", "1024*1024")
	if err != nil {
		log.C(c).Errorw("WanxiangImageAsync failed", "error", err.Error())
		// 图片生成失败不影响整体流程
	}

	// 使用分页引擎处理文本
	paginationBiz := pagination.NewPaginationBiz()

	// 将文本转换为分页元素
	elements := []pagination.Element{
		{
			Type:    pagination.ElementTypeNumber, // 使用number类型作为标题
			Content: "AI处理结果",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: qianwenResult,
		},
	}

	paginatedContent, err := paginationBiz.PaginateElements(elements)
	if err != nil {
		log.C(c).Errorw("Pagination failed", "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to paginate content: "+err.Error()), nil)
		return
	}

	// 创建卡册，关联template_id
	now := time.Now()
	book := &model.BookM{
		UserID:     userID,
		Title:      fmt.Sprintf("AI生成卡册 - %s", time.Now().Format("2006-01-02 15:04:05")),
		TemplateID: req.TemplateID,
		ViewTime:   &now,
		ImageUrl:   imageUrl, // 保存生成的图片URL
	}

	if err := ctrl.b.Books().Create(c, book); err != nil {
		log.C(c).Errorw("Failed to create book", "error", err.Error())
		core.WriteResponse(c, err, nil)
		return
	}

	// 更新用户的书籍数量统计
	if err := ctrl.b.Users().IncrementUserBookNum(c, userID); err != nil {
		log.C(c).Errorw("Failed to increment user book num", "error", err.Error())
		// 统计更新失败不影响主要流程，但记录错误
	}

	// 将分页卡片数据转换为JSON格式
	var cardsJSON []interface{}
	for _, card := range paginatedContent.Cards {
		var cardElements []map[string]interface{}
		for _, element := range card.Elements {
			cardElements = append(cardElements, map[string]interface{}{
				"type":    element.Type,
				"content": element.Content,
			})
		}
		cardsJSON = append(cardsJSON, cardElements)
	}

	// 将JSON数据转换为字符串
	cardsJSONStr, err := json.Marshal(cardsJSON)
	if err != nil {
		log.C(c).Errorw("Failed to marshal cards JSON", "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to process cards data: "+err.Error()), nil)
		return
	}

	// 创建卡片记录，将分页数据存储到ProcessedText字段
	card := &model.CardM{
		UserID:        userID,
		BookID:        book.ID,
		ProcessedText: string(cardsJSONStr), // 将JSON数据存储到ProcessedText字段
		SortOrder:     0,
	}

	if err := ctrl.b.Cards().Create(c, card); err != nil {
		log.C(c).Errorw("Failed to create card", "error", err.Error())
		// 卡片创建失败不影响整体流程，但记录错误
	} else {
		// 卡片创建成功后，更新用户的卡片数量统计
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
