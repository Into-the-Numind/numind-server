package membership

import "time"

type UserBoosterBalance struct {
	UserID           uint64    `gorm:"primaryKey" json:"user_id"`
	CreditsRemaining int64     `gorm:"not null;default:0" json:"credits_remaining"`
	UpdatedAt        time.Time `gorm:"type:datetime(0);not null;index:idx_booster_updated_at" json:"updated_at"`
}

func (UserBoosterBalance) TableName() string { return "user_booster_balance" }
