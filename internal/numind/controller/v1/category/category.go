package category

import (
	"numind-server/internal/numind/biz"
)

type CategoryController struct {
	b biz.IBiz
}

func New(b biz.IBiz) *CategoryController {
	return &CategoryController{b: b}
}
