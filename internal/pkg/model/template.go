package model

import "gorm.io/gorm"

type Template struct {
	gorm.Model
	Name string `gorm:"size:50;uniqueIndex" json:"name" valid:"required,length(1|50)"`
	File string `gorm:"type:text" json:"file" valid:"required"`
}

func (Template) TableName() string {
	return "template"
}
