package membership

import "time"

type TrialGrant struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID           uint64    `gorm:"uniqueIndex:uniq_trial_user_id;not null" json:"user_id"`
	GrantedAt        time.Time `gorm:"type:datetime(0);not null" json:"granted_at"`
	ExpiresAt        time.Time `gorm:"type:datetime(0);not null;index:idx_trial_expires_at" json:"expires_at"`
	CreditsRemaining int       `gorm:"not null;default:200" json:"credits_remaining"`
	Source           string    `gorm:"type:varchar(20);not null;default:'b2b_grant'" json:"source"`
	GranterUserID    *uint64   `gorm:"index:idx_trial_granter_expires" json:"granter_user_id,omitempty"`
	CreatedAt        time.Time `gorm:"type:datetime(0);not null" json:"created_at"`
}

func (TrialGrant) TableName() string { return "trial_grant" }
