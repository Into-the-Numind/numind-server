package middleware

import "context"

// ctxKey 是 context value 的类型安全 key，避免字符串冲突
type ctxKey string

const (
	// CtxKeyUserID 用户 ID 的 context key
	CtxKeyUserID ctxKey = "userID"
)

// NewContextWithUserID 将 userID 写入 context
func NewContextWithUserID(ctx context.Context, userID uint) context.Context {
	return context.WithValue(ctx, CtxKeyUserID, userID)
}

// UserIDFromCtx 从 context 中提取 userID，未找到返回 0, false
func UserIDFromCtx(ctx context.Context) (uint, bool) {
	uid, ok := ctx.Value(CtxKeyUserID).(uint)
	return uid, ok
}
