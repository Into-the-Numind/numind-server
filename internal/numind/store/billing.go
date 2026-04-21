package store

import (
	"context"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/model"
)

// BillingStore 定义了计费数据存储接口
type BillingStore interface {
	// CreateUsageRecord 创建用量记录
	CreateUsageRecord(ctx context.Context, record *model.UsageRecord) error
	// CreateUsageRecords 批量创建用量记录
	CreateUsageRecords(ctx context.Context, records []*model.UsageRecord) error
	// ListUsageByUser 查询用户用量记录（分页）
	ListUsageByUser(ctx context.Context, userID uint, offset, limit int) ([]model.UsageRecord, int64, error)
	// GetUserConsumption 获取用户在指定时间范围内的总消费（分）
	GetUserConsumption(ctx context.Context, userID uint, from, to time.Time) (int64, error)
	// GetOrCreateAccount 获取或创建用户计费账户
	GetOrCreateAccount(ctx context.Context, userID uint) (*model.BillingAccount, error)
	// GetPricingRule 获取匹配的定价规则
	GetPricingRule(ctx context.Context, serviceType, provider, modelName string) (*model.PricingRule, error)
	// GetPricingRuleTiers 获取指定定价规则的分段配置
	GetPricingRuleTiers(ctx context.Context, ruleID uint) ([]model.PricingRuleTier, error)
	// GetProviderModelID resolves the provider-specific model ID (provider_model_id)
	// for the given logical model key and provider name by joining ai_service and
	// ai_service_route via llm_provider. Returns ("", gorm.ErrRecordNotFound) when
	// no mapping exists.
	GetProviderModelID(ctx context.Context, modelKey, providerName string) (string, error)

	// ListUsageRecords 管理端用量记录分页查询（支持多条件过滤）
	ListUsageRecords(ctx context.Context, filter UsageRecordFilter) ([]model.UsageRecord, int64, error)
	// GetUsageOverview 获取用量概览统计
	GetUsageOverview(ctx context.Context) (*UsageOverviewResult, error)
	// GetUserConsumptionRanking 获取用户消费排行榜（分页）
	GetUserConsumptionRanking(ctx context.Context, from, to time.Time, offset, limit int) ([]UserConsumptionItem, int64, error)
	// GetUserUsageOverview 获取指定用户的用量概览统计
	GetUserUsageOverview(ctx context.Context, userID uint) (*UsageOverviewResult, error)
	// ListPricingRules 查询所有定价规则（分页）
	ListPricingRules(ctx context.Context, offset, limit int) ([]model.PricingRule, int64, error)
	// CreatePricingRule 创建定价规则
	CreatePricingRule(ctx context.Context, rule *model.PricingRule) error
	// UpdatePricingRule 更新定价规则
	UpdatePricingRule(ctx context.Context, id uint, update PricingRuleUpdate) error
	// DeletePricingRule 删除定价规则
	DeletePricingRule(ctx context.Context, id uint) error

	// GetTiersByRuleID 获取某规则的所有分段，按 token_type + min_tokens 排序
	GetTiersByRuleID(ctx context.Context, ruleID uint) ([]model.PricingRuleTier, error)
	// ReplaceTiers 全量替换某规则的分段（事务：DELETE + INSERT）
	ReplaceTiers(ctx context.Context, ruleID uint, tiers []model.PricingRuleTier) error

	// GetAnalytics 获取消费分析数据（按时间范围）
	GetAnalytics(ctx context.Context, filter AnalyticsFilter) (*AnalyticsResult, error)

	// RecalculateCosts 按新的分段规则重算指定时间范围的 cost_cents/revenue_cents
	// dryRun=true 时只返回统计不写入
	RecalculateCosts(ctx context.Context, from, to time.Time, dryRun bool) (*RecalculateResult, error)

	// ListTierChangeLogs 查询等级变更日志（支持时间范围和分页）
	ListTierChangeLogs(ctx context.Context, filter TierChangeLogFilter) ([]TierChangeLogItem, int64, error)

	// GetTierChangeStats 获取等级变更月度统计（用于计算收入）
	GetTierChangeStats(ctx context.Context, from, to time.Time) (*TierChangeStats, error)
}

// UsageRecordFilter 用量记录查询过滤条件
type UsageRecordFilter struct {
	UserID      uint
	ServiceType string
	Provider    string
	Operation   string
	DateFrom    *time.Time
	DateTo      *time.Time
	Offset      int
	Limit       int
}

// UsageOverviewResult 用量概览统计结果
type UsageOverviewResult struct {
	TodayCostCents    int64
	MonthCostCents    int64
	TotalCostCents    int64
	TodayRevenueCents int64
	MonthRevenueCents int64
	TotalRevenueCents int64
	TodayCallCount    int64
	MonthCallCount    int64
	TotalCallCount    int64
	ByServiceType     []ServiceTypeStat
	ByOperation       []OperationStat
	ByProvider        []ProviderStat
}

// ServiceTypeStat 按服务类型统计
type ServiceTypeStat struct {
	ServiceType  string `gorm:"column:service_type" json:"service_type"`
	CallCount    int64  `gorm:"column:call_count" json:"call_count"`
	CostCents    int64  `gorm:"column:cost_cents" json:"cost_cents"`
	RevenueCents int64  `gorm:"column:revenue_cents" json:"revenue_cents"`
	TotalTokens  int64  `gorm:"column:total_tokens" json:"total_tokens"`
}

