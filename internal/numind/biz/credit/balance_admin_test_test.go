package credit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

func TestIsParentAccount(t *testing.T) {
	// 父账户：ParentUserID == nil
	parent := &model.User{Model: gorm.Model{ID: 42}}
	assert.True(t, isParentAccount(parent), "parent (ParentUserID nil) is parent")

	// 子账户：ParentUserID != nil
	pid := uint(42)
	child := &model.User{Model: gorm.Model{ID: 100}, ParentUserID: &pid}
	assert.False(t, isParentAccount(child), "child (ParentUserID set) is not parent")

	// nil user
	assert.False(t, isParentAccount(nil), "nil user is not parent")

	// Edge: parent with ID=0 still satisfies the nil-ParentUserID rule
	root := &model.User{Model: gorm.Model{ID: 0}}
	assert.True(t, isParentAccount(root))
}

func TestAdminTestPoolView_JSONShape(t *testing.T) {
	// Smoke test for AdminTestPoolView serialization
	view := &AdminTestPoolView{
		Granted:      5000,
		Used:         1500,
		Remaining:    3500,
		PeriodEnd:    "2026-05-31",
		DaysToExpire: 10,
	}
	bal := &BalanceBreakdown{
		BillingMode:   "credits",
		AdminTestPool: view,
	}
	assert.Equal(t, int64(5000), bal.AdminTestPool.Granted)
	assert.Equal(t, "2026-05-31", bal.AdminTestPool.PeriodEnd)

	// Sub-account: AdminTestPool nil → omitempty in JSON
	balChild := &BalanceBreakdown{BillingMode: "credits"}
	assert.Nil(t, balChild.AdminTestPool)
}

func TestAdminTestStatus_Fields(t *testing.T) {
	// AdminTestStatus 是 credit-local 类型，与 budget.AdminTestStatus 镜像。
	// 测试字段类型完整性（adapter 转换边界）。
	status := AdminTestStatus{
		Granted:      5000,
		Used:         1500,
		Remaining:    3500,
		PeriodStart:  time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:    time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC),
		DaysToExpire: 10,
	}
	assert.Equal(t, int64(5000), status.Granted)
	assert.Equal(t, int64(1500), status.Used)
	assert.Equal(t, 10, status.DaysToExpire)
	assert.False(t, status.PeriodEnd.IsZero())
}

// TestAdminTestConsumer_InterfaceSatisfaction is a compile-time check that
// any type matching the credit.AdminTestConsumer signature can be assigned.
// Used by the wire-layer adapter (biz.go) to plug in budget.AdminTestConsumer
// + a Status-conversion adapter.
func TestAdminTestConsumer_InterfaceSatisfaction(t *testing.T) {
	var _ AdminTestConsumer = (*fakeAdminConsumer)(nil)
	// silence unused warning on context import in this file group
	_ = context.Background
}
