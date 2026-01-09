package v1

import "time"

// ============================================================================
// 客户管理相关数据结构
// ============================================================================

// SubUserInfo 二级客户信息
type SubUserInfo struct {
	UserID              uint       `json:"user_id"`
	Nickname            string     `json:"nickname"`
	Phone               string     `json:"phone"`
	AvatarURL           string     `json:"avatar_url"`
	MembershipType      string     `json:"membership_type"`
	MembershipExpires   *time.Time `json:"membership_expires"`
	TotalSopRuns        int        `json:"total_sop_runs"`
	MonthlySopRuns      int        `json:"monthly_sop_runs"`
	AuthorizedTemplates int        `json:"authorized_templates"` // 已授权的模板数量
	CreatedAt           time.Time  `json:"created_at"`
}

// ListSubUsersResponse 二级客户列表响应
type ListSubUsersResponse struct {
	TotalCount int64         `json:"total_count"`
	SubUsers   []SubUserInfo `json:"sub_users"`
}

// SubUserDetailResponse 二级客户详情响应
type SubUserDetailResponse struct {
	SubUserInfo
	AuthorizedTemplateList []TemplateInfo `json:"authorized_template_list"` // 已授权的模板列表
}

// TemplateInfo 模板信息
type TemplateInfo struct {
	TemplateID  uint      `json:"template_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	GrantedAt   time.Time `json:"granted_at"` // 授权时间
}

// GrantTemplateRequest 授权模板请求
type GrantTemplateRequest struct {
	TemplateIDs []uint `json:"template_ids" binding:"required"` // 批量授权
}

// RevokeTemplateRequest 撤销模板请求
type RevokeTemplateRequest struct {
	TemplateIDs []uint `json:"template_ids" binding:"required"` // 批量撤销
}

// CustomerStatisticsResponse 客户统计响应
type CustomerStatisticsResponse struct {
	TotalSubUsers       int `json:"total_sub_users"`       // 二级客户总数
	ActiveSubUsers      int `json:"active_sub_users"`      // 活跃二级客户数(本月有运行记录)
	TotalTemplatesCount int `json:"total_templates_count"` // 总模板数
	MyTotalSopRuns      int `json:"my_total_sop_runs"`     // 我的总运行次数
	MyMonthlySopRuns    int `json:"my_monthly_sop_runs"`   // 我的当月运行次数
}
