package errno

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewErrnos(t *testing.T) {
	t.Run("ErrTrialAlreadyGranted", func(t *testing.T) {
		require.NotNil(t, ErrTrialAlreadyGranted)
		assert.Equal(t, 409, ErrTrialAlreadyGranted.HTTP)
		assert.Equal(t, "Trial.AlreadyGranted", ErrTrialAlreadyGranted.Code)
		assert.Equal(t, "该账户已使用过体验包", ErrTrialAlreadyGranted.Message)
	})

	t.Run("ErrTrialNotAllowedForActivePro", func(t *testing.T) {
		require.NotNil(t, ErrTrialNotAllowedForActivePro)
		assert.Equal(t, 409, ErrTrialNotAllowedForActivePro.HTTP)
		assert.Equal(t, "Trial.NotAllowedForActivePro", ErrTrialNotAllowedForActivePro.Code)
		assert.Equal(t, "已是 Pro 会员，不能再开通试用包", ErrTrialNotAllowedForActivePro.Message)
	})

	t.Run("ErrChildNotMember", func(t *testing.T) {
		require.NotNil(t, ErrChildNotMember)
		assert.Equal(t, 403, ErrChildNotMember.HTTP)
		assert.Equal(t, "Membership.ChildNotMember", ErrChildNotMember.Code)
		assert.Equal(t, "子账户当前不是会员", ErrChildNotMember.Message)
	})

	t.Run("ErrNotActiveMember", func(t *testing.T) {
		require.NotNil(t, ErrNotActiveMember)
		assert.Equal(t, 403, ErrNotActiveMember.HTTP)
		assert.Equal(t, "Membership.NotActiveMember", ErrNotActiveMember.Code)
		assert.Equal(t, "当前不是会员状态", ErrNotActiveMember.Message)
	})

	t.Run("ErrBoosterQuantityExceedsLimit", func(t *testing.T) {
		require.NotNil(t, ErrBoosterQuantityExceedsLimit)
		assert.Equal(t, 400, ErrBoosterQuantityExceedsLimit.HTTP)
		assert.Equal(t, "Booster.QuantityExceedsLimit", ErrBoosterQuantityExceedsLimit.Code)
		assert.Equal(t, "单次最多购买 10000 份", ErrBoosterQuantityExceedsLimit.Message)
	})

	t.Run("ErrSubscriptionExpired", func(t *testing.T) {
		require.NotNil(t, ErrSubscriptionExpired)
		assert.Equal(t, 410, ErrSubscriptionExpired.HTTP)
		assert.Equal(t, "Subscription.Expired", ErrSubscriptionExpired.Code)
		assert.Equal(t, "订阅已过期", ErrSubscriptionExpired.Message)
	})

	t.Run("ErrIdempotencyKeyConflict", func(t *testing.T) {
		require.NotNil(t, ErrIdempotencyKeyConflict)
		assert.Equal(t, 409, ErrIdempotencyKeyConflict.HTTP)
		assert.Equal(t, "Idempotency.KeyConflict", ErrIdempotencyKeyConflict.Code)
		assert.Equal(t, "幂等键冲突（同一 key 不同请求体）", ErrIdempotencyKeyConflict.Message)
	})

	t.Run("ErrSystemMaintenance", func(t *testing.T) {
		require.NotNil(t, ErrSystemMaintenance)
		assert.Equal(t, 503, ErrSystemMaintenance.HTTP)
		assert.Equal(t, "System.Maintenance", ErrSystemMaintenance.Code)
		assert.Equal(t, "系统维护中", ErrSystemMaintenance.Message)
	})
}
