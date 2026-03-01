package v1

// SubUserInfo 子用户信息
type SubUserInfo struct {
	UserID              uint   `json:"user_id"`
	Nickname            string `json:"nickname"`
	Phone               string `json:"phone"`
	Avatar              string `json:"avatar"`
	UserTier            string `json:"user_tier"`
	TierExpires         string `json:"tier_expires"`
	TotalSopRuns        int    `json:"total_sop_runs"`
	MonthlySopRuns      int    `json:"monthly_sop_runs"`
	AuthorizedTemplates int    `json:"authorized_templates"`
	RemainingSopRuns    int    `json:"remaining_sop_runs"` // 剩余运行次数
}

// ListSubUsersResponse 获取子客户列表响应
type ListSubUsersResponse struct {
	Total    int64         `json:"total"`
	SubUsers []SubUserInfo `json:"sub_users"`
}

// SubUserDetailResponse 获取子客户详情响应
type SubUserDetailResponse struct {
	UserID         uint   `json:"user_id"`
	Nickname       string `json:"nickname"`
	Phone          string `json:"phone"`
	Avatar         string `json:"avatar"`
	UserTier       string `json:"user_tier"`
	TierExpires    string `json:"tier_expires"`
	TotalSopRuns   int    `json:"total_sop_runs"`
	MonthlySopRuns int    `json:"monthly_sop_runs"`

	AuthorizedTemplatesCount int            `json:"authorized_templates_count"`
	RemainingSopRuns         int            `json:"remaining_sop_runs"`
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
	// 用户等级相关字段（用于侧边栏运行次数卡片）
	UserTier         string `json:"user_tier"`
	TierExpires      string `json:"tier_expires"`
	RemainingSopRuns int    `json:"remaining_sop_runs"`
}

// UpdateTierRequest 升级子用户会员等级请求
type UpdateTierRequest struct {
	Tier   string `json:"tier" binding:"required,oneof=standard premium"`
	Months int    `json:"months" binding:"required,min=1,max=12"`
}

// CreateCustomerRequest 创建子客户的请求参数
type CreateCustomerRequest struct {
	Username string `json:"username" binding:"required" valid:"alphanum,required,stringlength(1|255)"`
	Password string `json:"password" binding:"required" valid:"required,stringlength(6|18)"`
	Nickname string `json:"nickname" valid:"stringlength(0|255)"`
	Phone    string `json:"phone"` // Optional for sub-users
}
