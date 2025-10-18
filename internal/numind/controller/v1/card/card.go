package card

import (
	"numind-server/internal/numind/biz"
)

type CardController struct {
	b biz.IBiz
}

func New(b biz.IBiz) *CardController {
	return &CardController{b: b}
}
