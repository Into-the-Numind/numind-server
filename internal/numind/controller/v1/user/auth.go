package user

import (
	"time"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/util"
)

// GetProfile 获取用户资料
func (ctrl *UserController) GetProfile(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	// 获取用户统计信息
	stats := gin.H{
		"read_count":     0, // 这里需要从数据库获取
		"favorite_count": 0, // 这里需要从数据库获取
		"used_days":      0, // 这里需要计算
	}

	// 转换头像URL用于展示（优先使用COS链接）
	avatarURL := user.AvatarURL
	if avatarURL != "" {
		avatarURL = util.GetAvatarWithCOS(c, user.ID, avatarURL)
	}

	userData := gin.H{
		"id":         user.ID,
		"openid":     user.OpenID,
		"nickname":   user.Nickname,
		"avatar_url": avatarURL,
		"phone":      user.Phone,
		"created_at": user.CreatedAt,
		"stats":      stats,
	}

	core.WriteResponse(c, nil, userData)
}

// GetUserStats 获取用户统计信息
func (ctrl *UserController) GetUserStats(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	// 计算使用天数
	usedDays := 0
	if !user.CreatedAt.IsZero() {
		usedDays = int(time.Since(user.CreatedAt).Hours()/24) + 1
	}

	stats := gin.H{
		"read_count":     0, // 这里需要从数据库获取
		"favorite_count": 0, // 这里需要从数据库获取
		"used_days":      usedDays,
	}

	core.WriteResponse(c, nil, stats)
}

// // UploadAvatar 上传头像
// func (ctrl *UserController) UploadAvatar(c *gin.Context) {
// 	user := middleware.GetCurrentUser(c)
// 	if user == nil {
// 		core.WriteResponse(c, errno.ErrUnauthorized, nil)
// 		return
// 	}

// 	_, err := c.FormFile("file")
// 	if err != nil {
// 		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文件上传失败: "+err.Error()), nil)
// 		return
// 	}

// 	// 这里应该实现文件上传到OSS的逻辑
// 	// 暂时返回模拟的URL
// 	avatarURL := "https://example.com/avatar/" + strconv.FormatUint(uint64(user.ID), 10) + ".jpg"

// 	core.WriteResponse(c, nil, gin.H{
// 		"avatar_url": avatarURL,
// 	})
// }

// GetPhoneNumber 获取手机号
func (ctrl *UserController) GetPhoneNumber(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	var req struct {
		Code string `json:"code" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 这里应该调用微信API获取手机号
	// 暂时返回模拟数据
	phoneData := gin.H{
		"phoneNumber":     "13800138000",
		"purePhoneNumber": "13800138000",
		"countryCode":     "86",
	}

	core.WriteResponse(c, nil, phoneData)
}

// UpdatePhone 更新手机号
func (ctrl *UserController) UpdatePhone(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	var req struct {
		Phone string `json:"phone" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 更新手机号
	user.Phone = req.Phone

	core.WriteResponse(c, nil, user)
}