// OperationStat 按业务操作统计
type OperationStat struct {
	Operation    string `gorm:"column:operation" json:"operation"`
	CallCount    int64  `gorm:"column:call_count" json:"call_count"`
	CostCents    int64  `gorm:"column:cost_cents" json:"cost_cents"`
	RevenueCents int64  `gorm:"column:revenue_cents" json:"revenue_cents"`
}

// ProviderStat 按供应商统计
type ProviderStat struct {
	Provider     string `gorm:"column:provider" json:"provider"`
	CallCount    int64  `gorm:"column:call_count" json:"call_count"`
	CostCents    int64  `gorm:"column:cost_cents" json:"cost_cents"`
	RevenueCents int64  `gorm:"column:revenue_cents" json:"revenue_cents"`
}

// PricingRuleUpdate 定价规则更新参数（类型安全，仅允许更新已知字段）
type PricingRuleUpdate struct {
	ServiceType            *string
	Provider               *string
	Model                  *string
	BillingMode            *string
	FlatUnit               *string
	InputPricePerMTok      *float64
	OutputPricePerMTok     *float64
	PricePerCall           *float64
	PricePerGB             *float64
	SellInputPricePerMTok  *float64
	SellOutputPricePerMTok *float64
	SellPricePerCall       *float64
	SellPricePerGB         *float64
	IsActive               *bool
}

// IsEmpty 检查是否没有任何字段需要更新
func (u PricingRuleUpdate) IsEmpty() bool {
	return u.ServiceType == nil && u.Provider == nil && u.Model == nil &&
		u.BillingMode == nil && u.FlatUnit == nil &&
		u.InputPricePerMTok == nil && u.OutputPricePerMTok == nil &&
		u.PricePerCall == nil && u.PricePerGB == nil &&
		u.SellInputPricePerMTok == nil && u.SellOutputPricePerMTok == nil &&
		u.SellPricePerCall == nil && u.SellPricePerGB == nil &&
		u.IsActive == nil
}

// AnalyticsFilter 消费分析查询参数
type AnalyticsFilter struct {
	From time.Time
	To   time.Time
}

// RunTokenStat 单次运行的 token 汇总（用于分布计算）
type RunTokenStat struct {
	BizRefID    uint  `gorm:"column:biz_ref_id"`
	TotalTokens int64 `gorm:"column:total_tokens"`
}

// UserPeriodStat 用户期间消费汇总
type UserPeriodStat struct {
	UserID          uint   `gorm:"column:user_id"`
	Nickname        string `gorm:"column:nickname"`
	PeriodRuns      int64  `gorm:"column:period_runs"`
	PeriodTokens    int64  `gorm:"column:period_tokens"`
	PeriodCostCents int64  `gorm:"column:period_cost_cents"`
}

// ModelPeriodStat 按模型的期间统计
type ModelPeriodStat struct {
	Model           string `gorm:"column:model"`
	PeriodTokens    int64  `gorm:"column:period_tokens"`
	PeriodCostCents int64  `gorm:"column:period_cost_cents"`
}

// AnalyticsResult 消费分析完整结果
type AnalyticsResult struct {
	DaysInRange int
	RunStats    []RunTokenStat
	UserStats   []UserPeriodStat
	ModelStats  []ModelPeriodStat
}

// RecalculateResult 重算结果
type RecalculateResult struct {
	AffectedRecords   int64
	OldTotalCostCents int64
	NewTotalCostCents int64
	DeltaCents        int64
	DryRun            bool
}

// UserConsumptionItem 用户消费排行项
type UserConsumptionItem struct {
	UserID    uint   `gorm:"column:user_id" json:"user_id"`
	Nickname  string `gorm:"column:nickname" json:"nickname"`
	Username  string `gorm:"column:username" json:"username"`
	CostCents int64  `gorm:"column:cost_cents" json:"cost_cents"`
	CallCount int64  `gorm:"column:call_count" json:"call_count"`
}

// TierChangeLogFilter 等级变更日志查询过滤条件
type TierChangeLogFilter struct {
	From   *time.Time
	To     *time.Time
	Offset int
	Limit  int
}

// TierChangeLogItem JOIN user 表取 nickname
type TierChangeLogItem struct {
	ID             uint64     `gorm:"column:id"`
	ParentUserID   uint       `gorm:"column:parent_user_id"`
	ParentNickname string     `gorm:"column:parent_nickname"`
	SubUserID      uint       `gorm:"column:sub_user_id"`
	SubNickname    string     `gorm:"column:sub_nickname"`
	OldTier        string     `gorm:"column:old_tier"`
	NewTier        string     `gorm:"column:new_tier"`
	Months         int        `gorm:"column:months"`
	OldTierExpires *time.Time `gorm:"column:old_tier_expires"`
	NewTierExpires time.Time  `gorm:"column:new_tier_expires"`
	CreatedAt      time.Time  `gorm:"column:created_at"`
}

// TierChangeStats 月度统计
type TierChangeStats struct {
	TotalChanges  int64               `json:"total_changes"`
	Upgrades      int64               `json:"upgrades"`
	Downgrades    int64               `json:"downgrades"`
	TierBreakdown []TierBreakdownItem `json:"tier_breakdown"`
}

// TierBreakdownItem 按目标等级的统计项
type TierBreakdownItem struct {
	NewTier     string `gorm:"column:new_tier" json:"new_tier"`
	Count       int64  `gorm:"column:count" json:"count"`
	TotalMonths int64  `gorm:"column:total_months" json:"total_months"`
}

