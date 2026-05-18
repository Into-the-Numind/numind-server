package admin_user

import (
	"crypto/rand"
	"math"
	"math/big"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
	"numind-server/pkg/auth"
)

// AdminUserController 管理员用户控制器
type AdminUserController struct {
	ds store.IStore
}

// New 创建管理员用户控制器
func New(ds store.IStore) *AdminUserController {
	return &AdminUserController{ds: ds}
}

// ListUsers 获取用户列表（支持搜索和过滤）
func (ctrl *AdminUserController) ListUsers(c *gin.Context) {
	log.C(c).Infow("Admin list users called")

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit > 100 {
		limit = 100
	}
	search := c.Query("search")
	statusStr := c.Query("status")

	query := ctrl.ds.DB().Model(&model.User{})

	// 搜索（用户名/昵称/手机号）
	if search != "" {
		query = query.Where("username LIKE ? OR nickname LIKE ? OR phone LIKE ?",
			"%"+search+"%", "%"+search+"%", "%"+search+"%")
	}

	// 按状态过滤
	if statusStr != "" {
		status, err := strconv.Atoi(statusStr)
		if err == nil {
			query = query.Where("status = ?", status)
		}
	}

	// 获取总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		log.C(c).Errorw("Failed to count users", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	// 获取用户列表
	var users []model.User
	if err := query.Order("id DESC").Offset(offset).Limit(limit).Find(&users).Error; err != nil {
		log.C(c).Errorw("Failed to list users", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	// 转换为响应格式
	items := make([]v1.AdminUserItem, 0, len(users))
	for _, u := range users {
		items = append(items, v1.AdminUserItem{
			ID:           u.ID,
			Username:     u.Username,
			Nickname:     u.Nickname,
			Phone:        u.Phone,
			AvatarURL:    u.AvatarURL,
			IsAdmin:      u.IsAdmin,
			Status:       u.Status,
			TotalSopRuns: u.TotalSopRuns,
			ParentUserID: u.ParentUserID,
			LastLogin:    u.LastLogin,
			CreatedAt:    u.CreatedAt,
			UpdatedAt:    u.UpdatedAt,
		})
	}

	totalPages := int64(math.Ceil(float64(total) / float64(limit)))

	core.WriteResponse(c, nil, v1.AdminListUsersResponse{
		Total:      total,
		TotalPages: totalPages,
		Users:      items,
	})
}

// GetUser 获取用户详情
func (ctrl *AdminUserController) GetUser(c *gin.Context) {
	log.C(c).Infow("Admin get user called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}

	var user model.User
	if err := ctrl.ds.DB().First(&user, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			core.WriteResponse(c, errno.ErrPageNotFound.SetMessage("用户不存在"), nil)
			return
		}
		log.C(c).Errorw("Failed to get user", "error", err, "user_id", id)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	item := v1.AdminUserItem{
		ID:           user.ID,
		Username:     user.Username,
		Nickname:     user.Nickname,
		Phone:        user.Phone,
		AvatarURL:    user.AvatarURL,
		IsAdmin:      user.IsAdmin,
		Status:       user.Status,
		TotalSopRuns: user.TotalSopRuns,
		ParentUserID: user.ParentUserID,
		LastLogin:    user.LastLogin,
		CreatedAt:    user.CreatedAt,
		UpdatedAt:    user.UpdatedAt,
	}

	core.WriteResponse(c, nil, item)
}

// UpdateUser 更新用户信息
func (ctrl *AdminUserController) UpdateUser(c *gin.Context) {
	log.C(c).Infow("Admin update user called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}

	var req v1.AdminUpdateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误"), nil)
		return
	}

	var user model.User
	if err := ctrl.ds.DB().First(&user, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			core.WriteResponse(c, errno.ErrPageNotFound.SetMessage("用户不存在"), nil)
			return
		}
		log.C(c).Errorw("Failed to get user for update", "error", err, "user_id", id)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("操作失败，请稍后重试"), nil)
		return
	}

	updates := make(map[string]interface{})
	if req.Nickname != nil {
		updates["nickname"] = *req.Nickname
	}
	if req.Phone != nil {
		updates["phone"] = *req.Phone
	}

	if len(updates) > 0 {
		if err := ctrl.ds.DB().Model(&user).Updates(updates).Error; err != nil {
			log.C(c).Errorw("Failed to update user", "error", err, "user_id", id)
			core.WriteResponse(c, errno.InternalServerError.SetMessage("操作失败，请稍后重试"), nil)
			return
		}
	}

	adminUser := middleware.GetCurrentUser(c)
	log.C(c).Infow("Admin updated user info", "admin_id", adminUser.ID, "target_user_id", id, "updates", updates)

	core.WriteResponse(c, nil, nil)
}

// UpdateUserStatus 更新用户状态（启用/禁用）
func (ctrl *AdminUserController) UpdateUserStatus(c *gin.Context) {
	log.C(c).Infow("Admin update user status called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}

	var req v1.AdminUpdateUserStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误"), nil)
		return
	}

	// 检查用户是否存在
	var user model.User
	if err := ctrl.ds.DB().First(&user, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			core.WriteResponse(c, errno.ErrPageNotFound.SetMessage("用户不存在"), nil)
			return
		}
		log.C(c).Errorw("Failed to get user for status update", "error", err, "user_id", id)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("操作失败，请稍后重试"), nil)
		return
	}

	if err := ctrl.ds.DB().Model(&user).Update("status", req.Status).Error; err != nil {
		log.C(c).Errorw("Failed to update user status", "error", err, "user_id", id)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("操作失败，请稍后重试"), nil)
		return
	}

	adminUser := middleware.GetCurrentUser(c)
	log.C(c).Infow("Admin updated user status", "admin_id", adminUser.ID, "target_user_id", id, "new_status", req.Status)

	core.WriteResponse(c, nil, nil)
}

