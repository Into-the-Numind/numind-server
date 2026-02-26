package book

import (
	"context"
	"fmt"
	"mime/multipart"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/book"
	"numind-server/internal/numind/biz/config"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// CreateBookRequest 创建笔记的请求结构
type CreateBookRequest struct {
	Text     string `form:"text" binding:"required"` // 用户输入的文字（包含OCR结果）
	Title    string `form:"title"`                   // 笔记标题（可选，为空则不设置标题）
	BookType string `form:"book_type"`               // 笔记类型：text, text_with_image, todo, done
	AIPolish int    `form:"ai_polish"`               // AI润色开关 0=关闭 1=开启
	// 图片文件通过multipart/form-data上传，字段名为"images"，可选
}

// QianwenResponse 通义千问返回的结构化数据
type QianwenResponse struct {
	Text        string `json:"text"`         // 带markdown格式的文字内容
	ImagePrompt string `json:"image_prompt"` // 文生图提示词
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

// validateImageFile 验证图片文件
func validateImageFile(file *multipart.FileHeader) error {
	// 检查文件大小 (限制为10MB)
	if file.Size > 10*1024*1024 {
		return fmt.Errorf("file size too large: %d bytes", file.Size)
	}

	// 检查文件类型
	allowedTypes := []string{"image/jpeg", "image/jpg", "image/png", "image/webp"}
	contentType := file.Header.Get("Content-Type")

	for _, allowedType := range allowedTypes {
		if contentType == allowedType {
			return nil
		}
	}

	return fmt.Errorf("unsupported file type: %s", contentType)
}

// createWithImageProcessor 使用图片处理器创建笔记
func (ctrl *BookController) createWithImageProcessor(c *gin.Context, userID uint, text string, title string, bookType string, files []*multipart.FileHeader, aiPolish int) {
	// 创建适配器来包装biz接口
	bizAdapter := &BookBizAdapter{biz: ctrl.b}

	// 创建异步处理器
	asyncProcessor := book.NewAsyncBookProcessor(bizAdapter)

	// 设置配置读取器（从Redis/数据库读取配置）
	configReader := config.NewConfigReader(ctrl.b.Configs())
	asyncProcessor.SetConfigReader(configReader)

	// 异步创建book（传入title、bookType和aiPolish参数）
	book, err := asyncProcessor.CreateBookWithImagesAsync(c, userID, text, title, bookType, files, aiPolish)
	if err != nil {
		log.C(c).Errorw("Failed to create book with image processor", "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to create book: %s", err.Error()), nil)
		return
	}

	log.C(c).Infow("Book created successfully with image processor",
		"book_id", book.ID,
		"title", book.Title,
		"ai_polish", book.AIPolish)

	// 立即返回成功响应
	core.WriteResponse(c, nil, book)
}

// Create 创建笔记
// 支持multipart/form-data格式，包含text字段和可选的images文件
func (ctrl *BookController) Create(c *gin.Context) {
	log.C(c).Infow("Create book function called")

	// 获取multipart form
	form, err := c.MultipartForm()
	if err != nil {
		log.C(c).Errorw("Failed to get multipart form", "error", err.Error())
		core.WriteResponse(c, errno.ErrBind.SetMessage("Invalid multipart form"), nil)
		return
	}

	log.C(c).Infow("Multipart form received", "form_values", len(form.Value), "form_files", len(form.File))

	// 获取text字段
	textValues := form.Value["text"]
	if len(textValues) == 0 {
		log.C(c).Errorw("Missing text field in form")
		core.WriteResponse(c, errno.ErrBind.SetMessage("Missing text field"), nil)
		return
	}
	text := textValues[0]
	log.C(c).Infow("Text field received", "text_length", len(text))

	// 获取title字段（可选）
	title := ""
	if titleValues := form.Value["title"]; len(titleValues) > 0 && titleValues[0] != "" {
		title = titleValues[0]
	}
	log.C(c).Infow("Title field received", "title", title, "has_title", title != "")

	// 获取book_type字段（可选）
	bookType := ""
	if bookTypeValues := form.Value["book_type"]; len(bookTypeValues) > 0 && bookTypeValues[0] != "" {
		bookType = bookTypeValues[0]
	}
	log.C(c).Infow("Book type field received", "book_type", bookType)

	// 获取ai_polish字段
	aiPolish := 1 // 默认启用AI
	if aiPolishValues := form.Value["ai_polish"]; len(aiPolishValues) > 0 {
		if aiPol, err := strconv.Atoi(aiPolishValues[0]); err == nil {
			aiPolish = aiPol
		}
	}
	log.C(c).Infow("AI polish setting", "ai_polish", aiPolish)

	// 获取上传的图片文件（支持images和files字段名）
	files := form.File["images"]
	if len(files) == 0 {
		files = form.File["files"] // 兼容files字段名
	}
	log.C(c).Infow("Files received", "images_count", len(form.File["images"]), "files_count", len(form.File["files"]), "total_files", len(files))

	// 从JWT token中获取用户ID
	userID, err := getUserIDFromToken(c)
	if err != nil {
		log.C(c).Errorw("Failed to get user ID from token", "error", err.Error())
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("Failed to get user ID from token: %s", err.Error()), nil)
		return
	}

	log.C(c).Infow("User ID extracted from token", "user_id", userID)

	// 获取用户信息并检查会员权限
	user, err := ctrl.b.Users().GetCurrentUser(c, userID)
	if err != nil {
		log.C(c).Errorw("Failed to get user info", "user_id", userID, "error", err.Error())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("获取用户信息失败: %s", err.Error()), nil)
		return
	}

	log.C(c).Infow("User info retrieved",
		"user_id", user.ID,
		"membership_type", user.MembershipType,
		"is_pro", user.IsPro,
		"membership_expires", user.MembershipExpires,
		"is_membership_active", user.IsMembershipActive(),
		"can_use_subscription", user.CanUseSubscription())

	// 检查会员是否过期
	if !user.IsMembershipActive() {
		// 检查是否是订阅会员但已过期
		if user.MembershipType == model.MembershipTypeSubscription || user.MembershipType == model.MembershipTypeBoth {
			log.C(c).Warnw("User membership expired, cannot create book",
				"user_id", user.ID,
				"membership_type", user.MembershipType,
				"membership_expires", user.MembershipExpires,
				"current_time", time.Now())
			core.WriteResponse(c, errno.ErrForbidden.SetMessage("会员已过期，请续费后再创建卡册"), nil)
			return
		}
	}

	// 检查订阅会员权限（如果会员有效，则无限制）
	if user.CanUseSubscription() {
		log.C(c).Infow("User has active subscription, unlimited book creation",
			"user_id", user.ID,
			"membership_type", user.MembershipType,
			"membership_expires", user.MembershipExpires)
		// 订阅会员无限制，继续创建
	} else {
		// 检查免费用户限制
		if user.MembershipType == model.MembershipTypeFree {
			if !user.CanCreateBookAsFreeUser() {
				remaining := user.GetRemainingFreeUserMonthlyBooks()
				log.C(c).Warnw("Free user monthly limit reached",
					"user_id", user.ID,
					"free_user_monthly_book_count", user.FreeUserMonthlyBookCount,
					"remaining", remaining)
				core.WriteResponse(c, errno.ErrForbidden.SetMessage(
					"免费用户本月已创建%d个卡册，达到月度限制5个，剩余%d个，下月1号重置",
					user.FreeUserMonthlyBookCount, remaining), nil)
				return
			}
			remaining := user.GetRemainingFreeUserMonthlyBooks()
			log.C(c).Infow("Free user can create book",
				"user_id", user.ID,
				"free_user_monthly_book_count", user.FreeUserMonthlyBookCount,
				"remaining", remaining)
		} else {
			// 其他情况（资源包等）
			log.C(c).Infow("User membership check passed",
				"user_id", user.ID,
				"membership_type", user.MembershipType)
		}
	}

	// 验证文件格式和大小（仅当有文件时）
	for _, file := range files {
		if err := validateImageFile(file); err != nil {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("Invalid file: %s", err.Error()), nil)
			return
		}
	}

	// 使用新的处理模式
	log.C(c).Infow("Using new processing mode with images")
	ctrl.createWithImageProcessor(c, userID, text, title, bookType, files, aiPolish)
}