type billingStore struct {
	db *gorm.DB
}

func newBillingStore(db *gorm.DB) BillingStore {
	return &billingStore{db: db}
}

// CreateUsageRecord 创建用量记录
func (s *billingStore) CreateUsageRecord(ctx context.Context, record *model.UsageRecord) error {
	return s.db.WithContext(ctx).Create(record).Error
}

// CreateUsageRecords 批量创建用量记录
func (s *billingStore) CreateUsageRecords(ctx context.Context, records []*model.UsageRecord) error {
	if len(records) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Create(records).Error
}

// ListUsageByUser 查询用户用量记录（分页）
func (s *billingStore) ListUsageByUser(ctx context.Context, userID uint, offset, limit int) ([]model.UsageRecord, int64, error) {
	var records []model.UsageRecord
	var total int64

	db := s.db.WithContext(ctx).Where("user_id = ?", userID)
	if err := db.Model(&model.UsageRecord{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := db.Order("created_at DESC").Offset(offset).Limit(limit).Find(&records).Error; err != nil {
		return nil, 0, err
	}
	return records, total, nil
}

// GetUserConsumption 获取用户在指定时间范围内的总消费（分）
func (s *billingStore) GetUserConsumption(ctx context.Context, userID uint, from, to time.Time) (int64, error) {
	var total int64
	err := s.db.WithContext(ctx).
		Model(&model.UsageRecord{}).
		Where("user_id = ? AND created_at >= ? AND created_at < ?", userID, from, to).
		Select("COALESCE(SUM(cost_cents), 0)").
		Scan(&total).Error
	return total, err
}

// GetOrCreateAccount 获取或创建用户计费账户（并发安全）
func (s *billingStore) GetOrCreateAccount(ctx context.Context, userID uint) (*model.BillingAccount, error) {
	account := model.BillingAccount{
		UserID: userID,
		Status: "active",
	}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}},
			DoNothing: true,
		}).
		Create(&account).Error
	if err != nil {
		return nil, err
	}

	var result model.BillingAccount
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&result).Error; err != nil {
		return nil, err
	}
	return &result, nil
}

// GetPricingRule 获取匹配的定价规则。三级 fallback（优先级从高到低）：
//  1. 精确匹配 (service_type, provider, model)
//  2. provider 通配 (service_type, provider, '')
//  3. 全局兜底 (service_type, '', '')
//
// 全局兜底行由 migrations/seed_pricing_rules.sql 保证存在。任何未知
// (provider, model) 都能命中全局行（保守价格 ~ ¥3/MTok input, ¥10/MTok
// output），Reconcile 按真实 cost 多退少补。避免 ProviderFromModel 猜错
// 导致 SOP/SalesRAG 在 CheckAndEstimate 就失败的生产事故。
func (s *billingStore) GetPricingRule(ctx context.Context, serviceType, provider, modelName string) (*model.PricingRule, error) {
	// 查三级候选（最多 3 条），在 Go 端选最精确的。
	//
	// 原实现用 Order(gorm.Expr("CASE ... END", args...)) 让 DB 排序，但 GORM v2
	// 的 First() 会把带参数化的 Order 表达式静默丢弃，最终 SQL 只剩
	// `ORDER BY pricing_rule.id LIMIT 1`。当全局兜底 (provider='', model='')
	// 的 id 小于精确行（兜底通常早于模型 seed），精确规则被跳过，所有查询退
	// 回兜底价 ¥3/¥10，usage_record.cost_cents 被低估 ~5x（dev id=352-363 证据）。
	//
	// 改为 Find 取候选集 + Go 端按优先级选：没有 ORDER BY CASE 可能丢失的风险，
	// 也不需要在 CASE 里做字符串参数化（免 SQL injection 顾虑）。候选集最多 3 行，
	// 无性能影响。
	var rules []model.PricingRule
	err := s.db.WithContext(ctx).
		Where(`service_type = ? AND is_active = ?
			AND (
				(provider = ? AND model = ?)
				OR (provider = ? AND model = '')
				OR (provider = '' AND model = '')
			)`,
			serviceType, true,
			provider, modelName,
			provider,
		).
		Find(&rules).Error
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// Priority: 0 = exact (provider, model), 1 = (provider, ''), 2 = ('', '')
	priority := func(r *model.PricingRule) int {
		switch {
		case r.Provider == provider && r.Model == modelName:
			return 0
		case r.Provider == provider && r.Model == "":
			return 1
		default:
			return 2
		}
	}
	best := &rules[0]
	bestP := priority(best)
	for i := 1; i < len(rules); i++ {
		if p := priority(&rules[i]); p < bestP {
			best = &rules[i]
			bestP = p
		}
	}
	return best, nil
}

// GetPricingRuleTiers 获取指定定价规则的分段配置（用于 tiered_token 模式）
func (s *billingStore) GetPricingRuleTiers(ctx context.Context, ruleID uint) ([]model.PricingRuleTier, error) {
	var tiers []model.PricingRuleTier
	err := s.db.WithContext(ctx).
		Where("rule_id = ?", ruleID).
		Order("token_type, min_tokens").
		Find(&tiers).Error
	if err != nil {
		return nil, err
	}
	return tiers, nil
}

