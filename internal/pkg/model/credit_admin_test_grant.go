package model

import "time"

// CreditAdminTestGrant — Agent 模式 #12 配置者试聊独立测试配额（每月赠送 5000，月底作废，不累积）。
//
// 蓝本 §4.3.8 + §8 第 11 表。
//
// 设计要点：
//   - 每父账户每月一条 grant 记录（uq_parent_period）
//   - 月底 last day 后 period_end < now 即失效
//   - lazy-create 行为：父账户首次试聊时通过 INSERT ... ON CONFLICT DO NOTHING 创建当月 row
//   - 不累积：上月未用余额不滚到当月
//   - 不 fallback：admin_test 耗尽不动正式订阅积分
type CreditAdminTestGrant struct {
	ID            uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	ParentUserID  uint   `gorm:"column:parent_user_id;not null;uniqueIndex:uq_parent_period,priority:1" json:"parent_user_id"`
	GrantedAmount uint32 `gorm:"column:granted_amount;type:int unsigned;not null;default:5000" json:"granted_amount"`
	UsedAmount    uint32 `gorm:"column:used_amount;type:int unsigned;not null;default:0" json:"used_amount"`
	// RemainingAmount 是 DB 生成列；GORM 标 "->;type:..." 仅读不写。
	// SQLite 测试场景下生成列行为依赖 driver；测试代码不依赖 DB 计算，用 Remaining() 方法。
	RemainingAmount int32      `gorm:"column:remaining_amount;->;type:int GENERATED ALWAYS AS (CAST(granted_amount AS SIGNED) - CAST(used_amount AS SIGNED)) STORED;index:idx_period_remaining,priority:2" json:"remaining_amount"`
	PeriodStart     time.Time  `gorm:"column:period_start;type:date;not null;uniqueIndex:uq_parent_period,priority:2" json:"period_start"`
	PeriodEnd       time.Time  `gorm:"column:period_end;type:date;not null;index:idx_period_remaining,priority:1" json:"period_end"`
	GrantedAt       time.Time  `gorm:"column:granted_at;type:datetime;not null;default:CURRENT_TIMESTAMP" json:"granted_at"`
	LastUsedAt      *time.Time `gorm:"column:last_used_at;type:datetime" json:"last_used_at,omitempty"`
}

func (CreditAdminTestGrant) TableName() string { return "credit_admin_test_grant" }

// Remaining returns the Go-computed remaining amount (safe for SQLite tests
// where the DB-side GENERATED column may not be populated by AutoMigrate).
func (g *CreditAdminTestGrant) Remaining() int64 {
	return int64(g.GrantedAmount) - int64(g.UsedAmount)
}
