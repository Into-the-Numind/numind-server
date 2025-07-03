package book

import (
	"numind-server/internal/numind/biz"
)

type BookController struct {
	b biz.IBiz
}

func New(b biz.IBiz) *BookController {
	return &BookController{b: b}
}
