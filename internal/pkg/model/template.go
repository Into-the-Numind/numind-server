package model

import "gorm.io/gorm"

type Template struct {
	gorm.Model
	File string `gorm:"type:text" json:"file" valid:"required"`
}

func (Template) TableName() string {
	return "template"
}
