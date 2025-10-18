package model

import (
	"time"

	"gorm.io/gorm"
)

type Order struct {
	gorm.Model
	UserID      uint      `gorm:"index;not null" json:"user_id"`
	OutTradeNo  string    `gorm:"size:64;not null;unique" json:"out_trade_no"`
	Amount      int64     `gorm:"not null" json:"amount"`
	Description string    `gorm:"size:255" json:"description"`
	Status      string    `gorm:"size:32;not null" json:"status"` // pending, paid, failed
	PaidAt      time.Time `json:"paid_at"`
}
