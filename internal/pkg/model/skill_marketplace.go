package model

import (
	"time"

	"gorm.io/datatypes"
)

// SkillMarketplace is a row in the cross-tenant Skill marketplace. Publisher uploads
// a sanitized Skill copy; subscribers see it and can clone into their own tenant.
// The sanitized_body_md is an independent snapshot — publisher's later edits to the
// source Skill do not affect this row.
type SkillMarketplace struct {
	ID                    uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	PublisherUserID       uint           `gorm:"type:int unsigned;not null;index:idx_marketplace_publisher,priority:1" json:"publisher_user_id"`
	SourceSkillID         uint           `gorm:"type:int unsigned;not null;index:idx_marketplace_source" json:"source_skill_id"`
	Name                  string         `gorm:"size:100;not null" json:"name"`
	Description           string         `gorm:"size:500;not null;default:''" json:"description"`
	WhenToUse             string         `gorm:"size:500;not null;default:''" json:"when_to_use"`
	SanitizedBodyMD       string         `gorm:"type:mediumtext;not null" json:"sanitized_body_md"`
	AllowedTools          datatypes.JSON `gorm:"type:json;not null;default:(JSON_ARRAY())" json:"allowed_tools"`
	CategoryTags          datatypes.JSON `gorm:"type:json;not null;default:(JSON_ARRAY())" json:"category_tags"`
	IsPublic              bool           `gorm:"type:tinyint(1);not null;default:1;index:idx_marketplace_publisher,priority:2" json:"is_public"`
	IsPlatformRecommended bool           `gorm:"type:tinyint(1);not null;default:0;index:idx_marketplace_recommended,priority:1" json:"is_platform_recommended"`
	SubscribeCount        uint           `gorm:"type:int unsigned;not null;default:0;index:idx_marketplace_recommended,priority:2" json:"subscribe_count"`
	CreatedAt             time.Time      `gorm:"not null;default:CURRENT_TIMESTAMP;index:idx_marketplace_recommended,priority:3" json:"created_at"`
	UpdatedAt             time.Time      `gorm:"not null;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

func (SkillMarketplace) TableName() string { return "skill_marketplace" }

// SkillSubscription records that one parent account (subscriber_user_id) has subscribed
// to one marketplace item (marketplace_id), and the clone lives in their tenant's skill
// table as cloned_skill_id. UNIQUE(subscriber_user_id, marketplace_id) prevents
// duplicate subscriptions.
type SkillSubscription struct {
	ID               uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	SubscriberUserID uint      `gorm:"type:int unsigned;not null;uniqueIndex:uk_subscription_user_marketplace,priority:1;index:idx_subscription_subscriber,priority:1" json:"subscriber_user_id"`
	MarketplaceID    uint      `gorm:"type:int unsigned;not null;uniqueIndex:uk_subscription_user_marketplace,priority:2;index:idx_subscription_marketplace" json:"marketplace_id"`
	ClonedSkillID    uint      `gorm:"type:int unsigned;not null" json:"cloned_skill_id"`
	SubscribedAt     time.Time `gorm:"not null;default:CURRENT_TIMESTAMP;index:idx_subscription_subscriber,priority:2,sort:desc" json:"subscribed_at"`
}

func (SkillSubscription) TableName() string { return "skill_subscription" }
