package membership

import "time"

type Subscription struct {
	ID                   uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID               uint64    `gorm:"uniqueIndex:uniq_sub_user_id;not null" json:"user_id"`
	FirstStartedAt       time.Time `gorm:"type:datetime(0);not null" json:"first_started_at"`
	CurrentStartedAt     time.Time `gorm:"type:datetime(0);not null" json:"current_started_at"`
	ExpiresAt            time.Time `gorm:"type:datetime(0);not null;index:idx_sub_expires_at;index:idx_sub_granter_expires,priority:2" json:"expires_at"`
	TotalMonthsPurchased int       `gorm:"not null" json:"total_months_purchased"`
	PlanType             string    `gorm:"type:varchar(20);not null;default:'monthly'" json:"plan_type"`
	CycleCredits         int       `gorm:"not null;default:2000" json:"cycle_credits"`
	Source               string    `gorm:"type:varchar(20);not null;default:'b2b_grant'" json:"source"`
	GranterUserID        *uint64   `gorm:"index:idx_sub_granter_expires,priority:1" json:"granter_user_id,omitempty"`
	CreatedAt            time.Time `gorm:"type:datetime(0);not null" json:"created_at"`
	UpdatedAt            time.Time `gorm:"type:datetime(0);not null" json:"updated_at"`
}

func (Subscription) TableName() string { return "subscription" }
