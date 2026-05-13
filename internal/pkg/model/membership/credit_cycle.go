package membership

import "time"

type CreditCycle struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID           uint64    `gorm:"uniqueIndex:uniq_cycle_user_start,priority:1;not null" json:"user_id"`
	SubscriptionID   uint64    `gorm:"not null" json:"subscription_id"`
	CycleStart       time.Time `gorm:"type:datetime(0);uniqueIndex:uniq_cycle_user_start,priority:2;not null" json:"cycle_start"`
	CycleEnd         time.Time `gorm:"type:datetime(0);not null;index:idx_cycle_user_end" json:"cycle_end"`
	CreditsGranted   int       `gorm:"not null;default:0" json:"credits_granted"`
	CreditsRemaining int       `gorm:"not null;default:0" json:"credits_remaining"`
	CreatedAt        time.Time `gorm:"type:datetime(0);not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"type:datetime(0);not null" json:"updated_at"`
}

func (CreditCycle) TableName() string { return "credit_cycle" }
