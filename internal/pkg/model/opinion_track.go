package model

import "gorm.io/gorm"

// OpinionTrack 系统内置观点赛道
type OpinionTrack struct {
	gorm.Model
	Slug        string `gorm:"size:50;uniqueIndex;not null" json:"slug"` // 赛道标识: overseas_property, insurance, overseas_ip, study_immigration
	Name        string `gorm:"size:100;not null" json:"name"`            // 显示名称
	Description string `gorm:"size:512" json:"description"`              // 赛道描述
	IsEnabled   bool   `gorm:"default:true" json:"is_enabled"`           // 是否启用
	SortOrder   int    `gorm:"default:0" json:"sort_order"`              // 排序权重
	DocID       uint   `gorm:"index" json:"doc_id"`                      // 关联的 KnowledgeDocument ID
}

// TableName 指定表名
func (OpinionTrack) TableName() string {
	return "opinion_track"
}
