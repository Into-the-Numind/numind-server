package billing

import "context"

type billingCtxKey struct{}

// BillingCtx 计费上下文，通过 ctx 传递 userID、operation 和可选元数据
type BillingCtx struct {
	UserID    uint
	Operation string
	Meta      map[string]string
}

// WithBilling 在 context 中注入计费信息
func WithBilling(ctx context.Context, userID uint, operation string) context.Context {
	return context.WithValue(ctx, billingCtxKey{}, &BillingCtx{UserID: userID, Operation: operation})
}

// WithBillingMeta 在 context 中注入计费信息（含额外元数据）
func WithBillingMeta(ctx context.Context, userID uint, operation string, meta map[string]string) context.Context {
	return context.WithValue(ctx, billingCtxKey{}, &BillingCtx{UserID: userID, Operation: operation, Meta: meta})
}

// FromContext 从 context 中提取计费信息
func FromContext(ctx context.Context) *BillingCtx {
	if ctx == nil {
		return nil
	}
	bc, _ := ctx.Value(billingCtxKey{}).(*BillingCtx)
	return bc
}
