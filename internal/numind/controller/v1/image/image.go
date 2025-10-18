package image

import (
	"numind-server/internal/numind/biz"
)

type ImageController struct {
	b biz.IBiz
}

func New(b biz.IBiz) *ImageController {
	return &ImageController{b: b}
}