// ListUsageRecords 管理端用量记录分页查询（支持多条件过滤）
func (s *billingStore) ListUsageRecords(ctx context.Context, filter UsageRecordFilter) ([]model.UsageRecord, int64, error) {
	var records []model.UsageRecord
	var total int64

	query := s.db.WithContext(ctx).Model(&model.UsageRecord{})

	if filter.UserID > 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.ServiceType != "" {
		query = query.Where("service_type = ?", filter.ServiceType)
	}
	if filter.Provider != "" {
		query = query.Where("provider = ?", filter.Provider)
	}
	if filter.Operation != "" {
		query = query.Where("operation = ?", filter.Operation)
	}
	if filter.DateFrom != nil {
		query = query.Where("created_at >= ?", *filter.DateFrom)
	}
	if filter.DateTo != nil {
		query = query.Where("created_at < ?", *filter.DateTo)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := query.Order("created_at DESC").Offset(filter.Offset).Limit(filter.Limit).Find(&records).Error; err != nil {
		return nil, 0, err
	}
	return records, total, nil
}

// GetUsageOverview 获取用量概览统计
func (s *billingStore) GetUsageOverview(ctx context.Context) (*UsageOverviewResult, error) {
	result := &UsageOverviewResult{}
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	db := s.db.WithContext(ctx)

	// 单条查询合并 today/month/total 统计（减少数据库往返）
	type overviewRow struct {
		TodayCostCents    int64 `gorm:"column:today_cost_cents"`
		TodayRevenueCents int64 `gorm:"column:today_revenue_cents"`
		TodayCallCount    int64 `gorm:"column:today_call_count"`
		MonthCostCents    int64 `gorm:"column:month_cost_cents"`
		MonthRevenueCents int64 `gorm:"column:month_revenue_cents"`
		MonthCallCount    int64 `gorm:"column:month_call_count"`
		TotalCostCents    int64 `gorm:"column:total_cost_cents"`
		TotalRevenueCents int64 `gorm:"column:total_revenue_cents"`
		TotalCallCount    int64 `gorm:"column:total_call_count"`
	}
	var overview overviewRow
	// 参数对应：?1-3=todayStart（cost/revenue/count），?4-6=monthStart（cost/revenue/count）
	// total 统计不带日期条件，无需参数
	if err := db.Model(&model.UsageRecord{}).
		Select(`COALESCE(SUM(CASE WHEN created_at >= ? THEN cost_cents ELSE 0 END),0) as today_cost_cents,
			COALESCE(SUM(CASE WHEN created_at >= ? THEN revenue_cents ELSE 0 END),0) as today_revenue_cents,
			SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END) as today_call_count,
			COALESCE(SUM(CASE WHEN created_at >= ? THEN cost_cents ELSE 0 END),0) as month_cost_cents,
			COALESCE(SUM(CASE WHEN created_at >= ? THEN revenue_cents ELSE 0 END),0) as month_revenue_cents,
			SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END) as month_call_count,
			COALESCE(SUM(cost_cents),0) as total_cost_cents,
			COALESCE(SUM(revenue_cents),0) as total_revenue_cents,
			COUNT(*) as total_call_count`, todayStart, todayStart, todayStart, monthStart, monthStart, monthStart).
		Scan(&overview).Error; err != nil {
		return nil, fmt.Errorf("query usage overview: %w", err)
	}

	result.TodayCostCents = overview.TodayCostCents
	result.TodayRevenueCents = overview.TodayRevenueCents
	result.TodayCallCount = overview.TodayCallCount
	result.MonthCostCents = overview.MonthCostCents
	result.MonthRevenueCents = overview.MonthRevenueCents
	result.MonthCallCount = overview.MonthCallCount
	result.TotalCostCents = overview.TotalCostCents
	result.TotalRevenueCents = overview.TotalRevenueCents
	result.TotalCallCount = overview.TotalCallCount

	// 按服务类型分布
	if err := db.Model(&model.UsageRecord{}).
		Select("service_type, COUNT(*) as call_count, COALESCE(SUM(cost_cents),0) as cost_cents, COALESCE(SUM(revenue_cents),0) as revenue_cents, COALESCE(SUM(total_tokens),0) as total_tokens").
		Group("service_type").
		Order("cost_cents DESC").
		Scan(&result.ByServiceType).Error; err != nil {
		return nil, fmt.Errorf("query by service type: %w", err)
	}

	// 按操作分布
	if err := db.Model(&model.UsageRecord{}).
		Select("operation, COUNT(*) as call_count, COALESCE(SUM(cost_cents),0) as cost_cents, COALESCE(SUM(revenue_cents),0) as revenue_cents").
		Group("operation").
		Order("cost_cents DESC").
		Scan(&result.ByOperation).Error; err != nil {
		return nil, fmt.Errorf("query by operation: %w", err)
	}

	// 按供应商分布
	if err := db.Model(&model.UsageRecord{}).
		Select("provider, COUNT(*) as call_count, COALESCE(SUM(cost_cents),0) as cost_cents, COALESCE(SUM(revenue_cents),0) as revenue_cents").
		Group("provider").
		Order("cost_cents DESC").
		Scan(&result.ByProvider).Error; err != nil {
		return nil, fmt.Errorf("query by provider: %w", err)
	}

	return result, nil
}

// GetUserUsageOverview 获取指定用户的用量概览统计
func (s *billingStore) GetUserUsageOverview(ctx context.Context, userID uint) (*UsageOverviewResult, error) {
	result := &UsageOverviewResult{}
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	db := s.db.WithContext(ctx).Where("user_id = ?", userID)

	// 单条查询合并 today/month/total 统计
	type overviewRow struct {
		TodayCostCents    int64 `gorm:"column:today_cost_cents"`
		TodayRevenueCents int64 `gorm:"column:today_revenue_cents"`
		TodayCallCount    int64 `gorm:"column:today_call_count"`
		MonthCostCents    int64 `gorm:"column:month_cost_cents"`
		MonthRevenueCents int64 `gorm:"column:month_revenue_cents"`
		MonthCallCount    int64 `gorm:"column:month_call_count"`
		TotalCostCents    int64 `gorm:"column:total_cost_cents"`
		TotalRevenueCents int64 `gorm:"column:total_revenue_cents"`
		TotalCallCount    int64 `gorm:"column:total_call_count"`
	}
	var overview overviewRow
	// 参数对应：?1-3=todayStart（cost/revenue/count），?4-6=monthStart（cost/revenue/count）
	if err := db.Model(&model.UsageRecord{}).
		Select(`COALESCE(SUM(CASE WHEN created_at >= ? THEN cost_cents ELSE 0 END),0) as today_cost_cents,
			COALESCE(SUM(CASE WHEN created_at >= ? THEN revenue_cents ELSE 0 END),0) as today_revenue_cents,
			SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END) as today_call_count,
			COALESCE(SUM(CASE WHEN created_at >= ? THEN cost_cents ELSE 0 END),0) as month_cost_cents,
			COALESCE(SUM(CASE WHEN created_at >= ? THEN revenue_cents ELSE 0 END),0) as month_revenue_cents,
			SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END) as month_call_count,
			COALESCE(SUM(cost_cents),0) as total_cost_cents,
			COALESCE(SUM(revenue_cents),0) as total_revenue_cents,
			COUNT(*) as total_call_count`, todayStart, todayStart, todayStart, monthStart, monthStart, monthStart).
		Scan(&overview).Error; err != nil {
		return nil, fmt.Errorf("query user usage overview: %w", err)
	}

	result.TodayCostCents = overview.TodayCostCents
	result.TodayRevenueCents = overview.TodayRevenueCents
	result.TodayCallCount = overview.TodayCallCount
	result.MonthCostCents = overview.MonthCostCents
	result.MonthRevenueCents = overview.MonthRevenueCents
	result.MonthCallCount = overview.MonthCallCount
	result.TotalCostCents = overview.TotalCostCents
	result.TotalRevenueCents = overview.TotalRevenueCents
	result.TotalCallCount = overview.TotalCallCount

	// 按服务类型分布
	if err := s.db.WithContext(ctx).Model(&model.UsageRecord{}).
		Where("user_id = ?", userID).
		Select("service_type, COUNT(*) as call_count, COALESCE(SUM(cost_cents),0) as cost_cents, COALESCE(SUM(revenue_cents),0) as revenue_cents, COALESCE(SUM(total_tokens),0) as total_tokens").
		Group("service_type").
		Order("cost_cents DESC").
		Scan(&result.ByServiceType).Error; err != nil {
		return nil, fmt.Errorf("query by service type: %w", err)
	}

	// 按操作分布
	if err := s.db.WithContext(ctx).Model(&model.UsageRecord{}).
		Where("user_id = ?", userID).
		Select("operation, COUNT(*) as call_count, COALESCE(SUM(cost_cents),0) as cost_cents, COALESCE(SUM(revenue_cents),0) as revenue_cents").
		Group("operation").
		Order("cost_cents DESC").
		Scan(&result.ByOperation).Error; err != nil {
		return nil, fmt.Errorf("query by operation: %w", err)
	}

	// 按供应商分布
	if err := s.db.WithContext(ctx).Model(&model.UsageRecord{}).
		Where("user_id = ?", userID).
		Select("provider, COUNT(*) as call_count, COALESCE(SUM(cost_cents),0) as cost_cents, COALESCE(SUM(revenue_cents),0) as revenue_cents").
		Group("provider").
		Order("cost_cents DESC").
		Scan(&result.ByProvider).Error; err != nil {
		return nil, fmt.Errorf("query by provider: %w", err)
	}

	return result, nil
}

// GetUserConsumptionRanking 获取用户消费排行榜（分页）
func (s *billingStore) GetUserConsumptionRanking(ctx context.Context, from, to time.Time, offset, limit int) ([]UserConsumptionItem, int64, error) {
	var items []UserConsumptionItem
	var total int64

	// Count distinct users
	if err := s.db.WithContext(ctx).
		Table("usage_record").
		Where("created_at >= ? AND created_at < ?", from, to).
		Select("COUNT(DISTINCT user_id)").
		Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	// Get ranked data
	if err := s.db.WithContext(ctx).
		Table("usage_record u").
		Joins("LEFT JOIN user usr ON usr.id = u.user_id").
		Where("u.created_at >= ? AND u.created_at < ?", from, to).
		Select("u.user_id, COALESCE(usr.nickname,'') as nickname, COALESCE(usr.username,'') as username, SUM(u.cost_cents) as cost_cents, COUNT(*) as call_count").
		Group("u.user_id, usr.nickname, usr.username").
		Order("cost_cents DESC").
		Offset(offset).Limit(limit).
		Scan(&items).Error; err != nil {
		return nil, 0, err
	}

	return items, total, nil
}

// ListPricingRules 查询所有定价规则（分页）
func (s *billingStore) ListPricingRules(ctx context.Context, offset, limit int) ([]model.PricingRule, int64, error) {
	var rules []model.PricingRule
	var total int64
	db := s.db.WithContext(ctx).Model(&model.PricingRule{})
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := db.Order("id DESC").Offset(offset).Limit(limit).Find(&rules).Error; err != nil {
		return nil, 0, err
	}
	return rules, total, nil
}

// CreatePricingRule 创建定价规则
func (s *billingStore) CreatePricingRule(ctx context.Context, rule *model.PricingRule) error {
	// Capture intended value before Create; GORM may overwrite the struct field with the
	// DB default when the field has `gorm:"default:true"` and the value is false.
	wantActive := rule.IsActive
	if err := s.db.WithContext(ctx).Create(rule).Error; err != nil {
		return err
	}
	// GORM v2 skips bool zero value (false) when the field has a `default:true` tag
	// (model.PricingRule.IsActive). If the caller explicitly set is_active=false, GORM
	// silently falls back to the DB default of true. A follow-up UpdateColumn restores
	// the requested value.
	if !wantActive && rule.IsActive {
		if err := s.db.WithContext(ctx).Model(rule).UpdateColumn("is_active", false).Error; err != nil {
			return fmt.Errorf("CreatePricingRule: fixup is_active: %w", err)
		}
		rule.IsActive = false
	}
	return nil
}

// UpdatePricingRule 更新定价规则
func (s *billingStore) UpdatePricingRule(ctx context.Context, id uint, update PricingRuleUpdate) error {
	updates := make(map[string]interface{})
	if update.ServiceType != nil {
		updates["service_type"] = *update.ServiceType
	}
	if update.Provider != nil {
		updates["provider"] = *update.Provider
	}
	if update.Model != nil {
		updates["model"] = *update.Model
	}
	if update.BillingMode != nil {
		updates["billing_mode"] = *update.BillingMode
	}
	if update.FlatUnit != nil {
		updates["flat_unit"] = *update.FlatUnit
	}
	if update.InputPricePerMTok != nil {
		updates["input_price_per_m_tok"] = *update.InputPricePerMTok
	}
	if update.OutputPricePerMTok != nil {
		updates["output_price_per_m_tok"] = *update.OutputPricePerMTok
	}
	if update.PricePerCall != nil {
		updates["price_per_call"] = *update.PricePerCall
	}
	if update.PricePerGB != nil {
		updates["price_per_gb"] = *update.PricePerGB
	}
	if update.SellInputPricePerMTok != nil {
		updates["sell_input_price_per_m_tok"] = *update.SellInputPricePerMTok
	}
	if update.SellOutputPricePerMTok != nil {
		updates["sell_output_price_per_m_tok"] = *update.SellOutputPricePerMTok
	}
	if update.SellPricePerCall != nil {
		updates["sell_price_per_call"] = *update.SellPricePerCall
	}
	if update.SellPricePerGB != nil {
		updates["sell_price_per_gb"] = *update.SellPricePerGB
	}
	if update.IsActive != nil {
		updates["is_active"] = *update.IsActive
	}
	if len(updates) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Model(&model.PricingRule{}).Where("id = ?", id).Updates(updates).Error
}

// DeletePricingRule 删除定价规则
func (s *billingStore) DeletePricingRule(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Delete(&model.PricingRule{}, id).Error
}

// GetTiersByRuleID 获取某规则的所有分段，按 token_type + min_tokens 排序
func (s *billingStore) GetTiersByRuleID(ctx context.Context, ruleID uint) ([]model.PricingRuleTier, error) {
	var tiers []model.PricingRuleTier
	err := s.db.WithContext(ctx).
		Where("rule_id = ?", ruleID).
		Order("token_type ASC, min_tokens ASC").
		Find(&tiers).Error
	return tiers, err
}

// ReplaceTiers 全量替换某规则的分段（事务：DELETE + INSERT）
func (s *billingStore) ReplaceTiers(ctx context.Context, ruleID uint, tiers []model.PricingRuleTier) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 删除旧分段
		if err := tx.Where("rule_id = ?", ruleID).Delete(&model.PricingRuleTier{}).Error; err != nil {
			return err
		}
		// 插入新分段
		if len(tiers) == 0 {
			return nil
		}
		for i := range tiers {
			tiers[i].RuleID = ruleID
		}
		return tx.Create(&tiers).Error
	})
}

