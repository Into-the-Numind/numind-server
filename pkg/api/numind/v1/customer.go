package v1

// SubUserMembershipState 子用户会员订阅状态（Task 20 frontend dependency）
type SubUserMembershipState struct {
	HasActiveTrial        bool   `json:"has_active_trial"`
	HasActiveSubscription bool   `json:"has_active_subscription"`
	TrialExpiresAt        string `json:"trial_expires_at,omitempty"`        // YYYY-MM-DD, 无则省略
	SubscriptionExpiresAt string `json:"subscription_expires_at,omitempty"` // YYYY-MM-DD, 无则省略
}

// SubUserInfo 子用户信息
type SubUserInfo struct {
	UserID              uint   `json:"user_id"`
	Username            string `json:"username"`
	Nickname            string `json:"nickname"`
	Phone               string `json:"phone"`
	Avatar              string `json:"avatar"`
	TotalSopRuns        int    `json:"total_sop_runs"`
	AuthorizedTemplates int    `json:"authorized_templates"`
	CreditBalance       int64  `json:"credit_balance"` // 额度余额（total balance incl. booster）
	CreditExpires       string `json:"credit_expires"` // 最晚额度包到期时间

	// Task 20 fields: 前端 GrantMembershipModal 双状态 + trial tab graying
	MembershipState SubUserMembershipState `json:"membership_state"`
	HasUsedTrial    bool                   `json:"has_used_trial"`  // 是否曾用过 trial 包（任意状态）
	CycleRemaining  int64                  `json:"cycle_remaining"` // 订阅+trial 剩余积分（不含 booster）
}

// ListSubUsersResponse 获取子客户列表响应
type ListSubUsersResponse struct {
	Total    int64         `json:"total"`
	SubUsers []SubUserInfo `json:"sub_users"`
}

// SubUserDetailResponse 获取子客户详情响应
type SubUserDetailResponse struct {
	UserID       uint   `json:"user_id"`
	Nickname     string `json:"nickname"`
	Phone        string `json:"phone"`
	Avatar       string `json:"avatar"`
	TotalSopRuns int    `json:"total_sop_runs"`

	AuthorizedTemplatesCount int            `json:"authorized_templates_count"`
	AuthorizedTemplates      []TemplateInfo `json:"authorized_templates"`
}

// TemplateInfo 模板简要信息
type TemplateInfo struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// GrantTemplateRequest 授权模板请求
type GrantTemplateRequest struct {
	TemplateIDs []uint `json:"template_ids" binding:"required"` // 批量授权
}

// BatchGrantTemplateRequest 批量为多个用户授权模板请求
type BatchGrantTemplateRequest struct {
	UserIDs     []uint `json:"user_ids" binding:"required"`     // 用户ID列表
	TemplateIDs []uint `json:"template_ids" binding:"required"` // 模板ID列表
}

// BatchRevokeTemplateRequest 批量为多个用户撤销模板请求
type BatchRevokeTemplateRequest struct {
	UserIDs     []uint `json:"user_ids" binding:"required"`     // 用户ID列表
	TemplateIDs []uint `json:"template_ids" binding:"required"` // 模板ID列表
}

// RevokeTemplateRequest 撤销模板请求
type RevokeTemplateRequest struct {
	TemplateIDs []uint `json:"template_ids" binding:"required"` // 批量撤销
}

// CustomerStatisticsResponse 客户统计响应
type CustomerStatisticsResponse struct {
	TotalSubUsers  int64 `json:"total_sub_users"`
	ActiveSubUsers int64 `json:"active_sub_users"`
	TotalTemplates int64 `json:"total_templates"`
	TotalSopRuns   int64 `json:"total_sop_runs"`
}

// CreateCustomerRequest 创建子客户的请求参数
type CreateCustomerRequest struct {
	Username string `json:"username" binding:"required" valid:"alphanum,required,stringlength(1|255)"`
	Password string `json:"password" binding:"required" valid:"required,stringlength(6|18)"`
	Nickname string `json:"nickname" valid:"stringlength(0|255)"`
	Phone    string `json:"phone"` // Optional for sub-users
}
