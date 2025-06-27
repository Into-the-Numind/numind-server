package user

import (
	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/store"
	"numind-server/pkg/auth"
)

// UserController 是 user 模块在 Controller 层的实现，用来处理用户模块的请求.
type UserController struct {
	a *auth.Authz
	b biz.IBiz
	//pb.UnimplementedMiniBlogServer
}

// New 创建一个 user controller.
func New(ds store.IStore, a *auth.Authz) *UserController {
	return &UserController{a: a, b: biz.NewBiz(ds)}
}

// 只保留未在其他文件实现的 handler 方法
