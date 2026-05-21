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
//
// T11 (credits-cleanup): credit_package table has been archived and dropped.
// credit_account.balance column has been dropped. Methods that depended on
// credit_package have been removed or migrated to read from new tables.
// Removed: CreatePackages, ListPackagesByOrder, UpdateBalance, RecalculateBalance,
// GetMembershipStateBatch (store layer, never wired up post-T11 — production code
// uses biz/membership/state.go::MembershipService.GetMembershipStateBatch instead).
// GetBalance now always returns 0 (column gone; credits-mode callers must use
// MembershipService.GetBalance).
// GetQuotaBreakdown now reads from credit_cycle + user_booster_balance + trial_grant.
// GetLatestCreditExpiry now reads from subscription + trial_grant.
// GetUserTypeCreditMultiplier now reads from trial_grant (not credit_package).
type CreditStore interface {
	GetOrCreateAccount(ctx context.Context, userID uint) (*model.CreditAccount, error)
	// GetBalance always returns 0 after T11 (credit_account.balance column dropped).
	// Credits-mode callers must use MembershipService.GetBalance instead.
	// Legacy-tier callers can use 0 safely (they have no credits pool).
	GetBalance(ctx context.Context, userID uint) (int64, error)
	HasActiveSubscription(ctx context.Context, userID uint) (bool, error)
	HasTrialPackage(ctx context.Context, userID uint) (bool, error)
	GetUserTypeCreditMultiplier(ctx context.Context, userID uint) (float64, error)
	ListUserTypeConfigs(ctx context.Context) ([]model.CreditUserTypeConfig, error)
	UpdateUserTypeConfig(ctx context.Context, userType string, updates map[string]interface{}) error
	CreateTransaction(ctx context.Context, tx *gorm.DB, txn *model.CreditTransaction) error
	ListTransactionsByUser(ctx context.Context, userID uint, offset, limit int) ([]model.CreditTransaction, int64, error)
	// SumByReservationIDs sums credit_transaction.amount (negated to positive = spent)
	// for each reservation ID in the input slice. Returns a map[reservationID]→totalSpent.
	// Used by student-facing agent run list endpoints to compute credits_used per run.
	// Missing IDs (no transactions yet) return 0 — not an error.
	SumByReservationIDs(ctx context.Context, reservationIDs []uint64) (map[uint64]int64, error)
	ListAllAccountsWithBalance(ctx context.Context, offset, limit int) ([]model.CreditAccount, int64, error)
	GetQuotaBreakdown(ctx context.Context, userID uint) (subTotal, subRemain, boosterTotal, boosterRemain int64, err error)
	GetLatestCreditExpiry(ctx context.Context, userID uint) (string, error)
}

type creditStore struct {
	db *gorm.DB
}

func newCreditStore(db *gorm.DB) CreditStore {
	return &creditStore{db: db}
}

// GetOrCreateAccount 获取或创建用户积分账户（并发安全）
// T11: Balance field removed from CreditAccount (credit_account.balance column dropped).
func (s *creditStore) GetOrCreateAccount(ctx context.Context, userID uint) (*model.CreditAccount, error) {
	account := model.CreditAccount{
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

	var result model.CreditAccount
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&result).Error; err != nil {
		return nil, err
	}
	return &result, nil
}

// GetBalance always returns 0 after T11.
//
// T11 (credits-cleanup): credit_account.balance column has been dropped.
// The three-pool SOT (credit_cycle + user_booster_balance + trial_grant) is the
// authoritative balance for credits-mode users — use MembershipService.GetBalance instead.
// Legacy-tier users have no credits pool, so 0 is correct for them.
func (s *creditStore) GetBalance(_ context.Context, _ uint) (int64, error) {
	return 0, nil
}

