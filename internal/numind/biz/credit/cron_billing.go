package credit

import (
	"context"
	"fmt"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// reconcileBillingMode 兜底对账：扫描 billing_mode='legacy_tier' 且有 active/pending
// subscription credit package 的用户，批量切换到 billing_mode='credits'。
//
// 背景（spec §3.8）：payment.fulfillOrder 在独立短事务中切 billing_mode，但
// 若切换失败（User 行锁争用 / DB 异常）则仅 log warn。本函数作为 daily cron
// 的兜底，确保用户不会永远留在 legacy_tier。
//
// 幂等：多次执行结果相同，仅 UPDATE 匹配的用户。
//
// 接线说明：此方法为独立 public 方法，在 Phase 2 Task 2.0 由 RunCronTasks
// 统一调用（不在 Track D 内修改 credit.go 以避免与 Track C 冲突）。
func (b *creditBiz) reconcileBillingMode(ctx context.Context) error {
	// 使用单条 SQL 批量切换：
	//   UPDATE user SET billing_mode='credits'
	//   WHERE billing_mode='legacy_tier'
	//     AND id IN (SELECT user_id FROM credit_package
	//                WHERE type='subscription' AND status IN ('active','pending'))
	result := b.ds.DB().WithContext(ctx).
		Exec(`UPDATE `+"`user`"+` SET billing_mode = ?
              WHERE billing_mode = ?
                AND id IN (
                    SELECT user_id FROM credit_package
                    WHERE type = ? AND status IN (?, ?)
                )`,
			model.BillingModeCredits,
			model.BillingModeLegacyTier,
			model.CreditTypeSubscription,
			model.CreditPackageActive,
			model.CreditPackagePending,
		)
	if result.Error != nil {
		return fmt.Errorf("reconcile billing_mode: %w", result.Error)
	}

	if result.RowsAffected > 0 {
		log.Infow("Reconciled legacy_tier users to credits billing mode",
			"user_count", result.RowsAffected)
	}
	return nil
}
