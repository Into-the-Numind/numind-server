package models

type Tag struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:50;uniqueIndex" json:"name"`
}

func (Tag) TableName() string {
	return "tags"
}

type CardTag struct {
	CardID uint `gorm:"index" json:"card_id"`
	TagID  uint `gorm:"index" json:"tag_id"`
}

func (CardTag) TableName() string {
	return "card_tags"
}
