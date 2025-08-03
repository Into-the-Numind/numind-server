package handlers

import (
	"net/http"
	"numind-server/internal/pkg/middleware"
	"strconv"
	"time"

	"numind-server/internal/services"

	"github.com/gin-gonic/gin"
)

type AuthHandler struct {
	authService *services.AuthService
}

func NewAuthHandler(authService *services.AuthService) *AuthHandler {
	return &AuthHandler{
		authService: authService,
	}
}

// WechatLogin 微信登录
func (h *AuthHandler) WechatLogin(c *gin.Context) {
	var req services.WechatLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	result, err := h.authService.WechatLogin(&req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "登录成功",
		"data":    result,
	})
}

// AdminLogin 管理员登录
func (h *AuthHandler) AdminLogin(c *gin.Context) {
	var req services.AdminLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	result, err := h.authService.AdminLogin(&req)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "登录成功",
		"data":    result,
	})
}

// UpdateProfile 更新用户资料
func (h *AuthHandler) UpdateProfile(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1,
			"message": "用户未登录",
			"data":    nil,
		})
		return
	}

	var req struct {
		Nickname  *string `json:"nickname"`
		AvatarURL *string `json:"avatar_url"`
		Phone     *string `json:"phone"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	// 更新用户信息
	if req.Nickname != nil {
		user.Nickname = *req.Nickname
	}
	if req.AvatarURL != nil {
		user.AvatarURL = *req.AvatarURL
	}
	if req.Phone != nil {
		user.Phone = *req.Phone
	}

	// 这里需要调用数据库更新，暂时简化处理
	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "用户资料更新成功",
		"data":    user,
	})
}

// GetProfile 获取用户资料
func (h *AuthHandler) GetProfile(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusOK, gin.H{
			"code":    0,
			"message": "未登录用户",
			"data":    gin.H{},
		})
		return
	}

	// 获取用户统计信息
	stats := gin.H{
		"read_count":     0, // 这里需要从数据库获取
		"favorite_count": 0, // 这里需要从数据库获取
		"used_days":      0, // 这里需要计算
	}

	userData := gin.H{
		"id":         user.ID,
		"openid":     user.OpenID,
		"nickname":   user.Nickname,
		"avatar_url": user.AvatarURL,
		"phone":      user.Phone,
		"created_at": user.CreatedAt,
		"stats":      stats,
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取用户资料成功",
		"data":    userData,
	})
}

// GetUserStats 获取用户统计信息
func (h *AuthHandler) GetUserStats(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusOK, gin.H{
			"code":    0,
			"message": "未登录用户",
			"data":    gin.H{},
		})
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

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取统计信息成功",
		"data":    stats,
	})
}

// UploadAvatar 上传头像
func (h *AuthHandler) UploadAvatar(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1,
			"message": "用户未登录",
			"data":    nil,
		})
		return
	}

	_, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "文件上传失败: " + err.Error(),
			"data":    nil,
		})
		return
	}

	// 这里应该实现文件上传到OSS的逻辑
	// 暂时返回模拟的URL
	avatarURL := "https://example.com/avatar/" + strconv.FormatUint(uint64(user.ID), 10) + ".jpg"

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "头像上传成功",
		"data": gin.H{
			"avatar_url": avatarURL,
		},
	})
}

// GetPhoneNumber 获取手机号
func (h *AuthHandler) GetPhoneNumber(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1,
			"message": "用户未登录",
			"data":    nil,
		})
		return
	}

	var req struct {
		Code string `json:"code" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	// 这里应该调用微信API获取手机号
	// 暂时返回模拟数据
	phoneData := gin.H{
		"phoneNumber":     "13800138000",
		"purePhoneNumber": "13800138000",
		"countryCode":     "86",
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "获取手机号成功",
		"data":    phoneData,
	})
}

// UpdatePhone 更新手机号
func (h *AuthHandler) UpdatePhone(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1,
			"message": "用户未登录",
			"data":    nil,
		})
		return
	}

	var req struct {
		Phone string `json:"phone" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	// 更新手机号
	user.Phone = req.Phone

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "手机号更新成功",
		"data":    user,
	})
}

// ChangePassword 修改密码
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1,
			"message": "用户未登录",
			"data":    nil,
		})
		return
	}

	var req services.ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	err := h.authService.ChangePassword(user.ID, &req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "密码修改成功",
		"data":    nil,
	})
}
