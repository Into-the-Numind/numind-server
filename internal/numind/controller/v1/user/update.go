package user

import (
	"fmt"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asaskevich/govalidator"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
	v1 "numind-server/pkg/api/numind/v1"
)

// Update 更新用户信息.
func (ctrl *UserController) Update(c *gin.Context) {
	log.C(c).Infow("Update user function called")

	var r v1.UpdateUserRequest
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)

		return
	}

	if _, err := govalidator.ValidateStruct(r); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("%s", err.Error()), nil)

		return
	}

	if err := ctrl.b.Users().Update(c, c.Param("name"), &r); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, nil)
}

// UpdateProfile 更新当前用户的个人信息
func (ctrl *UserController) UpdateProfile(c *gin.Context) {
	log.C(c).Infow("Update current user profile function called")

	// 从中间件中获取当前用户
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	var r v1.UpdateUserProfileRequest
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	if _, err := govalidator.ValidateStruct(r); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("%s", err.Error()), nil)
		return
	}

	// 更新用户个人信息
	if err := ctrl.b.Users().UpdateUserProfile(c, currentUser.ID, &r); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// UploadAvatar 上传用户头像
func (ctrl *UserController) UploadAvatar(c *gin.Context) {
	log.C(c).Infow("Upload avatar function called")

	// 从中间件中获取当前用户
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	// 获取上传的文件
	file, err := c.FormFile("avatar")
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请选择要上传的头像文件"), nil)
		return
	}

	// 处理头像上传
	avatarURL, err := ctrl.handleAvatarUpload(c, file, currentUser)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 更新用户头像URL
	if err := ctrl.b.Users().UpdateUserAvatar(c, currentUser.ID, avatarURL); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"avatar_url": avatarURL,
	})
}

// handleAvatarUpload 处理头像上传
func (ctrl *UserController) handleAvatarUpload(c *gin.Context, file *multipart.FileHeader, user *model.User) (string, error) {
	// 验证文件
	if err := validateAvatarFile(file); err != nil {
		return "", errno.ErrInvalidParameter.SetMessage("%s", err.Error())
	}

	// 获取配置的图片上传路径
	imagePath := viper.GetString("resource.image_path")
	if imagePath == "" {
		imagePath = "./images/upload" // 默认路径
	}

	// 创建用户头像目录
	userDir := filepath.Join(imagePath, "avatars", fmt.Sprintf("%d", user.ID))
	if err := os.MkdirAll(userDir, 0755); err != nil {
		log.C(c).Errorw("Failed to create user avatar directory", "user_id", user.ID, "path", userDir, "error", err.Error())
		return "", errno.InternalServerError.SetMessage("创建用户头像目录失败")
	}

	// 生成文件名
	ext := strings.ToLower(filepath.Ext(file.Filename))
	timestamp := time.Now().Unix()
	fileName := fmt.Sprintf("avatar_%d%s", timestamp, ext)
	filePath := filepath.Join(userDir, fileName)

	// 保存文件
	if err := c.SaveUploadedFile(file, filePath); err != nil {
		log.C(c).Errorw("Failed to save avatar file", "user_id", user.ID, "file_path", filePath, "error", err.Error())
		return "", errno.InternalServerError.SetMessage("保存头像文件失败")
	}

	// 返回完整路径URL（保存到数据库）
	avatarURL := fmt.Sprintf("%s/avatars/%d/%s", imagePath, user.ID, fileName)

	// 上传到COS（如果启用）
	if util.IsCOSEnabled() {
		// 读取上传的文件
		if imageData, err := os.ReadFile(filePath); err == nil {
			// 构建COS对象键：avatars/{user_id}/avatar_{timestamp}.{ext}
			objectKey := fmt.Sprintf("avatars/%d/%s", user.ID, fileName)

			// 上传到COS
			cosURL, uploadErr := util.UploadBytesToCOS(c, objectKey, file.Header.Get("Content-Type"), imageData)
			if uploadErr != nil {
				log.C(c).Warnw("上传头像到COS失败", "user_id", user.ID, "error", uploadErr.Error())
			} else if cosURL != "" {
				log.C(c).Infow("✅ 用户头像已上传到COS", "user_id", user.ID, "cos_url", cosURL)

				// 生成签名URL（可选，如果需要的话）
				if signedURL, err := util.GenerateSignedURL(c, objectKey, 600); err == nil && signedURL != "" {
					log.C(c).Infow("头像COS签名URL生成成功", "user_id", user.ID, "signed_url", signedURL)
				}
			}
		} else {
			log.C(c).Warnw("读取头像文件失败", "user_id", user.ID, "path", filePath, "error", err.Error())
		}
	}

	log.C(c).Infow("Avatar uploaded successfully", "user_id", user.ID, "file_path", filePath, "avatar_url", avatarURL)

	return avatarURL, nil
}

// validateAvatarFile 验证头像文件
func validateAvatarFile(file *multipart.FileHeader) error {
	// 检查文件大小（限制为2MB）
	if file.Size > 2*1024*1024 {
		return fmt.Errorf("头像文件大小不能超过2MB")
	}

	// 检查文件类型
	contentType := file.Header.Get("Content-Type")
	allowedTypes := map[string]bool{
		"image/jpeg": true,
		"image/jpg":  true,
		"image/png":  true,
		"image/gif":  true,
	}

	if !allowedTypes[contentType] {
		return fmt.Errorf("只支持JPEG、PNG、GIF格式的图片")
	}

	// 检查文件扩展名
	ext := strings.ToLower(filepath.Ext(file.Filename))
	allowedExts := []string{".jpg", ".jpeg", ".png", ".gif"}

	allowed := false
	for _, allowedExt := range allowedExts {
		if ext == allowedExt {
			allowed = true
			break
		}
	}

	if !allowed {
		return fmt.Errorf("不支持的文件格式")
	}

	return nil
}

// updateWechatUser 更新微信用户信息
func (ctrl *UserController) UpdateWechatUser(c *gin.Context) {
	log.C(c).Infow("Update wechat user function called")

	var r v1.UpdateUserRequest
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	if _, err := govalidator.ValidateStruct(r); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("%s", err.Error()), nil)
		return
	}

	openid := c.Param("openid")
	if err := ctrl.b.Users().UpdateWechatUser(c, openid, &r); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
