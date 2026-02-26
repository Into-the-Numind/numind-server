package image

import (
	"context"
	"fmt"
	"mime/multipart"
	"path/filepath"
	"strings"

	"github.com/asaskevich/govalidator"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/book"
	"numind-server/internal/numind/biz/image"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// OCRResult 百度OCR返回结果的结构
type OCRResult struct {
	WordsResult []struct {
		Words string `json:"words"`
	} `json:"words_result"`
	WordsResultNum int   `json:"words_result_num"`
	LogID          int64 `json:"log_id"`
}

// getUserIDFromToken 从JWT token中获取用户ID
func getUserIDFromToken(c *gin.Context) (uint, error) {
	header := c.Request.Header.Get("Authorization")
	if len(header) == 0 {
		return 0, fmt.Errorf("missing authorization header")
	}

	var tokenString string
	_, _ = fmt.Sscanf(header, "Bearer %s", &tokenString)

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

// BatchCreate 批量创建图片记录
func (ctrl *ImageController) BatchCreate(c *gin.Context) {
	log.C(c).Infow("Batch create images function called")

	var req []*model.ImageM
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	for _, img := range req {
		if _, err := govalidator.ValidateStruct(img); err != nil {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("%s", err.Error()), nil)
			return
		}
	}

	if err := ctrl.b.Images().BatchCreate(c, req); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// BatchUpload 支持批量上传图片文件 - 异步处理版本
func (ctrl *ImageController) BatchUpload(c *gin.Context) {
	log.C(c).Infow("Batch upload images function called")

	form, err := c.MultipartForm()
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("Invalid multipart form"), nil)
		return
	}

	files := form.File["files"]
	if len(files) == 0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("No files uploaded"), nil)
		return
	}

	// 从JWT token中获取用户ID
	userID, err := getUserIDFromToken(c)
	if err != nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("Failed to get user ID from token: %s", err.Error()), nil)
		return
	}

	// 验证文件格式和大小
	for _, file := range files {
		if err := validateImageFile(file); err != nil {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("Invalid file: %s", err.Error()), nil)
			return
		}
	}

	// 创建适配器来包装biz接口
	bizAdapter := &BizAdapter{biz: ctrl.b}

	// 创建异步处理器
	asyncProcessor := image.NewAsyncImageProcessor(
		bizAdapter,
		ctrl.b.Baidu(),
		ctrl.b.Ali(),
		ctrl.b.Volc(), // 添加volc参数
	)

	// 异步处理图片
	taskID, err := asyncProcessor.ProcessImagesAsync(c, userID, files)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Failed to start image processing: %s", err.Error()), nil)
		return
	}

	// 立即返回任务ID
	core.WriteResponse(c, nil, gin.H{
		"task_id": taskID,
		"user_id": userID,
		"status":  "processing",
		"message": "图片处理已开始，请通过日志查看处理结果",
	})
}

// validateImageFile 验证图片文件
func validateImageFile(file *multipart.FileHeader) error {
	// 检查文件大小 (限制为10MB)
	if file.Size > 10*1024*1024 {
		return fmt.Errorf("file too large: %s", file.Filename)
	}

	// 检查文件扩展名
	ext := strings.ToLower(filepath.Ext(file.Filename))
	allowedExts := []string{".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp"}

	allowed := false
	for _, allowedExt := range allowedExts {
		if ext == allowedExt {
			allowed = true
			break
		}
	}

	if !allowed {
		return fmt.Errorf("unsupported file type: %s", file.Filename)
	}

	return nil
}

// BizAdapter 适配器，将biz.IBiz接口适配为异步处理器需要的接口
type BizAdapter struct {
	biz biz.IBiz
}

// Images 实现AsyncImageBiz接口
func (a *BizAdapter) Images() image.AsyncImageBiz {
	return &ImageAdapter{imageBiz: a.biz.Images()}
}

// Books 实现AsyncBookBiz接口
func (a *BizAdapter) Books() image.AsyncBookBiz {
	return &BookAdapter{bookBiz: a.biz.Books()}
}

// ImageAdapter 图片业务适配器
type ImageAdapter struct {
	imageBiz image.ImageBiz
}

// Create 实现AsyncImageBiz.Create方法
func (a *ImageAdapter) Create(ctx context.Context, image *model.ImageM) error {
	return a.imageBiz.Create(ctx, image)
}

// BookAdapter 书籍业务适配器
type BookAdapter struct {
	bookBiz book.BookBiz
}

// Create 实现AsyncBookBiz.Create方法
func (a *BookAdapter) Create(ctx context.Context, book *model.BookM) error {
	return a.bookBiz.Create(ctx, book)
}
