package model

import "gorm.io/gorm"

type Template struct {
	gorm.Model
	Name string `gorm:"size:50;not null;uniqueIndex" json:"name" valid:"required"`
	File string `gorm:"type:text;not null" json:"file" valid:"required"`
}

func (Template) TableName() string {
	return "template"
}
