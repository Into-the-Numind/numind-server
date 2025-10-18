package model

import "time"

type ActionLogM struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	Action    string    `gorm:"size:100;not null" json:"action"`
	Target    string    `gorm:"size:100" json:"target"`
	TargetID  *uint     `json:"target_id,omitempty"`
	Detail    string    `gorm:"type:text" json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}

func (ActionLogM) TableName() string {
	return "action_log"
}
