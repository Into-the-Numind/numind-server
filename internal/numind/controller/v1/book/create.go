package book

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/book"
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
	tokenString := c.GetHeader("Authorization")
	if tokenString == "" {
		return 0, fmt.Errorf("no authorization header")
	}

	// 移除 "Bearer " 前缀
	if len(tokenString) > 7 && tokenString[:7] == "Bearer " {
		tokenString = tokenString[7:]
	}

	// 解析JWT token
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		// 验证签名方法
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(viper.GetString("jwt.secret")), nil
	})

	if err != nil {
		return 0, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		// 从claims中获取用户ID
		if userID, exists := claims["user_id"]; exists {
			switch v := userID.(type) {
			case float64:
				return uint(v), nil
			case int:
				return uint(v), nil
			case uint:
				return v, nil
			default:
				return 0, fmt.Errorf("invalid user_id type in token")
			}
		}
		return 0, fmt.Errorf("user_id not found in token")
	}

	return 0, fmt.Errorf("invalid token")
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
	// 这里实现图片下载和保存逻辑
	// 为了简化，这里返回一个占位符
	return fmt.Sprintf("/images/book_%d_cover.jpg", bookID), nil
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

	// 创建适配器来包装biz接口
	bizAdapter := &BookBizAdapter{biz: ctrl.b}

	// 创建异步处理器
	asyncProcessor := book.NewAsyncBookProcessor(bizAdapter)

	// 异步创建book
	book, err := asyncProcessor.CreateBookAsync(c, userID, req.Text, req.TemplateID)
	if err != nil {
		log.C(c).Errorw("Failed to create book async", "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to create book: "+err.Error()), nil)
		return
	}

	// 立即返回成功响应
	core.WriteResponse(c, nil, book)
}

// BookBizAdapter 适配器，用于包装biz接口
type BookBizAdapter struct {
	biz biz.IBiz
}

func (a *BookBizAdapter) Books() book.AsyncBookBiz {
	return &AsyncBookBizAdapter{biz: a.biz}
}

func (a *BookBizAdapter) Cards() book.AsyncCardBiz {
	return &AsyncCardBizAdapter{biz: a.biz}
}

func (a *BookBizAdapter) Users() book.AsyncUserBiz {
	return &AsyncUserBizAdapter{biz: a.biz}
}

func (a *BookBizAdapter) Ali() book.AsyncAliBiz {
	return &AsyncAliBizAdapter{biz: a.biz}
}

// AsyncBookBizAdapter 书籍业务适配器
type AsyncBookBizAdapter struct {
	biz biz.IBiz
}

func (a *AsyncBookBizAdapter) Create(ctx context.Context, book *model.BookM) error {
	return a.biz.Books().Create(ctx, book)
}

func (a *AsyncBookBizAdapter) Update(ctx context.Context, book *model.BookM) error {
	return a.biz.Books().Update(ctx, book)
}

func (a *AsyncBookBizAdapter) GetByID(ctx context.Context, id uint) (*model.BookM, error) {
	return a.biz.Books().GetByID(ctx, id)
}

// AsyncCardBizAdapter 卡片业务适配器
type AsyncCardBizAdapter struct {
	biz biz.IBiz
}

func (a *AsyncCardBizAdapter) Create(ctx context.Context, card *model.CardM) error {
	return a.biz.Cards().Create(ctx, card)
}

func (a *AsyncCardBizAdapter) Update(ctx context.Context, card *model.CardM) error {
	return a.biz.Cards().Update(ctx, card)
}

// AsyncUserBizAdapter 用户业务适配器
type AsyncUserBizAdapter struct {
	biz biz.IBiz
}

func (a *AsyncUserBizAdapter) IncrementUserBookNum(ctx context.Context, userID uint) error {
	return a.biz.Users().IncrementUserBookNum(ctx, userID)
}

func (a *AsyncUserBizAdapter) IncrementUserCardNum(ctx context.Context, userID uint) error {
	return a.biz.Users().IncrementUserCardNum(ctx, userID)
}

// AsyncAliBizAdapter 阿里业务适配器
type AsyncAliBizAdapter struct {
	biz biz.IBiz
}

func (a *AsyncAliBizAdapter) QianwenTextStream(messages []map[string]string, maxTokens int, temperature float64) (string, error) {
	return a.biz.Ali().QianwenTextStream(messages, maxTokens, temperature)
}

func (a *AsyncAliBizAdapter) WanxiangImageAsync(prompt, style, size string) (string, error) {
	return a.biz.Ali().WanxiangImageAsync(prompt, style, size)
}

func (a *AsyncAliBizAdapter) GetPromptManager() book.AsyncPromptManager {
	return &AsyncPromptManagerAdapter{promptManager: a.biz.Ali().GetPromptManager()}
}

// AsyncPromptManagerAdapter 提示词管理器适配器
type AsyncPromptManagerAdapter struct {
	promptManager interface {
		GetTextProcessingPrompt() string
	}
}

func (a *AsyncPromptManagerAdapter) GetTextProcessingPrompt() string {
	return a.promptManager.GetTextProcessingPrompt()
}
