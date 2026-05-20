package agent

import "context"

type permissionSinkKey struct{}

// WithPermissionSink 把 sink channel 存入 ctx（每 Run 一个 buffered size=1 chan）。
// runner.Run 在装配 ctx 时调；permission wrapper 在 deny 时通过 PermissionSinkFromCtx 取出 send detail。
func WithPermissionSink(ctx context.Context, sink chan<- *PermissionDenialDetail) context.Context {
	return context.WithValue(ctx, permissionSinkKey{}, sink)
}

// PermissionSinkFromCtx 取 sink；返回 nil 表示 ctx 未注入 sink。
func PermissionSinkFromCtx(ctx context.Context) chan<- *PermissionDenialDetail {
	s, _ := ctx.Value(permissionSinkKey{}).(chan<- *PermissionDenialDetail)
	return s
}
