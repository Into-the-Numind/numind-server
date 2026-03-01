package customer

import (
	"strconv"

	"github.com/gin-gonic/gin"

	customerbiz "numind-server/internal/numind/biz/customer"
	userbiz "numind-server/internal/numind/biz/user"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
)

// CustomerController 客户管理控制器
type CustomerController struct {
	customerBiz customerbiz.ICustomerBiz
	userBiz     userbiz.UserBiz
}

// NewCustomerController 创建客户管理控制器
func NewCustomerController(customerBiz customerbiz.ICustomerBiz, userBiz userbiz.UserBiz) *CustomerController {
	return &CustomerController{
		customerBiz: customerBiz,
		userBiz:     userBiz,
	}
}

// GetStatistics 获取客户统计数据
func (ctrl *CustomerController) GetStatistics(c *gin.Context) {
	log.C(c).Infow("Get customer statistics called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 获取统计数据
	stats, err := ctrl.customerBiz.GetCustomerStatistics(c, user.ID)
	if err != nil {
		log.C(c).Errorw("Failed to get customer statistics", "user_id", user.ID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, stats)
}

// ListSubUsers 获取二级客户列表
func (ctrl *CustomerController) ListSubUsers(c *gin.Context) {
	log.C(c).Infow("List sub users called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 获取分页参数
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	// 限制最大limit
	if limit > 100 {
		limit = 100
	}

	// 获取二级客户列表
	result, err := ctrl.customerBiz.ListSubUsers(c, user.ID, offset, limit)
	if err != nil {
		log.C(c).Errorw("Failed to list sub users", "user_id", user.ID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, result)
}

// GetSubUserDetail 获取二级客户详情
func (ctrl *CustomerController) GetSubUserDetail(c *gin.Context) {
	log.C(c).Infow("Get sub user detail called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 获取sub_user_id参数
	subUserID, err := strconv.ParseUint(c.Param("user_id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}

	// 获取二级客户详情
	detail, err := ctrl.customerBiz.GetSubUserDetail(c, user.ID, uint(subUserID))
	if err != nil {
		log.C(c).Errorw("Failed to get sub user detail", "parent_user_id", user.ID, "sub_user_id", subUserID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, detail)
}

// ListSubUserTemplates 获取二级客户的已授权模板列表
func (ctrl *CustomerController) ListSubUserTemplates(c *gin.Context) {
	log.C(c).Infow("List sub user templates called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 获取sub_user_id参数
	subUserID, err := strconv.ParseUint(c.Param("user_id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}

	// 获取详情(包含已授权模板列表)
	detail, err := ctrl.customerBiz.GetSubUserDetail(c, user.ID, uint(subUserID))
	if err != nil {
		log.C(c).Errorw("Failed to get sub user templates", "parent_user_id", user.ID, "sub_user_id", subUserID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"authorized_templates": detail.AuthorizedTemplates,
		"templates":            detail.AuthorizedTemplates,
		"total":                len(detail.AuthorizedTemplates),
	})
}

// GrantTemplates 为二级客户授权模板
func (ctrl *CustomerController) GrantTemplates(c *gin.Context) {
	log.C(c).Infow("Grant templates called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 获取sub_user_id参数
	subUserID, err := strconv.ParseUint(c.Param("user_id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}

	// 绑定请求body
	var req v1.GrantTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// 执行授权
	err = ctrl.customerBiz.GrantTemplates(c, user.ID, uint(subUserID), req.TemplateIDs)
	if err != nil {
		log.C(c).Errorw("Failed to grant templates", "parent_user_id", user.ID, "sub_user_id", subUserID, "template_ids", req.TemplateIDs, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"message": "授权成功",
	})
}

// BatchGrantTemplates 批量为多个二级客户授权模板
func (ctrl *CustomerController) BatchGrantTemplates(c *gin.Context) {
	log.C(c).Infow("Batch grant templates called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 绑定请求body
	var req v1.BatchGrantTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// 执行批量授权
	err := ctrl.customerBiz.BatchGrantTemplates(c, user.ID, req.UserIDs, req.TemplateIDs)
	if err != nil {
		log.C(c).Errorw("Failed to batch grant templates", "parent_user_id", user.ID, "user_ids", req.UserIDs, "template_ids", req.TemplateIDs, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"message": "批量授权成功",
	})
}

// RevokeTemplates 撤销二级客户的模板权限
func (ctrl *CustomerController) RevokeTemplates(c *gin.Context) {
	log.C(c).Infow("Revoke templates called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 获取sub_user_id参数
	subUserID, err := strconv.ParseUint(c.Param("user_id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}

	// 绑定请求body
	var req v1.RevokeTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	log.C(c).Infow("Revoking templates", "parent_user_id", user.ID, "sub_user_id", subUserID, "template_ids", req.TemplateIDs)

	// 执行撤销
	err = ctrl.customerBiz.RevokeTemplates(c, user.ID, uint(subUserID), req.TemplateIDs)
	if err != nil {
		log.C(c).Errorw("Failed to revoke templates", "parent_user_id", user.ID, "sub_user_id", subUserID, "template_ids", req.TemplateIDs, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	log.C(c).Infow("Templates revoked successfully", "parent_user_id", user.ID, "sub_user_id", subUserID, "template_ids", req.TemplateIDs)
	core.WriteResponse(c, nil, gin.H{
		"message": "撤销成功",
	})
}

// BatchRevokeTemplates 批量为多个二级客户撤销模板权限
func (ctrl *CustomerController) BatchRevokeTemplates(c *gin.Context) {
	log.C(c).Infow("Batch revoke templates called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 绑定请求body
	var req v1.BatchRevokeTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// 执行批量撤销
	err := ctrl.customerBiz.BatchRevokeTemplates(c, user.ID, req.UserIDs, req.TemplateIDs)
	if err != nil {
		log.C(c).Errorw("Failed to batch revoke templates", "parent_user_id", user.ID, "user_ids", req.UserIDs, "template_ids", req.TemplateIDs, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"message": "批量撤销成功",
	})
}

// Create 创建子客户
func (ctrl *CustomerController) Create(c *gin.Context) {
	log.C(c).Infow("Create customer called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 绑定请求body
	var req v1.CreateCustomerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// 验证请求参数
	// TODO: 可以添加额外的验证逻辑

	log.C(c).Infow("Creating customer", "parent_user_id", user.ID, "username", req.Username, "phone", req.Phone)

	// 调用UserBiz创建客户
	if err := ctrl.userBiz.CreateCustomer(c, user.ID, &req); err != nil {
		log.C(c).Errorw("Failed to create customer", "parent_user_id", user.ID, "req", req, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"message": "创建成功",
	})
}

// UpdateSubUserTier 升级子用户会员等级
func (ctrl *CustomerController) UpdateSubUserTier(c *gin.Context) {
	log.C(c).Infow("Update sub user tier called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 获取sub_user_id参数
	subUserID, err := strconv.ParseUint(c.Param("user_id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}

	// 绑定请求body
	var req v1.UpdateTierRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// 执行升级
	err = ctrl.customerBiz.UpdateSubUserTier(c, user.ID, uint(subUserID), &req)
	if err != nil {
		log.C(c).Errorw("Failed to update sub user tier",
			"parent_user_id", user.ID, "sub_user_id", subUserID, "tier", req.Tier, "months", req.Months, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"message": "升级成功",
	})
}

// CheckUsername 检查用户名是否可用
func (ctrl *CustomerController) CheckUsername(c *gin.Context) {
	username := c.Query("username")
	if username == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("用户名不能为空"), nil)
		return
	}

	if err := ctrl.userBiz.CheckUsernameUsage(c, username); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"available": true})
}
