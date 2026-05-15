package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/model"
)

// CreditStore 积分数据存储接口
//
// T6 (credits-cleanup): GetActivePackagesForUpdate, UpdatePackage,
// ActivatePendingPackages, ExpireActivePackages were deleted along with the
// legacy creditBiz.DeductCreditsTx chain. The new path
// (membership.MembershipService.DeductCreditsTx) reads/writes credit_cycle /
// user_booster_balance / trial_grant directly without touching credit_package.
type CreditStore interface {
	GetOrCreateAccount(ctx context.Context, userID uint) (*model.CreditAccount, error)
	GetBalance(ctx context.Context, userID uint) (int64, error)
	CreatePackages(ctx context.Context, packages []model.CreditPackage) error
	ListPackagesByUser(ctx context.Context, userID uint, offset, limit int) ([]model.CreditPackage, int64, error)
	ListPackagesByOrder(ctx context.Context, orderID uint64) ([]model.CreditPackage, error)
	HasActiveSubscription(ctx context.Context, userID uint) (bool, error)
	HasTrialPackage(ctx context.Context, userID uint) (bool, error)
	GetUserTypeCreditMultiplier(ctx context.Context, userID uint) (float64, error)
	ListUserTypeConfigs(ctx context.Context) ([]model.CreditUserTypeConfig, error)
	UpdateUserTypeConfig(ctx context.Context, userType string, updates map[string]interface{}) error
	CreateTransaction(ctx context.Context, tx *gorm.DB, txn *model.CreditTransaction) error
	ListTransactionsByUser(ctx context.Context, userID uint, offset, limit int) ([]model.CreditTransaction, int64, error)
	UpdateBalance(ctx context.Context, tx *gorm.DB, userID uint, delta int64) error
	RecalculateBalance(ctx context.Context, userID uint) error
	ListAllAccountsWithBalance(ctx context.Context, offset, limit int) ([]model.CreditAccount, int64, error)
	GetQuotaBreakdown(ctx context.Context, userID uint) (subTotal, subRemain, boosterTotal, boosterRemain int64, err error)
	GetLatestCreditExpiry(ctx context.Context, userID uint) (string, error)
	// GetMembershipStateBatch batch-fetches membership state for a list of userIDs in one query.
	// Returns a map keyed by userID.  Missing keys mean no active packages exist for that user.
	GetMembershipStateBatch(ctx context.Context, userIDs []uint) (map[uint]*MembershipStateRow, error)
}

// MembershipStateRow aggregated membership state for one user (used by GetMembershipStateBatch).
type MembershipStateRow struct {
	HasActiveTrial        bool
	HasActiveSubscription bool
	HasUsedTrial          bool   // any trial package ever (any status)
	TrialExpiresAt        string // RFC3339 date, empty if none
	SubscriptionExpiresAt string // RFC3339 date, empty if none
	SubRemain             int64  // subscription + trial remain credits (no booster)
}

type creditStore struct {
	db *gorm.DB
}

func newCreditStore(db *gorm.DB) CreditStore {
	return &creditStore{db: db}
}

// GetOrCreateAccount 获取或创建用户积分账户（并发安全）
func (s *creditStore) GetOrCreateAccount(ctx context.Context, userID uint) (*model.CreditAccount, error) {
	account := model.CreditAccount{
		UserID:  userID,
		Balance: 0,
		Status:  "active",
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

	var result model.CreditAccount
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&result).Error; err != nil {
		return nil, err
	}
	return &result, nil
}

// GetBalance 获取用户积分余额（缓存值）
// 用户未创建积分账户时返回 0（不报错）
func (s *creditStore) GetBalance(ctx context.Context, userID uint) (int64, error) {
	var account model.CreditAccount
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&account).Error
	if err == gorm.ErrRecordNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return account.Balance, nil
}

// CreatePackages 批量创建积分包
func (s *creditStore) CreatePackages(ctx context.Context, packages []model.CreditPackage) error {
	if len(packages) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Create(&packages).Error
}

// ListPackagesByUser 查询用户积分包列表（分页）
func (s *creditStore) ListPackagesByUser(ctx context.Context, userID uint, offset, limit int) ([]model.CreditPackage, int64, error) {
	var packages []model.CreditPackage
	var total int64

	// 使用独立的查询实例，避免 GORM 查询对象被污染
	countDB := s.db.WithContext(ctx).Model(&model.CreditPackage{}).Where("user_id = ?", userID)
	if err := countDB.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	findDB := s.db.WithContext(ctx).Where("user_id = ?", userID)
	if err := findDB.Order("created_at DESC").Offset(offset).Limit(limit).Find(&packages).Error; err != nil {
		return nil, 0, err
	}
	return packages, total, nil
}