// GetAnalytics 获取消费分析数据（按时间范围）
func (s *billingStore) GetAnalytics(ctx context.Context, filter AnalyticsFilter) (*AnalyticsResult, error) {
	result := &AnalyticsResult{
		DaysInRange: int(filter.To.Sub(filter.From).Hours()/24) + 1,
	}

	// 1. 每次 SOP 运行的 token 汇总
	err := s.db.WithContext(ctx).
		Model(&model.UsageRecord{}).
		Select("biz_ref_id, COALESCE(SUM(total_tokens), 0) AS total_tokens").
		Where("biz_ref_type = ? AND created_at >= ? AND created_at < ?",
			"sop_run", filter.From, filter.To.AddDate(0, 0, 1)).
		Group("biz_ref_id").
		Scan(&result.RunStats).Error
	if err != nil {
		return nil, fmt.Errorf("get run stats: %w", err)
	}

	// 2. 每个用户的期间汇总（JOIN user 表取 nickname）
	err = s.db.WithContext(ctx).
		Table("usage_record ur").
		Select(`ur.user_id,
			u.nickname,
			COUNT(DISTINCT CASE WHEN ur.biz_ref_type='sop_run' THEN ur.biz_ref_id END) AS period_runs,
			SUM(ur.total_tokens) AS period_tokens,
			SUM(ur.cost_cents)   AS period_cost_cents`).
		Joins("LEFT JOIN `user` u ON u.id = ur.user_id").
		Where("ur.created_at >= ? AND ur.created_at < ?",
			filter.From, filter.To.AddDate(0, 0, 1)).
		Group("ur.user_id, u.nickname").
		Having("period_tokens > 0").
		Order("period_cost_cents DESC").
		Limit(1000).
		Scan(&result.UserStats).Error
	if err != nil {
		return nil, fmt.Errorf("get user stats: %w", err)
	}

	// 3. 按模型分组
	err = s.db.WithContext(ctx).
		Model(&model.UsageRecord{}).
		Select("model, COALESCE(SUM(total_tokens), 0) AS period_tokens, COALESCE(SUM(cost_cents), 0) AS period_cost_cents").
		Where("created_at >= ? AND created_at < ?",
			filter.From, filter.To.AddDate(0, 0, 1)).
		Group("model").
		Order("period_cost_cents DESC").
		Scan(&result.ModelStats).Error
	if err != nil {
		return nil, fmt.Errorf("get model stats: %w", err)
	}

	return result, nil
}

