package credit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// 注：本文件在 package credit_test，经 svc(ICreditService) + 既有 helper 间接用被测包，
// 不直接 import biz/credit（无 credit.X 裸引用，避免 unused import 编译错误）。

func i64p(v int64) *int64 { return &v }

// 直接 seed credit_reservation 行，验证 biz 映射 / 过滤 / 分页归一化。
func TestListConsumptionLog_MappingFilterPaging(t *testing.T) {
	ctx := context.Background()
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := newCreditServiceWithMembership(ds, db, nil)

	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	seed := []model.CreditReservation{
		{UserID: 7, Operation: "sop_run", Status: "reconciled", ReservedCredits: 20, Delta: i64p(-2), ActualCostCents: i64p(18), CreatedAt: base},
		{UserID: 7, Operation: "weird_new_op", Status: "reconciled", ActualCostCents: i64p(5), CreatedAt: base.Add(time.Hour)},
		{UserID: 7, Operation: "sop_run", Status: "reserved", ActualCostCents: nil, CreatedAt: base.Add(2 * time.Hour)},
	}
	for i := range seed {
		require.NoError(t, db.Create(&seed[i]).Error)
	}

	items, total, err := svc.ListConsumptionLog(ctx, 7, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	require.Len(t, items, 2)
	// created_at DESC：weird_new_op 在前（未知 operation 回退裸值）
	assert.Equal(t, "weird_new_op", items[0].Action)
	assert.Equal(t, "weird_new_op", items[0].ActionLabel)
	assert.Equal(t, int64(5), items[0].Credits)
	// sop_run → 中文名，credits = actual_cost_cents
	assert.Equal(t, "sop_run", items[1].Action)
	assert.Equal(t, "SOP 执行", items[1].ActionLabel)
	assert.Equal(t, int64(18), items[1].Credits)

	// 分页归一化：page=0 / pageSize=0 → 视为 1 / 20，不报错
	_, _, err = svc.ListConsumptionLog(ctx, 7, 0, 0)
	require.NoError(t, err)
	// pageSize 上限 100：传 9999 不应报错
	_, _, err = svc.ListConsumptionLog(ctx, 7, 1, 9999)
	require.NoError(t, err)
}

// 跑真实 Reserve→Reconcile，断言展示 credits == actual_cost_cents == 账本净扣减绝对值。
func TestListConsumptionLog_LedgerTruth(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	svc, ds, rsv := setupReservation(t, 505, 120, []seedPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 1000, RemainCredits: 1000,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})
	require.NoError(t, svc.Reconcile(ctx, rsv.ID, 95)) // actual 95 < reserved 120 → 退 25；净 95

	items, total, err := svc.ListConsumptionLog(ctx, 505, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	assert.Equal(t, int64(95), items[0].Credits, "展示额 = actual_cost_cents (= reserved+delta)")

	var ledgerSum int64
	require.NoError(t, ds.DB().Model(&model.CreditTransaction{}).
		Where("user_id = ?", 505).
		Select("COALESCE(SUM(amount),0)").Scan(&ledgerSum).Error)
	assert.Equal(t, items[0].Credits, -ledgerSum, "展示额必须等于账本真实净扣减")
}