// T9: ListPackagesByUser deleted — credit_package reader retired.
// T11: CreatePackages and ListPackagesByOrder deleted — credit_package table dropped.

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
//  1. Active subscription (subscription table) → 1.0 (subscription users are not discounted).
//  2. Active trial grant with remaining credits (trial_grant table) → look up credit_user_type_config for 'trial'.
//  3. All other cases → 1.0.
//
// T11 (credits-cleanup): Step 2 now reads trial_grant (not credit_package which was dropped).
// trial_grant.credits_remaining > 0 AND expires_at > now means user has active trial credits.
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

	// Step 2: check for active trial with remaining credits in trial_grant (T11: replaces credit_package query).
	var trialCount int64
	if err := s.db.WithContext(ctx).
		Table("trial_grant").
		Where("user_id = ? AND credits_remaining > 0 AND expires_at > ?", userID, time.Now()).
		Count(&trialCount).Error; err != nil {
		return 1.0, fmt.Errorf("GetUserTypeCreditMultiplier: check trial grant: %w", err)
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

// SumByReservationIDs returns the total credits spent (sum of negative amounts, negated)
// for a batch of reservation IDs. Each reservation is referenced in credit_transaction as
// biz_ref_type='reservation', biz_ref_id=<id string>.
// Missing IDs (no rows yet) map to 0 — not an error. Debit rows have negative amount;
// we negate so the result is a positive "credits_used" count.
func (s *creditStore) SumByReservationIDs(ctx context.Context, reservationIDs []uint64) (map[uint64]int64, error) {
	result := make(map[uint64]int64, len(reservationIDs))
	if len(reservationIDs) == 0 {
		return result, nil
	}
	// Convert IDs to strings for biz_ref_id comparison (model stores biz_ref_id as VARCHAR).
	strIDs := make([]string, len(reservationIDs))
	for i, id := range reservationIDs {
		strIDs[i] = fmt.Sprintf("%d", id)
	}
	type row struct {
		BizRefID string `gorm:"column:biz_ref_id"`
		Total    int64  `gorm:"column:total"`
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Model(&model.CreditTransaction{}).
		Select("biz_ref_id, SUM(amount) AS total").
		Where("biz_ref_type = ? AND biz_ref_id IN ?", "reservation", strIDs).
		Group("biz_ref_id").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("creditStore.SumByReservationIDs: %w", err)
	}
	for _, r := range rows {
		var id uint64
		if _, err := fmt.Sscanf(r.BizRefID, "%d", &id); err != nil {
			continue
		}
		// amount values are negative (deductions); negate to get positive spent count.
		total := -r.Total
		if total < 0 {
			total = 0
		}
		result[id] = total
	}
	return result, nil
}

// T11 (credits-cleanup): UpdateBalance and RecalculateBalance have been deleted.
// credit_account.balance column was dropped in migration 20260515_200000_t11.
// The three-pool SOT (credit_cycle + user_booster_balance + trial_grant) is the
// authoritative balance. Use MembershipService.GetBalance for credits-mode users.

// ListAllAccountsWithBalance 查询所有积分账户（管理端，分页）。
//
// T11 (credits-cleanup): credit_account.balance column has been dropped.
// Ordering is now by user_id ASC (stable order) instead of balance DESC.
// Callers that need balance should use MembershipService.GetBalance per user.
func (s *creditStore) ListAllAccountsWithBalance(ctx context.Context, offset, limit int) ([]model.CreditAccount, int64, error) {
	var accounts []model.CreditAccount
	var total int64

	countDB := s.db.WithContext(ctx).Model(&model.CreditAccount{})
	if err := countDB.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	findDB := s.db.WithContext(ctx).Model(&model.CreditAccount{})
	if err := findDB.Order("user_id ASC").Offset(offset).Limit(limit).Find(&accounts).Error; err != nil {
		return nil, 0, err
	}
	return accounts, total, nil
}

// GetQuotaBreakdown 获取用户额度分布（订阅 vs 加量包）。
//
// T11 (credits-cleanup): credit_package table has been dropped. This method
// now reads directly from the three-pool SOT:
//   - sub/trial pool → credit_cycle (active cycle) + trial_grant (active trial)
//   - booster pool   → user_booster_balance (permanent aggregate row)
//
// For credits-mode users: this reflects the live membership tables.
// For legacy_tier users: all pools return 0 (they have no credits).
func (s *creditStore) GetQuotaBreakdown(ctx context.Context, userID uint) (subTotal, subRemain, boosterTotal, boosterRemain int64, err error) {
	// Sub/trial pool: read active credit_cycle for subscription users.
	var cycleRemain int64
	if cycleErr := s.db.WithContext(ctx).
		Table("credit_cycle").
		Select("COALESCE(SUM(credits_remaining), 0)").
		Where("user_id = ? AND cycle_end > ?", userID, time.Now()).
		Scan(&cycleRemain).Error; cycleErr != nil {
		err = fmt.Errorf("GetQuotaBreakdown: credit_cycle: %w", cycleErr)
		return
	}

	// Sub/trial pool: read active trial_grant.
	var trialRemain int64
	if trialErr := s.db.WithContext(ctx).
		Table("trial_grant").
		Select("COALESCE(credits_remaining, 0)").
		Where("user_id = ? AND expires_at > ?", userID, time.Now()).
		Scan(&trialRemain).Error; trialErr != nil {
		err = fmt.Errorf("GetQuotaBreakdown: trial_grant: %w", trialErr)
		return
	}

	subTotal = cycleRemain + trialRemain
	subRemain = subTotal

	// Booster pool: user_booster_balance (永不过期、每用户单行).
	// credits_remaining serves as both total and remain — front-end uses booster_total>0
	// to determine if booster has ever been purchased.
	var boosterBalance struct {
		CreditsRemaining int64
	}
	if boosterErr := s.db.WithContext(ctx).
		Table("user_booster_balance").
		Select("credits_remaining").
		Where("user_id = ?", userID).
		Scan(&boosterBalance).Error; boosterErr != nil {
		err = fmt.Errorf("GetQuotaBreakdown: user_booster_balance: %w", boosterErr)
		return
	}
	boosterTotal = boosterBalance.CreditsRemaining
	boosterRemain = boosterBalance.CreditsRemaining
	return
}

// GetLatestCreditExpiry 获取用户最晚的有效额度到期时间。
//
// T11 (credits-cleanup): credit_package table has been dropped. Now reads from
// subscription (for sub expiry) and trial_grant (for trial expiry), returning
// whichever is latest. Returns "" if the user has no active membership.
func (s *creditStore) GetLatestCreditExpiry(ctx context.Context, userID uint) (string, error) {
	now := time.Now()
	var latestExpiry time.Time

	// Check subscription expiry.
	// Scan() never returns ErrRecordNotFound (it returns zero rows affected),
	// so a simple err check suffices — the gorm.ErrRecordNotFound guard was dead code.
	var subExpiry struct{ ExpiresAt time.Time }
	if err := s.db.WithContext(ctx).
		Table("subscription").
		Select("expires_at").
		Where("user_id = ? AND expires_at > ?", userID, now).
		Order("expires_at DESC").
		Limit(1).
		Scan(&subExpiry).Error; err != nil {
		return "", fmt.Errorf("GetLatestCreditExpiry: subscription: %w", err)
	}
	if !subExpiry.ExpiresAt.IsZero() && subExpiry.ExpiresAt.After(latestExpiry) {
		latestExpiry = subExpiry.ExpiresAt
	}

	// Check trial_grant expiry.
	var trialExpiry struct{ ExpiresAt time.Time }
	if err := s.db.WithContext(ctx).
		Table("trial_grant").
		Select("expires_at").
		Where("user_id = ? AND expires_at > ?", userID, now).
		Limit(1).
		Scan(&trialExpiry).Error; err != nil {
		return "", fmt.Errorf("GetLatestCreditExpiry: trial_grant: %w", err)
	}
	if !trialExpiry.ExpiresAt.IsZero() && trialExpiry.ExpiresAt.After(latestExpiry) {
		latestExpiry = trialExpiry.ExpiresAt
	}

	if latestExpiry.IsZero() {
		return "", nil
	}
	return latestExpiry.Format("2006-01-02"), nil
}

// T11 (credits-cleanup): the previous store-layer GetMembershipStateBatch and its
// MembershipStateRow DTO have been removed. They were a leftover from the
// credit_package era. Production code uses biz/membership/state.go::
// MembershipService.GetMembershipStateBatch instead (called from biz/customer/customer.go).
// No tests, callers, or routes referenced the store-layer version.