// ListPackagesByOrder 查询指定订单关联的积分包
func (s *creditStore) ListPackagesByOrder(ctx context.Context, orderID uint64) ([]model.CreditPackage, error) {
	var packages []model.CreditPackage
	err := s.db.WithContext(ctx).Where("order_id = ?", orderID).Find(&packages).Error
	return packages, err
}

// hasActiveSubscriptionInner is the shared subscription-active probe used by both
// HasActiveSubscription (public, called by GrantMembership guard) and
// GetUserTypeCreditMultiplier (public, called by burn-rate adjustment).
//
// **Semantics contract**: returns true iff the user has a row in `subscription`
// whose `expires_at` is strictly in the future. Time source is Go time.Now()
// (not SQL NOW()) so MySQL prod and SQLite test environments agree.
//
// **Must stay in sync with HasActiveSubscription semantics** — anything that
// changes the predicate (e.g. adding a status column / source filter) must
// update both callers' expectations.
func (s *creditStore) hasActiveSubscriptionInner(ctx context.Context, userID uint) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Table("subscription").
		Where("user_id = ? AND expires_at > ?", userID, time.Now()).
		Count(&count).Error
	return count > 0, err
}

// HasActiveSubscription 检查用户是否有未过期的订阅。
// T4 重写：改为读 subscription 新表（而非旧 credit_package 表），
// 与 MembershipService.GrantTrial 的写路径对齐，确保 trial lifetime
// 保护在 GrantMembership 中仍然有效。
// 使用 Go time.Now() 而非 SQL NOW()，兼容 MySQL 和 SQLite（测试环境）。
func (s *creditStore) HasActiveSubscription(ctx context.Context, userID uint) (bool, error) {
	return s.hasActiveSubscriptionInner(ctx, userID)
}

// HasTrialPackage 检查用户是否曾经拥有过 trial grant（lifetime 唯一性检测）。
// T4 重写：改为读 trial_grant 新表（而非旧 credit_package 表），
// 确保 GrantMembership 的 trial 防重复保护在写新表路径下仍然有效。
// trial_grant 表每个 user_id UNIQUE，有行即代表 lifetime 已用。
func (s *creditStore) HasTrialPackage(ctx context.Context, userID uint) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Table("trial_grant").
		Where("user_id = ?", userID).
		Count(&count).Error
	return count > 0, err
}

// GetUserTypeCreditMultiplier returns the credit burn-rate multiplier for a user.
// Rules (evaluated in order):
//  1. Active subscription (new subscription table) → 1.0 (subscription users are not discounted).
//  2. Active trial package with remaining credits → look up credit_user_type_config for 'trial'.
//  3. All other cases → 1.0.
//
// NOTE (T4 review-round-1 DRY): Step 1 now delegates to hasActiveSubscriptionInner
// (the shared probe). HasActiveSubscription delegates to the same helper, so both
// public methods always agree on what "active subscription" means.
// Step 2 still reads `credit_package` during the transition period; it will be
// updated in T6 when legacy deduct is removed.
//
// Returns 1.0 on any store error so callers always get a safe default.
func (s *creditStore) GetUserTypeCreditMultiplier(ctx context.Context, userID uint) (float64, error) {
	// Step 1: check for active subscription via the shared probe.
	hasActive, err := s.hasActiveSubscriptionInner(ctx, userID)
	if err != nil {
		return 1.0, fmt.Errorf("GetUserTypeCreditMultiplier: check subscription: %w", err)
	}
	if hasActive {
		return 1.0, nil
	}

	var trialCount int64
	if err := s.db.WithContext(ctx).
		Model(&model.CreditPackage{}).
		Where("user_id = ? AND type = ? AND status = ? AND remain_credits > 0 AND expires_at > ?",
			userID, model.CreditTypeTrial, model.CreditPackageActive, time.Now()).
		Count(&trialCount).Error; err != nil {
		return 1.0, fmt.Errorf("GetUserTypeCreditMultiplier: check trial package: %w", err)
	}
	if trialCount == 0 {
		return 1.0, nil
	}

	var cfg model.CreditUserTypeConfig
	if err := s.db.WithContext(ctx).
		Where("user_type = ? AND is_active = ?", "trial", true).
		First(&cfg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 1.0, nil
		}
		return 1.0, fmt.Errorf("GetUserTypeCreditMultiplier: load config: %w", err)
	}
	if cfg.CreditMultiplier <= 0 {
		return 1.0, nil
	}
	return cfg.CreditMultiplier, nil
}

// ListUserTypeConfigs returns all rows in credit_user_type_config, ordered by user_type.
func (s *creditStore) ListUserTypeConfigs(ctx context.Context) ([]model.CreditUserTypeConfig, error) {
	var rows []model.CreditUserTypeConfig
	if err := s.db.WithContext(ctx).Order("user_type ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("ListUserTypeConfigs: %w", err)
	}
	return rows, nil
}

