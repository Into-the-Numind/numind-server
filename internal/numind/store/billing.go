package store

import (
	"context"
	"fmt"
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
	GetTiersByRuleID(ctx context.Context, ruleID uint64) ([]model.PricingRuleTier, error)
	// ReplaceTiers 全量替换某规则的分段（事务：DELETE + INSERT）
	ReplaceTiers(ctx context.Context, ruleID uint64, tiers []model.PricingRuleTier) error
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
		u.InputPricePerMTok == nil && u.OutputPricePerMTok == nil &&
		u.PricePerCall == nil && u.PricePerGB == nil &&
		u.SellInputPricePerMTok == nil && u.SellOutputPricePerMTok == nil &&
		u.SellPricePerCall == nil && u.SellPricePerGB == nil &&
		u.IsActive == nil
}

// UserConsumptionItem 用户消费排行项
type UserConsumptionItem struct {
	UserID    uint   `gorm:"column:user_id" json:"user_id"`
	Nickname  string `gorm:"column:nickname" json:"nickname"`
	Username  string `gorm:"column:username" json:"username"`
	CostCents int64  `gorm:"column:cost_cents" json:"cost_cents"`
	CallCount int64  `gorm:"column:call_count" json:"call_count"`
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

// GetPricingRule 获取匹配的定价规则（单次查询，精确匹配优先，fallback 到默认）
func (s *billingStore) GetPricingRule(ctx context.Context, serviceType, provider, modelName string) (*model.PricingRule, error) {
	var rule model.PricingRule
	err := s.db.WithContext(ctx).
		Where("service_type = ? AND provider = ? AND model IN (?, '') AND is_active = ?",
			serviceType, provider, modelName, true).
		Order("CASE WHEN model = '' THEN 1 ELSE 0 END").
		First(&rule).Error
	if err != nil {
		return nil, err
	}
	return &rule, nil
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
	return s.db.WithContext(ctx).Create(rule).Error
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
	if update.InputPricePerMTok != nil {
		updates["input_price_per_mtok"] = *update.InputPricePerMTok
	}
	if update.OutputPricePerMTok != nil {
		updates["output_price_per_mtok"] = *update.OutputPricePerMTok
	}
	if update.PricePerCall != nil {
		updates["price_per_call"] = *update.PricePerCall
	}
	if update.PricePerGB != nil {
		updates["price_per_gb"] = *update.PricePerGB
	}
	if update.SellInputPricePerMTok != nil {
		updates["sell_input_price_per_mtok"] = *update.SellInputPricePerMTok
	}
	if update.SellOutputPricePerMTok != nil {
		updates["sell_output_price_per_mtok"] = *update.SellOutputPricePerMTok
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
func (s *billingStore) GetTiersByRuleID(ctx context.Context, ruleID uint64) ([]model.PricingRuleTier, error) {
	var tiers []model.PricingRuleTier
	err := s.db.WithContext(ctx).
		Where("rule_id = ?", ruleID).
		Order("token_type ASC, min_tokens ASC").
		Find(&tiers).Error
	return tiers, err
}

// ReplaceTiers 全量替换某规则的分段（事务：DELETE + INSERT）
func (s *billingStore) ReplaceTiers(ctx context.Context, ruleID uint64, tiers []model.PricingRuleTier) error {
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
