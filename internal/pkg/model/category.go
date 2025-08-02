package model

// CategoryM 文章分类表
type CategoryM struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:50;uniqueIndex;not null" json:"name"`
}

func (CategoryM) TableName() string {
	return "category"
}
