package user

import (
	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/numind/store"
)

// UserController 是 user 模块在 Controller 层的实现，用来处理用户模块的请求.
type UserController struct {
	b             biz.IBiz
	membershipSvc *membership.MembershipService
}

// New 创建一个 user controller.
func New(ds store.IStore) *UserController {
	b := biz.B
	if b == nil {
		b = biz.NewBiz(ds)
	}
	return &UserController{b: b}
}

// WithMembershipSvc attaches a MembershipService to the controller.
func (ctrl *UserController) WithMembershipSvc(svc *membership.MembershipService) *UserController {
	ctrl.membershipSvc = svc
	return ctrl
}
