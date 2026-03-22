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

// ====== Billing ======

// AdminBillingOverviewResponse 用量概览响应
type AdminBillingOverviewResponse struct {
	TodayCostCents    int64                  `json:"today_cost_cents"`
	MonthCostCents    int64                  `json:"month_cost_cents"`
	TotalCostCents    int64                  `json:"total_cost_cents"`
	TodayRevenueCents int64                  `json:"today_revenue_cents"`
	MonthRevenueCents int64                  `json:"month_revenue_cents"`
	TotalRevenueCents int64                  `json:"total_revenue_cents"`
	TodayCallCount    int64                  `json:"today_call_count"`
	MonthCallCount    int64                  `json:"month_call_count"`
	TotalCallCount    int64                  `json:"total_call_count"`
	ByServiceType     []AdminServiceTypeStat `json:"by_service_type"`
	ByOperation       []AdminOperationStat   `json:"by_operation"`
	ByProvider        []AdminProviderStat    `json:"by_provider"`
}

// AdminServiceTypeStat 按服务类型统计项
type AdminServiceTypeStat struct {
	ServiceType  string `json:"service_type"`
	CallCount    int64  `json:"call_count"`
	CostCents    int64  `json:"cost_cents"`
	RevenueCents int64  `json:"revenue_cents"`
	TotalTokens  int64  `json:"total_tokens"`
}

// AdminOperationStat 按操作统计项
type AdminOperationStat struct {
	Operation    string `json:"operation"`
	CallCount    int64  `json:"call_count"`
	CostCents    int64  `json:"cost_cents"`
	RevenueCents int64  `json:"revenue_cents"`
}

// AdminProviderStat 按供应商统计项
type AdminProviderStat struct {
	Provider     string `json:"provider"`
	CallCount    int64  `json:"call_count"`
	CostCents    int64  `json:"cost_cents"`
	RevenueCents int64  `json:"revenue_cents"`
}

