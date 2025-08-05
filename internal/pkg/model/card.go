package model

import (
	"encoding/json"

	"gorm.io/gorm"
)

type CardM struct {
	gorm.Model
	UserID        uint   `gorm:"index;not null" json:"user_id"`   // 创建用户ID
	BookID        uint   `gorm:"index" json:"book_id"`            // 所属卡册ID
	ProcessedText string `gorm:"type:text" json:"processed_text"` // AI处理后的文本
	SortOrder     int    `gorm:"default:1" json:"sort_order"`     // 在卡册中的排序，从1开始
	Tags          string `gorm:"size:255" json:"tags"`            // 标签，逗号分隔

	// 关联关系
	// User  User   `gorm:"foreignKey:UserID;constraintName:fk_card_user_new" json:"user,omitempty"`
	// Book  BookM  `gorm:"foreignKey:BookID;constraintName:fk_card_book_new" json:"book,omitempty"`
}

func (CardM) TableName() string {
	return "cards"
}

// MarshalJSON 自定义JSON序列化，将ProcessedText解析为JSON对象
func (c *CardM) MarshalJSON() ([]byte, error) {
	type Alias CardM

	// 尝试解析ProcessedText为JSON
	var parsedData interface{}
	if c.ProcessedText != "" {
		if err := json.Unmarshal([]byte(c.ProcessedText), &parsedData); err == nil {
			// 如果解析成功，创建一个新的结构体，将解析后的数据作为processed_text返回
			alias := &struct {
				*Alias
				ProcessedText interface{} `json:"processed_text"`
			}{
				Alias:         (*Alias)(c),
				ProcessedText: parsedData,
			}
			return json.Marshal(alias)
		}
	}

	// 如果解析失败或为空，使用原始结构体
	return json.Marshal((*Alias)(c))
}
