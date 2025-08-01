package template

import (
	"numind-server/internal/numind/biz"
)

type TemplateController struct {
	b biz.IBiz
}

func New(b biz.IBiz) *TemplateController {
	return &TemplateController{b: b}
}