// RecalculateCosts 按新的分段规则重算指定时间范围的 cost_cents/revenue_cents。
// 注意：写入按每 1000 条一个事务分批提交，非全局原子操作。
// 如果进程中途失败，数据库会处于部分重算状态，需检查后重新执行。
// dryRun=true 时只返回统计不写入。
func (s *billingStore) RecalculateCosts(ctx context.Context, from, to time.Time, dryRun bool) (*RecalculateResult, error) {
	// 1. 加载所有 tiered_token 规则和分段
	var rules []model.PricingRule
	if err := s.db.WithContext(ctx).Where("billing_mode = ?", "tiered_token").Find(&rules).Error; err != nil {
		return nil, fmt.Errorf("load rules: %w", err)
	}
	// ruleID → (tokenType → []tier sorted by min_tokens)
	ruleIDs := make([]uint, 0, len(rules))
	for _, r := range rules {
		ruleIDs = append(ruleIDs, r.ID)
	}
	tierMap := map[uint]map[string][]model.PricingRuleTier{}
	if len(ruleIDs) > 0 {
		var allTiers []model.PricingRuleTier
		if err := s.db.WithContext(ctx).
			Where("rule_id IN ?", ruleIDs).
			Order("token_type ASC, min_tokens ASC").
			Find(&allTiers).Error; err != nil {
			return nil, fmt.Errorf("load tiers: %w", err)
		}
		for _, t := range allTiers {
			if tierMap[t.RuleID] == nil {
				tierMap[t.RuleID] = map[string][]model.PricingRuleTier{}
			}
			tierMap[t.RuleID][t.TokenType] = append(tierMap[t.RuleID][t.TokenType], t)
		}
	}

	// lookup: (serviceType, provider, model) → rule
	ruleIndex := map[string]*model.PricingRule{}
	for i := range rules {
		key := rules[i].ServiceType + "|" + rules[i].Provider + "|" + rules[i].Model
		ruleIndex[key] = &rules[i]
	}

	// 2. 批量读取目标时间范围的 usage_record（分批，每批 1000 条）
	result := &RecalculateResult{DryRun: dryRun}
	offset := 0
	batchSize := 1000

	type writeEntry struct {
		id         uint64
		newCost    int64
		newRevenue int64
	}

	calcTieredCents := func(tokens int, tiers []model.PricingRuleTier, sell bool) int64 {
		if tokens <= 0 {
			return 0
		}
		for _, t := range tiers {
			if uint(tokens) >= t.MinTokens && (t.MaxTokens == nil || uint(tokens) <= *t.MaxTokens) {
				price := t.CostPerMTok
				if sell {
					price = t.SellPerMTok
				}
				return int64(math.Round(float64(tokens) * price / 1_000_000 * 100))
			}
		}
		return 0
	}

	for {
		var records []model.UsageRecord
		err := s.db.WithContext(ctx).
			Where("created_at >= ? AND created_at < ?", from, to.AddDate(0, 0, 1)).
			Order("id ASC").
			Offset(offset).Limit(batchSize).
			Find(&records).Error
		if err != nil {
			return nil, fmt.Errorf("load records batch: %w", err)
		}
		if len(records) == 0 {
			break
		}

		var batchAffected, batchOldCost, batchNewCost int64
		var writes []writeEntry

		for _, rec := range records {
			key := rec.ServiceType + "|" + rec.Provider + "|" + rec.Model
			rule, ok := ruleIndex[key]
			if !ok {
				batchNewCost += rec.CostCents
				continue
			}
			batchOldCost += rec.CostCents
			rtiers := tierMap[rule.ID]

			newCost := calcTieredCents(rec.PromptTokens, rtiers["input"], false) +
				calcTieredCents(rec.CompletionTokens, rtiers["output"], false)
			newRevenue := calcTieredCents(rec.PromptTokens, rtiers["input"], true) +
				calcTieredCents(rec.CompletionTokens, rtiers["output"], true)
			batchNewCost += newCost
			batchAffected++
			writes = append(writes, writeEntry{id: rec.ID, newCost: newCost, newRevenue: newRevenue})
		}

		if !dryRun && len(writes) > 0 {
			txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				for _, w := range writes {
					if err := tx.Model(&model.UsageRecord{}).
						Where("id = ?", w.id).
						Updates(map[string]interface{}{
							"cost_cents":    w.newCost,
							"revenue_cents": w.newRevenue,
						}).Error; err != nil {
						return fmt.Errorf("update record %d: %w", w.id, err)
					}
				}
				return nil
			})
			if txErr != nil {
				return nil, txErr
			}
		}

		result.AffectedRecords += batchAffected
		result.OldTotalCostCents += batchOldCost
		result.NewTotalCostCents += batchNewCost
		offset += batchSize
	}

	result.DeltaCents = result.NewTotalCostCents - result.OldTotalCostCents
	return result, nil
}

