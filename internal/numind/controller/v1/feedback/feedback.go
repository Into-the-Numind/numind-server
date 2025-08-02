package feedback

import (
	"numind-server/internal/numind/biz"
)

// FeedbackController 是 feedback 模块在 controller 层的实现.
type FeedbackController struct {
	b biz.IBiz
}

// New 创建一个 feedback controller.
func New(b biz.IBiz) *FeedbackController {
	return &FeedbackController{b: b}
}