// AdminUsageRecordItem 用量记录项
type AdminUsageRecordItem struct {
	ID               uint64            `json:"id"`
	UserID           uint              `json:"user_id"`
	ServiceType      string            `json:"service_type"`
	Provider         string            `json:"provider"`
	Model            string            `json:"model"`
	Operation        string            `json:"operation"`
	PromptTokens     int               `json:"prompt_tokens"`
	CompletionTokens int               `json:"completion_tokens"`
	TotalTokens      int               `json:"total_tokens"`
	ReasoningTokens  int               `json:"reasoning_tokens"`
	BytesUploaded    int64             `json:"bytes_uploaded"`
	ItemCount        int               `json:"item_count"`
	CostCents        int64             `json:"cost_cents"`
	RevenueCents     int64             `json:"revenue_cents"`
	BizRefType       string            `json:"biz_ref_type"`
	BizRefID         uint              `json:"biz_ref_id"`
	IsFallback       bool              `json:"is_fallback"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}

// AdminListUsageRecordsResponse 用量记录列表响应
type AdminListUsageRecordsResponse struct {
	Total      int64                  `json:"total"`
	TotalPages int64                  `json:"total_pages"`
	Records    []AdminUsageRecordItem `json:"records"`
}

// AdminUserConsumptionItem 用户消费排行项
type AdminUserConsumptionItem struct {
	UserID    uint   `json:"user_id"`
	Username  string `json:"username"`
	Nickname  string `json:"nickname"`
	CostCents int64  `json:"cost_cents"`
	CallCount int64  `json:"call_count"`
}

// AdminUserConsumptionResponse 用户消费排行响应
type AdminUserConsumptionResponse struct {
	Total      int64                      `json:"total"`
	TotalPages int64                      `json:"total_pages"`
	Users      []AdminUserConsumptionItem `json:"users"`
}

// AdminPricingRuleItem 定价规则项
type AdminPricingRuleItem struct {
	ID                     uint      `json:"id"`
	ServiceType            string    `json:"service_type"`
	Provider               string    `json:"provider"`
	Model                  string    `json:"model"`
	InputPricePerMTok      float64   `json:"input_price_per_mtok"`
	OutputPricePerMTok     float64   `json:"output_price_per_mtok"`
	PricePerCall           float64   `json:"price_per_call"`
	PricePerGB             float64   `json:"price_per_gb"`
	SellInputPricePerMTok  float64   `json:"sell_input_price_per_mtok"`
	SellOutputPricePerMTok float64   `json:"sell_output_price_per_mtok"`
	SellPricePerCall       float64   `json:"sell_price_per_call"`
	SellPricePerGB         float64   `json:"sell_price_per_gb"`
	IsActive               bool      `json:"is_active"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// AdminListPricingRulesResponse 定价规则列表响应
type AdminListPricingRulesResponse struct {
	Total      int64                  `json:"total"`
	TotalPages int64                  `json:"total_pages"`
	Rules      []AdminPricingRuleItem `json:"rules"`
}

// AdminCreatePricingRuleRequest 创建定价规则请求
type AdminCreatePricingRuleRequest struct {
	ServiceType            string  `json:"service_type" binding:"required"`
	Provider               string  `json:"provider" binding:"required"`
	Model                  string  `json:"model"`
	InputPricePerMTok      float64 `json:"input_price_per_mtok"`
	OutputPricePerMTok     float64 `json:"output_price_per_mtok"`
	PricePerCall           float64 `json:"price_per_call"`
	PricePerGB             float64 `json:"price_per_gb"`
	SellInputPricePerMTok  float64 `json:"sell_input_price_per_mtok"`
	SellOutputPricePerMTok float64 `json:"sell_output_price_per_mtok"`
	SellPricePerCall       float64 `json:"sell_price_per_call"`
	SellPricePerGB         float64 `json:"sell_price_per_gb"`
	IsActive               *bool   `json:"is_active"`
}

// AdminUpdatePricingRuleRequest 更新定价规则请求
type AdminUpdatePricingRuleRequest struct {
	ServiceType            *string  `json:"service_type"`
	Provider               *string  `json:"provider"`
	Model                  *string  `json:"model"`
	InputPricePerMTok      *float64 `json:"input_price_per_mtok"`
	OutputPricePerMTok     *float64 `json:"output_price_per_mtok"`
	PricePerCall           *float64 `json:"price_per_call"`
	PricePerGB             *float64 `json:"price_per_gb"`
	SellInputPricePerMTok  *float64 `json:"sell_input_price_per_mtok"`
	SellOutputPricePerMTok *float64 `json:"sell_output_price_per_mtok"`
	SellPricePerCall       *float64 `json:"sell_price_per_call"`
	SellPricePerGB         *float64 `json:"sell_price_per_gb"`
	IsActive               *bool    `json:"is_active"`
}

// ====== Pricing Rule Tiers ======

// AdminPricingRuleTierItem 分段定价项
type AdminPricingRuleTierItem struct {
	ID          uint64  `json:"id"`
	RuleID      uint64  `json:"rule_id"`
	TokenType   string  `json:"token_type"` // input | output
	MinTokens   uint    `json:"min_tokens"`
	MaxTokens   *uint   `json:"max_tokens"` // nil = 不限
	CostPerMTok float64 `json:"cost_per_mtok"`
	SellPerMTok float64 `json:"sell_per_mtok"`
}

// AdminReplaceTiersRequest PUT /pricing-rules/:id/tiers 请求体
type AdminReplaceTiersRequest struct {
	Tiers []AdminTierInput `json:"tiers" binding:"required"`
}

type AdminTierInput struct {
	TokenType   string  `json:"token_type" binding:"required,oneof=input output"`
	MinTokens   uint    `json:"min_tokens"`
	MaxTokens   *uint   `json:"max_tokens"`
	CostPerMTok float64 `json:"cost_per_mtok" binding:"min=0"`
	SellPerMTok float64 `json:"sell_per_mtok" binding:"min=0"`
}

// ====== Analytics ======

// AdminAnalyticsRequest GET /billing/analytics 查询参数
type AdminAnalyticsRequest struct {
	From string `form:"from" binding:"required"` // YYYY-MM-DD
	To   string `form:"to" binding:"required"`   // YYYY-MM-DD
}

// AdminAnalyticsBucket 分布桶
type AdminAnalyticsBucket struct {
	Bucket string `json:"bucket"`
	Count  int64  `json:"count"`
}

// AdminAnalyticsUserDetail 用于前端模拟器的用户原始数据
type AdminAnalyticsUserDetail struct {
	UserID          uint  `json:"user_id"`
	PeriodTokens    int64 `json:"period_tokens"`
	PeriodCostCents int64 `json:"period_cost_cents"`
}

// AdminAnalyticsTopUser 用户消费排行项
type AdminAnalyticsTopUser struct {
	UserID          uint   `json:"user_id"`
	Nickname        string `json:"nickname"`
	PeriodRuns      int64  `json:"period_runs"`
	PeriodTokens    int64  `json:"period_tokens"`
	PeriodCostCents int64  `json:"period_cost_cents"`
}

// AdminAnalyticsModelStat 按模型的统计
type AdminAnalyticsModelStat struct {
	Model           string `json:"model"`
	TokenSharePct   int    `json:"token_share_pct"`
	PeriodCostCents int64  `json:"period_cost_cents"`
}

// AdminAnalyticsSummary 汇总统计
type AdminAnalyticsSummary struct {
	ActiveUsers         int64 `json:"active_users"`
	TotalRuns           int64 `json:"total_runs"`
	DaysInRange         int   `json:"days_in_range"`
	AvgTokensPerRun     int64 `json:"avg_tokens_per_run"`
	P50TokensPerRun     int64 `json:"p50_tokens_per_run"`
	P90TokensPerRun     int64 `json:"p90_tokens_per_run"`
	P95TokensPerRun     int64 `json:"p95_tokens_per_run"`
	P50CostCentsPerUser int64 `json:"p50_cost_cents_per_user"`
	P90CostCentsPerUser int64 `json:"p90_cost_cents_per_user"`
	P95CostCentsPerUser int64 `json:"p95_cost_cents_per_user"`
}

// AdminAnalyticsResponse GET /billing/analytics 响应
type AdminAnalyticsResponse struct {
	Summary          AdminAnalyticsSummary      `json:"summary"`
	RunDistribution  []AdminAnalyticsBucket     `json:"run_distribution"`
	UserDistribution []AdminAnalyticsBucket     `json:"user_distribution"`
	UserDetails      []AdminAnalyticsUserDetail `json:"user_details"`
	ModelBreakdown   []AdminAnalyticsModelStat  `json:"model_breakdown"`
	TopUsers         []AdminAnalyticsTopUser    `json:"top_users"`
}

// AdminRecalculateRequest POST /billing/recalculate 请求体
type AdminRecalculateRequest struct {
	From   string `json:"from" binding:"required"` // YYYY-MM-DD
	To     string `json:"to" binding:"required"`   // YYYY-MM-DD
	DryRun bool   `json:"dry_run"`
}

// AdminRecalculateResponse 重算结果
type AdminRecalculateResponse struct {
	AffectedRecords   int64 `json:"affected_records"`
	OldTotalCostCents int64 `json:"old_total_cost_cents"`
	NewTotalCostCents int64 `json:"new_total_cost_cents"`
	DeltaCents        int64 `json:"delta_cents"`
	DryRun            bool  `json:"dry_run"`
}

// ====== Tier Change Logs ======

// AdminTierChangeLogItem 等级变更日志项
type AdminTierChangeLogItem struct {
	ID             uint64  `json:"id"`
	ParentUserID   uint    `json:"parent_user_id"`
	ParentNickname string  `json:"parent_nickname"`
	SubUserID      uint    `json:"sub_user_id"`
	SubNickname    string  `json:"sub_nickname"`
	OldTier        string  `json:"old_tier"`
	NewTier        string  `json:"new_tier"`
	Months         int     `json:"months"`
	OldTierExpires *string `json:"old_tier_expires"`
	NewTierExpires string  `json:"new_tier_expires"`
	CreatedAt      string  `json:"created_at"`
}

// AdminTierChangeLogsResponse 等级变更日志列表响应
type AdminTierChangeLogsResponse struct {
	Total int64                    `json:"total"`
	Items []AdminTierChangeLogItem `json:"items"`
}

// AdminTierChangeStatsResponse 等级变更统计响应
type AdminTierChangeStatsResponse struct {
	TotalChanges  int64                    `json:"total_changes"`
	Upgrades      int64                    `json:"upgrades"`
	Downgrades    int64                    `json:"downgrades"`
	TierBreakdown []AdminTierBreakdownItem `json:"tier_breakdown"`
}

// AdminTierBreakdownItem 按目标等级的统计项
type AdminTierBreakdownItem struct {
	NewTier     string `json:"new_tier"`
	Count       int64  `json:"count"`
	TotalMonths int64  `json:"total_months"`
}