// ListTierChangeLogs 查询等级变更日志（支持时间范围和分页）
func (s *billingStore) ListTierChangeLogs(ctx context.Context, filter TierChangeLogFilter) ([]TierChangeLogItem, int64, error) {
	var total int64
	countQuery := s.db.WithContext(ctx).Model(&model.TierChangeLog{})
	if filter.From != nil {
		countQuery = countQuery.Where("created_at >= ?", *filter.From)
	}
	if filter.To != nil {
		countQuery = countQuery.Where("created_at < ?", (*filter.To).AddDate(0, 0, 1))
	}
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count tier change logs: %w", err)
	}

	var items []TierChangeLogItem
	query := s.db.WithContext(ctx).
		Table("tier_change_log t").
		Select(`t.id, t.parent_user_id, p.nickname AS parent_nickname,
			t.sub_user_id, s.nickname AS sub_nickname,
			t.old_tier, t.new_tier, t.months,
			t.old_tier_expires, t.new_tier_expires, t.created_at`).
		Joins("LEFT JOIN `user` p ON p.id = t.parent_user_id").
		Joins("LEFT JOIN `user` s ON s.id = t.sub_user_id")

	if filter.From != nil {
		query = query.Where("t.created_at >= ?", *filter.From)
	}
	if filter.To != nil {
		query = query.Where("t.created_at < ?", (*filter.To).AddDate(0, 0, 1))
	}

	err := query.Order("t.created_at DESC").
		Offset(filter.Offset).Limit(filter.Limit).
		Scan(&items).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list tier change logs: %w", err)
	}
	return items, total, nil
}

