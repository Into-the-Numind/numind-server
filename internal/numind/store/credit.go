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
type CreditStore interface {
	GetOrCreateAccount(ctx context.Context, userID uint) (*model.CreditAccount, error)
	GetBalance(ctx context.Context, userID uint) (int64, error)
	GetActivePackagesForUpdate(ctx context.Context, tx *gorm.DB, userID uint) ([]model.CreditPackage, error)
	UpdatePackage(ctx context.Context, tx *gorm.DB, pkg *model.CreditPackage) error
	CreatePackages(ctx context.Context, packages []model.CreditPackage) error
	ListPackagesByUser(ctx context.Context, userID uint, offset, limit int) ([]model.CreditPackage, int64, error)
	ListPackagesByOrder(ctx context.Context, orderID uint64) ([]model.CreditPackage, error)
	HasActiveSubscription(ctx context.Context, userID uint) (bool, error)
	HasTrialPackage(ctx context.Context, userID uint) (bool, error)
	GetUserTypeCreditMultiplier(ctx context.Context, userID uint) (float64, error)
	CreateTransaction(ctx context.Context, tx *gorm.DB, txn *model.CreditTransaction) error
	ListTransactionsByUser(ctx context.Context, userID uint, offset, limit int) ([]model.CreditTransaction, int64, error)
	UpdateBalance(ctx context.Context, tx *gorm.DB, userID uint, delta int64) error
	RecalculateBalance(ctx context.Context, userID uint) error
	ActivatePendingPackages(ctx context.Context) ([]uint, error)
	ExpireActivePackages(ctx context.Context) ([]uint, error)
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

// GetActivePackagesForUpdate 获取用户有效积分包并加行锁（用于扣减操作，FIFO）
func (s *creditStore) GetActivePackagesForUpdate(ctx context.Context, tx *gorm.DB, userID uint) ([]model.CreditPackage, error) {
	var packages []model.CreditPackage
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ? AND status = ?", userID, model.CreditPackageActive).
		Order("expires_at ASC").
		Find(&packages).Error
	return packages, err
}

// UpdatePackage 更新积分包（在事务中使用）
func (s *creditStore) UpdatePackage(ctx context.Context, tx *gorm.DB, pkg *model.CreditPackage) error {
	return tx.WithContext(ctx).Save(pkg).Error
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

// HasActiveSubscription 检查用户是否有有效的订阅积分包（active 或 pending）
func (s *creditStore) HasActiveSubscription(ctx context.Context, userID uint) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&model.CreditPackage{}).
		Where("user_id = ? AND type = ? AND status IN ?", userID, model.CreditTypeSubscription,
			[]string{model.CreditPackageActive, model.CreditPackagePending}).
		Count(&count).Error
	return count > 0, err
}

// HasTrialPackage 检查用户是否有体验积分包（不限状态）
func (s *creditStore) HasTrialPackage(ctx context.Context, userID uint) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&model.CreditPackage{}).
		Where("user_id = ? AND type = ?", userID, model.CreditTypeTrial).
		Count(&count).Error
	return count > 0, err
}

// GetUserTypeCreditMultiplier returns the credit burn-rate multiplier for a user.
// Rules (evaluated in order):
//  1. Active subscription → 1.0 (subscription users are not discounted).
//  2. Active trial package with remaining credits → look up credit_user_type_config for 'trial'.
//  3. All other cases → 1.0.
//
// Returns 1.0 on any store error so callers always get a safe default.
func (s *creditStore) GetUserTypeCreditMultiplier(ctx context.Context, userID uint) (float64, error) {
	hasSub, err := s.HasActiveSubscription(ctx, userID)
	if err != nil {
		return 1.0, fmt.Errorf("GetUserTypeCreditMultiplier: check subscription: %w", err)
	}
	if hasSub {
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

// ActivatePendingPackages 激活所有到期应生效的 pending 积分包，返回涉及的用户 ID 列表
func (s *creditStore) ActivatePendingPackages(ctx context.Context) ([]uint, error) {
	var userIDs []uint
	now := time.Now()

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 查询需要激活的积分包
		var packages []model.CreditPackage
		if err := tx.
			Where("status = ? AND activated_at <= ?", model.CreditPackagePending, now).
			Find(&packages).Error; err != nil {
			return err
		}

		if len(packages) == 0 {
			return nil
		}

		// 收集 ID 并批量更新状态
		ids := make([]uint64, 0, len(packages))
		seen := make(map[uint]struct{})
		for _, p := range packages {
			ids = append(ids, p.ID)
			if _, ok := seen[p.UserID]; !ok {
				seen[p.UserID] = struct{}{}
				userIDs = append(userIDs, p.UserID)
			}
		}

		return tx.Model(&model.CreditPackage{}).
			Where("id IN ?", ids).
			UpdateColumn("status", model.CreditPackageActive).Error
	})
	return userIDs, err
}

// ExpireActivePackages 过期所有已超出 expires_at 的 active 积分包，返回涉及的用户 ID 列表
func (s *creditStore) ExpireActivePackages(ctx context.Context) ([]uint, error) {
	var userIDs []uint
	now := time.Now()

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 查询需要过期的积分包
		var packages []model.CreditPackage
		if err := tx.
			Where("status = ? AND expires_at <= ?", model.CreditPackageActive, now).
			Find(&packages).Error; err != nil {
			return err
		}

		if len(packages) == 0 {
			return nil
		}

		// 收集 ID 并批量更新状态
		ids := make([]uint64, 0, len(packages))
		seen := make(map[uint]struct{})
		for _, p := range packages {
			ids = append(ids, p.ID)
			if _, ok := seen[p.UserID]; !ok {
				seen[p.UserID] = struct{}{}
				userIDs = append(userIDs, p.UserID)
			}
		}

		return tx.Model(&model.CreditPackage{}).
			Where("id IN ?", ids).
			UpdateColumn("status", model.CreditPackageExpired).Error
	})
	return userIDs, err
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

// GetQuotaBreakdown 获取用户额度分布（订阅 vs 加量包），只统计 active 状态的积分包
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
