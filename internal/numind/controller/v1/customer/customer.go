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

// 批量接口参数上限（防 DoS / 保护 DB）。
// 实际业务场景父账号有限子账号 + 有限 chatbot，50/100 远超可预见业务需求。
const (
	maxChatbotIDsPerRequest = 50
	maxSubUserIDsPerBatch   = 100
)

// CustomerController 客户管理控制器
type CustomerController struct {
	customerBiz customerbiz.ICustomerBiz
	userBiz     userbiz.UserBiz
}

// chatbotIDsRequest chatbot 授权/撤销请求 body（单账号）
// 对称 v1.GrantTemplateRequest，保持本 controller 的权限 request 结构相同风格。
type chatbotIDsRequest struct {
	ChatbotIDs []uint `json:"chatbot_ids" binding:"required"`
}

// batchGrantChatbotRequest 批量授权请求 body（多个子账号 × 多个 chatbot）
type batchGrantChatbotRequest struct {
	UserIDs    []uint `json:"user_ids" binding:"required"`
	ChatbotIDs []uint `json:"chatbot_ids" binding:"required"`
}

// batchRevokeChatbotRequest 批量撤销请求 body
type batchRevokeChatbotRequest struct {
	UserIDs    []uint `json:"user_ids" binding:"required"`
	ChatbotIDs []uint `json:"chatbot_ids" binding:"required"`
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

	// 获取分页参数；不设上限——父账号实际可能拥有上千子账号，截断会导致客户管理页缺人。
	// 前端 numind-web-v3 CustomersView.loadSubUsers 已改循环分页（PAGE=100），双保险：
	// 即使有客户端不分页一次拉，也由前端循环逻辑约束单次查询规模。
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit <= 0 {
		limit = 20
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

// ListSubUserChatbots 获取二级客户的已授权 chatbot 列表
// GET /v1/customers/sub-users/:user_id/chatbots
func (ctrl *CustomerController) ListSubUserChatbots(c *gin.Context) {
	log.C(c).Infow("List sub user chatbots called")

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

	// 拉已授权 chatbot 详情（biz 层做父子校验，跨父账号 → ErrForbidden）
	chatbots, err := ctrl.customerBiz.ListSubUserChatbots(c, user.ID, uint(subUserID))
	if err != nil {
		log.C(c).Errorw("Failed to list sub user chatbots", "parent_user_id", user.ID, "sub_user_id", subUserID, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"chatbots": chatbots,
		"total":    int64(len(chatbots)),
	})
}

// GrantChatbots 为二级客户授权 chatbot
// POST /v1/customers/sub-users/:user_id/chatbots
// Body: { "chatbot_ids": [1, 2, 3] }
func (ctrl *CustomerController) GrantChatbots(c *gin.Context) {
	log.C(c).Infow("Grant chatbots called")

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
	var req chatbotIDsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	if len(req.ChatbotIDs) > maxChatbotIDsPerRequest {
		core.WriteResponse(c, errno.ErrBind.SetMessage("批量数量超出限制"), nil)
		return
	}

	// 执行授权（biz 层做父子校验 + chatbot 归属校验，返回 ErrForbidden/ErrChatbotNotFound）
	if err := ctrl.customerBiz.GrantChatbots(c, user.ID, uint(subUserID), req.ChatbotIDs); err != nil {
		log.C(c).Errorw("Failed to grant chatbots", "parent_user_id", user.ID, "sub_user_id", subUserID, "chatbot_ids", req.ChatbotIDs, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"granted": len(req.ChatbotIDs),
		"message": "授权成功",
	})
}

// RevokeChatbots 撤销二级客户的 chatbot 权限
// DELETE /v1/customers/sub-users/:user_id/chatbots
// Body: { "chatbot_ids": [1, 2] }
func (ctrl *CustomerController) RevokeChatbots(c *gin.Context) {
	log.C(c).Infow("Revoke chatbots called")

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
	var req chatbotIDsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	if len(req.ChatbotIDs) > maxChatbotIDsPerRequest {
		core.WriteResponse(c, errno.ErrBind.SetMessage("批量数量超出限制"), nil)
		return
	}

	log.C(c).Infow("Revoking chatbots", "parent_user_id", user.ID, "sub_user_id", subUserID, "chatbot_ids", req.ChatbotIDs)

	// 执行撤销
	if err := ctrl.customerBiz.RevokeChatbots(c, user.ID, uint(subUserID), req.ChatbotIDs); err != nil {
		log.C(c).Errorw("Failed to revoke chatbots", "parent_user_id", user.ID, "sub_user_id", subUserID, "chatbot_ids", req.ChatbotIDs, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	log.C(c).Infow("Chatbots revoked successfully", "parent_user_id", user.ID, "sub_user_id", subUserID, "chatbot_ids", req.ChatbotIDs)
	core.WriteResponse(c, nil, gin.H{
		"revoked": len(req.ChatbotIDs),
		"message": "撤销成功",
	})
}

// BatchGrantChatbots 批量为多个二级客户授权多个 chatbot
// POST /v1/customers/batch/grant-chatbots
// Body: { "user_ids": [10, 20], "chatbot_ids": [1, 2] }
func (ctrl *CustomerController) BatchGrantChatbots(c *gin.Context) {
	log.C(c).Infow("Batch grant chatbots called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 绑定请求body
	var req batchGrantChatbotRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	if len(req.UserIDs) > maxSubUserIDsPerBatch || len(req.ChatbotIDs) > maxChatbotIDsPerRequest {
		core.WriteResponse(c, errno.ErrBind.SetMessage("批量数量超出限制"), nil)
		return
	}

	// 执行批量授权（biz 层 fail-fast：任一子账号/chatbot 不合法 → 立即返回）
	if err := ctrl.customerBiz.BatchGrantChatbots(c, user.ID, req.UserIDs, req.ChatbotIDs); err != nil {
		log.C(c).Errorw("Failed to batch grant chatbots", "parent_user_id", user.ID, "user_ids", req.UserIDs, "chatbot_ids", req.ChatbotIDs, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"granted": len(req.UserIDs) * len(req.ChatbotIDs),
		"message": "批量授权成功",
	})
}

// BatchRevokeChatbots 批量为多个二级客户撤销 chatbot 权限
// POST /v1/customers/batch/revoke-chatbots
// Body: { "user_ids": [10, 20], "chatbot_ids": [1, 2] }
func (ctrl *CustomerController) BatchRevokeChatbots(c *gin.Context) {
	log.C(c).Infow("Batch revoke chatbots called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 绑定请求body
	var req batchRevokeChatbotRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	if len(req.UserIDs) > maxSubUserIDsPerBatch || len(req.ChatbotIDs) > maxChatbotIDsPerRequest {
		core.WriteResponse(c, errno.ErrBind.SetMessage("批量数量超出限制"), nil)
		return
	}

	// 执行批量撤销
	if err := ctrl.customerBiz.BatchRevokeChatbots(c, user.ID, req.UserIDs, req.ChatbotIDs); err != nil {
		log.C(c).Errorw("Failed to batch revoke chatbots", "parent_user_id", user.ID, "user_ids", req.UserIDs, "chatbot_ids", req.ChatbotIDs, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"revoked": len(req.UserIDs) * len(req.ChatbotIDs),
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
		log.C(c).Errorw("Failed to create customer", "parent_user_id", user.ID, "username", req.Username, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"message": "创建成功",
	})
}

// ListSubUserFeatures 获取子用户的功能权限列表
func (ctrl *CustomerController) ListSubUserFeatures(c *gin.Context) {
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	subUserID, err := strconv.ParseUint(c.Param("user_id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}

	features, err := ctrl.customerBiz.ListUserFeatures(c, user.ID, uint(subUserID))
	if err != nil {
		log.C(c).Errorw("Failed to list user features", "parent_user_id", user.ID, "sub_user_id", subUserID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"features": features,
	})
}

// GrantFeatures 为子用户授权功能
func (ctrl *CustomerController) GrantFeatures(c *gin.Context) {
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	subUserID, err := strconv.ParseUint(c.Param("user_id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}

	var req struct {
		Features []string `json:"features" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	err = ctrl.customerBiz.GrantFeatures(c, user.ID, uint(subUserID), req.Features)
	if err != nil {
		log.C(c).Errorw("Failed to grant features", "parent_user_id", user.ID, "sub_user_id", subUserID, "features", req.Features, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "功能授权成功"})
}

// RevokeFeatures 撤销子用户的功能权限
func (ctrl *CustomerController) RevokeFeatures(c *gin.Context) {
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	subUserID, err := strconv.ParseUint(c.Param("user_id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}

	var req struct {
		Features []string `json:"features" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	err = ctrl.customerBiz.RevokeFeatures(c, user.ID, uint(subUserID), req.Features)
	if err != nil {
		log.C(c).Errorw("Failed to revoke features", "parent_user_id", user.ID, "sub_user_id", subUserID, "features", req.Features, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "功能权限已撤销"})
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