// GetProviderModelID resolves the provider-specific model ID (provider_model_id) for the
// given logical model key and provider name. It joins ai_service → ai_service_route →
// llm_provider to find the matching row.
// Returns ("", gorm.ErrRecordNotFound) when no mapping exists.
func (s *billingStore) GetProviderModelID(ctx context.Context, modelKey, providerName string) (string, error) {
	var providerModelID string
	err := s.db.WithContext(ctx).
		Table("ai_service_route asr").
		Joins("JOIN ai_service ais ON ais.id = asr.model_id").
		Joins("JOIN llm_provider lp ON lp.id = asr.provider_id").
		Where("ais.model_key = ? AND lp.name = ?", modelKey, providerName).
		Select("asr.provider_model_id").
		Limit(1).
		Scan(&providerModelID).Error
	if err != nil {
		return "", fmt.Errorf("GetProviderModelID: %w", err)
	}
	if providerModelID == "" {
		// Return the sentinel bare; callers use errors.Is to detect not-found.
		return "", gorm.ErrRecordNotFound
	}
	return providerModelID, nil
}

// GetTierChangeStats 获取等级变更月度统计（用于计算收入）
func (s *billingStore) GetTierChangeStats(ctx context.Context, from, to time.Time) (*TierChangeStats, error) {
	stats := &TierChangeStats{}

	// Total count
	err := s.db.WithContext(ctx).Model(&model.TierChangeLog{}).
		Where("created_at >= ? AND created_at < ?", from, to.AddDate(0, 0, 1)).
		Count(&stats.TotalChanges).Error
	if err != nil {
		return nil, fmt.Errorf("count total: %w", err)
	}

	// Upgrades: free→trial, free→standard, free→premium, trial→standard, trial→premium, standard→premium
	err = s.db.WithContext(ctx).Model(&model.TierChangeLog{}).
		Where("created_at >= ? AND created_at < ?", from, to.AddDate(0, 0, 1)).
		Where("(old_tier = 'free' AND new_tier IN ('trial','standard','premium')) OR (old_tier = 'trial' AND new_tier IN ('standard','premium')) OR (old_tier = 'standard' AND new_tier = 'premium')").
		Count(&stats.Upgrades).Error
	if err != nil {
		return nil, fmt.Errorf("count upgrades: %w", err)
	}

	stats.Downgrades = stats.TotalChanges - stats.Upgrades

	// Breakdown by new_tier
	err = s.db.WithContext(ctx).Model(&model.TierChangeLog{}).
		Select("new_tier, COUNT(*) as count, COALESCE(SUM(months), 0) as total_months").
		Where("created_at >= ? AND created_at < ?", from, to.AddDate(0, 0, 1)).
		Group("new_tier").
		Scan(&stats.TierBreakdown).Error
	if err != nil {
		return nil, fmt.Errorf("tier breakdown: %w", err)
	}

	return stats, nil
}