// ResetPassword 重置用户密码
func (ctrl *AdminUserController) ResetPassword(c *gin.Context) {
	log.C(c).Infow("Admin reset password called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}

	// 检查用户是否存在
	var user model.User
	if err := ctrl.ds.DB().First(&user, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			core.WriteResponse(c, errno.ErrPageNotFound.SetMessage("用户不存在"), nil)
			return
		}
		log.C(c).Errorw("Failed to get user for password reset", "error", err, "user_id", id)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("操作失败，请稍后重试"), nil)
		return
	}

	var req v1.AdminResetPasswordRequest
	// 允许空body（使用随机密码）
	_ = c.ShouldBindJSON(&req)

	newPassword := req.NewPassword
	if newPassword == "" {
		newPassword, err = generateRandomPassword(8)
		if err != nil {
			core.WriteResponse(c, errno.InternalServerError.SetMessage("操作失败，请稍后重试"), nil)
			return
		}
	}

	// 使用 bcrypt 加密密码
	hashedPassword, err := auth.Encrypt(newPassword)
	if err != nil {
		log.C(c).Errorw("Failed to encrypt password", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("操作失败，请稍后重试"), nil)
		return
	}

	if err := ctrl.ds.DB().Model(&user).Update("password", hashedPassword).Error; err != nil {
		log.C(c).Errorw("Failed to reset password", "error", err, "user_id", id)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("操作失败，请稍后重试"), nil)
		return
	}

	adminUser := middleware.GetCurrentUser(c)
	log.C(c).Infow("Admin reset user password", "admin_id", adminUser.ID, "target_user_id", id)

	core.WriteResponse(c, nil, v1.AdminResetPasswordResponse{
		NewPassword: newPassword,
	})
}

// generateRandomPassword 生成随机密码
func generateRandomPassword(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, length)
	for i := range result {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		result[i] = charset[n.Int64()]
	}
	return string(result), nil
}
