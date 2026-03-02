package v1

import "time"

// AdminLoginRequest 管理员登录请求
type AdminLoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// AdminLoginResponse 管理员登录响应
type AdminLoginResponse struct {
	Token string         `json:"token"`
	User  AdminLoginUser `json:"user"`
}

// AdminLoginUser 登录返回的用户信息
type AdminLoginUser struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
}

// DashboardStatsResponse 仪表盘统计响应
type DashboardStatsResponse struct {
	TotalUsers    int64         `json:"total_users"`
	TierBreakdown TierBreakdown `json:"tier_breakdown"`
	TotalRuns     int64         `json:"total_runs"`
	RunsToday     int64         `json:"runs_today"`
	TotalTokens   int64         `json:"total_tokens"`
}

// TierBreakdown 用户等级分布
type TierBreakdown struct {
	Free     int64 `json:"free"`
	Standard int64 `json:"standard"`
	Premium  int64 `json:"premium"`
}

// RecentRunItem 最近运行记录项
type RecentRunItem struct {
	ID           uint       `json:"id"`
	TemplateID   uint       `json:"template_id"`
	TemplateName string     `json:"template_name"`
	UserID       uint       `json:"user_id"`
	UserNickname string     `json:"user_nickname"`
	Status       string     `json:"status"`
	TotalTokens  int64      `json:"total_tokens"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

// AdminListUsersRequest 管理员用户列表请求
type AdminListUsersRequest struct {
	Offset int    `form:"offset"`
	Limit  int    `form:"limit"`
	Search string `form:"search"` // 搜索关键字（用户名/昵称/手机号）
	Tier   string `form:"tier"`   // 按等级过滤
	Status *int   `form:"status"` // 按状态过滤
}

// AdminUserItem 管理员视角的用户信息
type AdminUserItem struct {
	ID             uint       `json:"id"`
	Username       string     `json:"username"`
	Nickname       string     `json:"nickname"`
	Phone          string     `json:"phone"`
	AvatarURL      string     `json:"avatar_url"`
	IsAdmin        bool       `json:"is_admin"`
	UserTier       string     `json:"user_tier"`
	TierExpires    *time.Time `json:"tier_expires"`
	Status         int        `json:"status"`
	TotalSopRuns   int        `json:"total_sop_runs"`
	MonthlySopRuns int        `json:"monthly_sop_runs"`
	ParentUserID   *uint      `json:"parent_user_id"`
	LastLogin      *time.Time `json:"last_login"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// AdminListUsersResponse 管理员用户列表响应
type AdminListUsersResponse struct {
	Total      int64           `json:"total"`
	TotalPages int64           `json:"total_pages"`
	Users      []AdminUserItem `json:"users"`
}

// AdminUpdateUserRequest 管理员更新用户请求
type AdminUpdateUserRequest struct {
	Nickname *string `json:"nickname"`
	Phone    *string `json:"phone"`
}

// AdminUpdateUserStatusRequest 管理员更新用户状态请求
type AdminUpdateUserStatusRequest struct {
	Status int `json:"status" binding:"oneof=0 1"`
}

// AdminUpdateUserTierRequest 管理员更新用户等级请求
type AdminUpdateUserTierRequest struct {
	Tier   string `json:"tier" binding:"required,oneof=free standard premium"`
	Months int    `json:"months" binding:"omitempty,min=1,max=12"`
}

// AdminResetPasswordRequest 管理员重置密码请求
type AdminResetPasswordRequest struct {
	NewPassword string `json:"new_password"` // 不传则生成随机密码
}

// AdminResetPasswordResponse 管理员重置密码响应
type AdminResetPasswordResponse struct {
	NewPassword string `json:"new_password"`
}