// UpdateUserTypeConfig updates the row identified by user_type with the supplied
// fields. Uses map[string]interface{} so zero-value bools (is_active=false) write
// correctly — see project CLAUDE.md / database.md §6 for the GORM default:true gotcha.
// Returns gorm.ErrRecordNotFound when no row matches user_type so callers can map
// to a 404 response.
func (s *creditStore) UpdateUserTypeConfig(ctx context.Context, userType string, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}
	res := s.db.WithContext(ctx).
		Model(&model.CreditUserTypeConfig{}).
		Where("user_type = ?", userType).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("UpdateUserTypeConfig: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// CreateTransaction 创建积分流水（在事务中使用）
func (s *creditStore) CreateTransaction(ctx context.Context, tx *gorm.DB, txn *model.CreditTransaction) error {
	return tx.WithContext(ctx).Create(txn).Error
}

// ListTransactionsByUser 查询用户积分流水列表（分页）
func (s *creditStore) ListTransactionsByUser(ctx context.Context, userID uint, offset, limit int) ([]model.CreditTransaction, int64, error) {
	var transactions []model.CreditTransaction
	var total int64

	// 使用独立的查询实例，避免 GORM 查询对象被污染
	countDB := s.db.WithContext(ctx).Model(&model.CreditTransaction{}).Where("user_id = ?", userID)
	if err := countDB.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	findDB := s.db.WithContext(ctx).Where("user_id = ?", userID)
	if err := findDB.Order("created_at DESC").Offset(offset).Limit(limit).Find(&transactions).Error; err != nil {
		return nil, 0, err
	}
	return transactions, total, nil
}

// UpdateBalance 更新用户积分账户余额（delta 可正可负，在事务中使用）
func (s *creditStore) UpdateBalance(ctx context.Context, tx *gorm.DB, userID uint, delta int64) error {
	return tx.WithContext(ctx).
		Model(&model.CreditAccount{}).
		Where("user_id = ?", userID).
		UpdateColumn("balance", gorm.Expr("balance + ?", delta)).Error
}

// RecalculateBalance 根据有效积分包重新计算并更新余额缓存
func (s *creditStore) RecalculateBalance(ctx context.Context, userID uint) error {
	return s.db.WithContext(ctx).Exec(
		"UPDATE credit_account SET balance = (SELECT COALESCE(SUM(remain_credits), 0) FROM credit_package WHERE user_id = ? AND status = ?) WHERE user_id = ?",
		userID, model.CreditPackageActive, userID,
	).Error
}

// ListAllAccountsWithBalance 查询所有积分账户（管理端，按余额降序，分页）
func (s *creditStore) ListAllAccountsWithBalance(ctx context.Context, offset, limit int) ([]model.CreditAccount, int64, error) {
	var accounts []model.CreditAccount
	var total int64

	// 使用独立的查询实例，避免 GORM 查询对象被污染
	countDB := s.db.WithContext(ctx).Model(&model.CreditAccount{})
	if err := countDB.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	findDB := s.db.WithContext(ctx).Model(&model.CreditAccount{})
	if err := findDB.Order("balance DESC").Offset(offset).Limit(limit).Find(&accounts).Error; err != nil {
		return nil, 0, err
	}
	return accounts, total, nil
}

// GetQuotaBreakdown 获取用户额度分布（订阅 vs 加量包），只统计 active 状态的积分包。
//
// **本方法仅服务 billing_mode=legacy_tier 历史用户**。Credits-mode 用户的余额在
// credit_cycle / user_booster_balance / trial_grant 表，由 MembershipService
// 管理（见 internal/numind/biz/membership/cycle.go + state.go）。Credits-mode
// 用户的 grant 路径（GrantOrRenewSubscription / GrantTrial）**不写 credit_package**，
// 因此对他们该方法返回 sub=0；调用方必须按 BillingMode 分流到 MembershipService
// .GetBalance（见 creditsImpl.GetBalance T6 实现）。
//
// Booster 字段仍兼容性地从 user_booster_balance 覆盖（老 fulfillOrder 复制过来），
// 但这一兼容路径在 credits-deduct-cycle-wiring 后已不再被业务 Reserve/Reconcile
// 路径直接消费。
func (s *creditStore) GetQuotaBreakdown(ctx context.Context, userID uint) (subTotal, subRemain, boosterTotal, boosterRemain int64, err error) {
	type result struct {
		Type          string
		TotalCredits  int64
		RemainCredits int64
	}
	var rows []result
	err = s.db.WithContext(ctx).
		Model(&model.CreditPackage{}).
		Select("type, SUM(total_credits) as total_credits, SUM(remain_credits) as remain_credits").
		Where("user_id = ? AND status = ?", userID, model.CreditPackageActive).
		Group("type").
		Find(&rows).Error
	if err != nil {
		return
	}
	for _, r := range rows {
		switch r.Type {
		case model.CreditTypeSubscription, model.CreditTypeTrial:
			subTotal += r.TotalCredits
			subRemain += r.RemainCredits
		case model.CreditTypeBooster:
			boosterTotal += r.TotalCredits
			boosterRemain += r.RemainCredits
		}
	}

	// Override booster fields with user_booster_balance (new schema) if exists.
	// 新表 user_booster_balance 只有 credits_remaining 一个字段（spec §2.4：永不过期、
	// 每用户单行），把它同时作为 total 和 remain — 前端 BoosterPurchaseCard 用
	// booster_total>0 来判断"是否开通 booster"，user_booster_balance 存在即视为有。
	var newBalance struct {
		CreditsRemaining int64
	}
	queryErr := s.db.WithContext(ctx).
		Table("user_booster_balance").
		Select("credits_remaining").
		Where("user_id = ?", userID).
		Scan(&newBalance).Error
	if queryErr == nil && newBalance.CreditsRemaining > 0 {
		boosterTotal = newBalance.CreditsRemaining
		boosterRemain = newBalance.CreditsRemaining
	}

	return
}

// GetLatestCreditExpiry 获取用户最晚的 active 额度包到期时间
func (s *creditStore) GetLatestCreditExpiry(ctx context.Context, userID uint) (string, error) {
	var pkg model.CreditPackage
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND status IN ?", userID, []string{model.CreditPackageActive, model.CreditPackagePending}).
		Order("expires_at DESC").
		First(&pkg).Error
	if err == gorm.ErrRecordNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return pkg.ExpiresAt.Format("2006-01-02"), nil
}

// GetMembershipStateBatch 批量获取多个用户的会员状态（一次 DB 查询），返回 map[userID]*MembershipStateRow。
// 用于 ListSubUsers 父账号列表场景，避免 N+1 问题。
// 对于 has_used_trial: trial 积分包（任意状态）存在即为 true。
// 对于 sub_remain: 统计 active 的 trial+subscription 包的 remain_credits 之和（不含 booster）。
// expires_at 取各类型 active 包中最晚到期时间。
func (s *creditStore) GetMembershipStateBatch(ctx context.Context, userIDs []uint) (map[uint]*MembershipStateRow, error) {
	if len(userIDs) == 0 {
		return map[uint]*MembershipStateRow{}, nil
	}

	type pkgRow struct {
		UserID        uint
		Type          string
		Status        string
		RemainCredits int64
		ExpiresAt     time.Time
	}

	// Fetch all packages for the given user IDs (active, exhausted, expired, pending — we need
	// has_used_trial across all statuses for trial; for state checks we filter in Go).
	var rows []pkgRow
	if err := s.db.WithContext(ctx).
		Model(&model.CreditPackage{}).
		Select("user_id, type, status, remain_credits, expires_at").
		Where("user_id IN ?", userIDs).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("GetMembershipStateBatch: %w", err)
	}

	now := time.Now()
	result := make(map[uint]*MembershipStateRow, len(userIDs))
	for _, r := range rows {
		m, ok := result[r.UserID]
		if !ok {
			m = &MembershipStateRow{}
			result[r.UserID] = m
		}

		// has_used_trial: any trial record ever
		if r.Type == model.CreditTypeTrial {
			m.HasUsedTrial = true
		}

		isActive := (r.Status == model.CreditPackageActive) && r.ExpiresAt.After(now)

		switch r.Type {
		case model.CreditTypeTrial:
			if isActive {
				m.HasActiveTrial = true
				if m.TrialExpiresAt == "" || r.ExpiresAt.Format("2006-01-02") > m.TrialExpiresAt {
					m.TrialExpiresAt = r.ExpiresAt.Format("2006-01-02")
				}
				m.SubRemain += r.RemainCredits
			}
		case model.CreditTypeSubscription:
			// B2B 年度方案在 credit_package 表里是 1 active + 11 pending 月段链。
			// HasActiveSubscription 看 active 或 pending（用户付费在期）；
			// SubscriptionExpiresAt 也要看 pending，否则年度会员的到期日会被
			// 截断成 active 段当月底，前端显示 5/6 月而非真实 2027 年底。
			// SubRemain 仍然只算 active 段（pending 段未激活不能用）。
			isActiveOrPending := isActive || r.Status == model.CreditPackagePending
			if isActiveOrPending {
				m.HasActiveSubscription = true
				if m.SubscriptionExpiresAt == "" || r.ExpiresAt.Format("2006-01-02") > m.SubscriptionExpiresAt {
					m.SubscriptionExpiresAt = r.ExpiresAt.Format("2006-01-02")
				}
			}
			if isActive {
				m.SubRemain += r.RemainCredits
			}
		}
	}

	return result, nil
}