// BookBizAdapter 适配器，用于包装biz接口
type BookBizAdapter struct {
	biz biz.IBiz
}

// Configs 返回配置业务接口（用于访问配置读取器）
func (a *BookBizAdapter) Configs() biz.IBiz {
	return a.biz
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

func (a *BookBizAdapter) Volc() book.AsyncVolcBiz {
	return &AsyncVolcBizAdapter{biz: a.biz}
}

func (a *BookBizAdapter) Templates() book.AsyncTemplateBiz {
	return &AsyncTemplateBizAdapter{biz: a.biz}
}

func (a *BookBizAdapter) Store() book.AsyncStoreBiz {
	return &AsyncStoreBizAdapter{biz: a.biz}
}

func (a *BookBizAdapter) Images() book.AsyncImageBiz {
	return &AsyncImageBizAdapter{biz: a.biz}
}

func (a *BookBizAdapter) Rag() book.AsyncRagBiz {
	return &AsyncRagBizAdapter{biz: a.biz}
}

// AsyncImageBizAdapter 图片业务适配器
type AsyncImageBizAdapter struct {
	biz biz.IBiz
}

func (a *AsyncImageBizAdapter) Create(ctx context.Context, image *model.ImageM) error {
	return a.biz.Images().Create(ctx, image)
}

func (a *AsyncImageBizAdapter) GetByID(ctx context.Context, id uint) (*model.ImageM, error) {
	return a.biz.Images().GetByID(ctx, id)
}

func (a *AsyncImageBizAdapter) ListByBook(ctx context.Context, bookID uint, offset, limit int) (int64, []*model.ImageM, error) {
	return a.biz.Images().ListByBook(ctx, bookID, offset, limit)
}

func (a *BookBizAdapter) Pagination() book.AsyncPaginationBiz {
	return &AsyncPaginationBizAdapter{biz: a.biz}
}

// AsyncPaginationBizAdapter 分页业务适配器
type AsyncPaginationBizAdapter struct {
	biz biz.IBiz
}

func (a *AsyncPaginationBizAdapter) PaginateText(ctx context.Context, text string) ([]interface{}, error) {
	// 使用现有的分页逻辑
	paginatedContent, err := a.biz.Pagination().PaginateFromJSON(text)
	if err != nil {
		return nil, err
	}

	// 转换为[]interface{}格式
	var result []interface{}
	for _, card := range paginatedContent.Cards {
		result = append(result, card)
	}
	return result, nil
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

func (a *AsyncBookBizAdapter) UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error {
	// 这里需要调用store层的方法，但由于适配器的限制，我们需要通过其他方式处理
	// 可以考虑在store层添加一个方法来直接更新用户统计
	return nil
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

func (a *AsyncCardBizAdapter) GetByID(ctx context.Context, id uint) (*model.CardM, error) {
	return a.biz.Cards().GetByID(ctx, id)
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

func (a *AsyncUserBizAdapter) IncrementMonthlyBookCount(ctx context.Context, userID uint) error {
	return a.biz.Users().IncrementMonthlyBookCount(ctx, userID)
}

func (a *AsyncUserBizAdapter) IncrementFreeUserMonthlyBookCount(ctx context.Context, userID uint) error {
	return a.biz.Users().IncrementFreeUserMonthlyBookCount(ctx, userID)
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

func (a *AsyncAliBizAdapter) StableDiffusionImageAsync(prompt, size string) (string, error) {
	return a.biz.Ali().StableDiffusionImageAsync(prompt, size)
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

// AsyncTemplateBizAdapter 模板业务适配器
type AsyncTemplateBizAdapter struct {
	biz biz.IBiz
}

func (a *AsyncTemplateBizAdapter) GetByID(ctx context.Context, id uint) (*model.Template, error) {
	return a.biz.Templates().GetByID(ctx, id)
}

// AsyncVolcBizAdapter 火山引擎业务适配器
type AsyncVolcBizAdapter struct {
	biz biz.IBiz
}

func (a *AsyncVolcBizAdapter) VolcTextStream(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64) (string, error) {
	return a.biz.Volc().VolcTextStream(ctx, messages, maxTokens, temperature)
}

// AsyncStoreBizAdapter store层业务适配器
type AsyncStoreBizAdapter struct {
	biz biz.IBiz
}

func (a *AsyncStoreBizAdapter) UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error {
	// 通过store层直接更新用户统计
	return a.biz.Books().UpdateUserBookStatsOnStatusChange(ctx, userID, oldStatus, newStatus)
}

// AsyncRagBizAdapter RAG业务适配器
type AsyncRagBizAdapter struct {
	biz biz.IBiz
}

func (a *AsyncRagBizAdapter) AddBookVector(ctx context.Context, userID uint, bookID uint, content string) error {
	// 通过 biz 层访问 RagService
	// 注意：这里需要确保 biz 层有 RagService 的访问方法
	// 如果还没有，需要在 biz.go 中添加
	if ragService := a.biz.Rag(); ragService != nil {
		return ragService.AddBookVector(ctx, userID, bookID, content)
	}
	return nil // RAG服务未初始化时，不报错
}

func (a *AsyncRagBizAdapter) UpdateBookVector(ctx context.Context, userID uint, bookID uint, content string) error {
	if ragService := a.biz.Rag(); ragService != nil {
		return ragService.UpdateBookVector(ctx, userID, bookID, content)
	}
	return nil
}

func (a *AsyncRagBizAdapter) DeleteBookVector(ctx context.Context, bookID uint) error {
	if ragService := a.biz.Rag(); ragService != nil {
		return ragService.DeleteBookVector(ctx, bookID)
	}
	return nil
}

func (a *AsyncRagBizAdapter) CheckBookVectorExists(ctx context.Context, bookID uint) (bool, error) {
	if ragService := a.biz.Rag(); ragService != nil {
		return ragService.CheckBookVectorExists(ctx, bookID)
	}
	return false, nil
}
