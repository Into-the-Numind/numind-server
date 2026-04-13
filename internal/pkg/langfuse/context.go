package langfuse

import "context"

type langfuseCtxKey struct{}

// TraceCtx Langfuse trace 上下文，通过 ctx 传递 traceID 和 parentObservationID
type TraceCtx struct {
	TraceID             string
	ParentObservationID string
	PromptName          string // 当前使用的 Langfuse prompt 名称（可选）
	PromptVersion       int    // 当前使用的 Langfuse prompt 版本（可选）
}

// WithTrace 在 context 中注入 Langfuse trace 信息
func WithTrace(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, langfuseCtxKey{}, &TraceCtx{TraceID: traceID})
}

// WithTraceAndParent 在 context 中注入 Langfuse trace + parent observation 信息
func WithTraceAndParent(ctx context.Context, traceID, parentObservationID string) context.Context {
	return context.WithValue(ctx, langfuseCtxKey{}, &TraceCtx{
		TraceID:             traceID,
		ParentObservationID: parentObservationID,
	})
}

// FromContext 从 context 中提取 Langfuse trace 信息
func FromContext(ctx context.Context) *TraceCtx {
	if ctx == nil {
		return nil
	}
	tc, _ := ctx.Value(langfuseCtxKey{}).(*TraceCtx)
	return tc
}
