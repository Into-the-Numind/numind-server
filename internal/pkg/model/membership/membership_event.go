package membership

import "time"

type MembershipEvent struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID          uint64    `gorm:"not null;index:idx_event_user_occurred,priority:1" json:"user_id"`
	EventType       string    `gorm:"type:varchar(30);not null;index:idx_event_type_occurred,priority:1" json:"event_type"`
	ProductType     string    `gorm:"type:varchar(20);not null" json:"product_type"`
	Months          *uint8    `json:"months,omitempty"`
	Quantity        *uint16   `json:"quantity,omitempty"`
	AmountCents     int64     `gorm:"not null;default:0" json:"amount_cents"`
	Source          string    `gorm:"type:varchar(20);not null" json:"source"`
	GranterUserID   *uint64   `gorm:"index:idx_event_granter_occurred,priority:1" json:"granter_user_id,omitempty"`
	IdempotencyKey  *string   `gorm:"type:varchar(64);uniqueIndex:uniq_event_idempotency_key" json:"idempotency_key,omitempty"`
	SubscriptionID  *uint64   `json:"subscription_id,omitempty"`
	OccurredAt      time.Time `gorm:"type:datetime(0);not null;index:idx_event_user_occurred,priority:2;index:idx_event_granter_occurred,priority:2;index:idx_event_type_occurred,priority:2" json:"occurred_at"`
}

func (MembershipEvent) TableName() string { return "membership_event" }
