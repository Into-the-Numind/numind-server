package model

import (
	"gorm.io/gorm"
)

const FeatureKeySalesAgent = "sales_agent"

// UserFeaturePermission 用户功能权限表(白名单模式)
type UserFeaturePermission struct {
	gorm.Model
	ParentUserID uint   `gorm:"not null;index:idx_parent_sub" json:"parent_user_id"`
	SubUserID    uint   `gorm:"not null;index:idx_sub_feature;uniqueIndex:idx_sub_feature_unique" json:"sub_user_id"`
	FeatureKey   string `gorm:"type:varchar(64);not null;index:idx_sub_feature;uniqueIndex:idx_sub_feature_unique" json:"feature_key"`
}

func (UserFeaturePermission) TableName() string {
	return "user_feature_permission"
}
