package agent

import "context"

// AbortController 提供 Agent Runtime 的三层 ctx 派生 helper（spec §6）。
// 层级：queryCtx (整个 Run) → batchCtx (一次 LLM batch) → toolCtx (单工具调用)。
// cancel 严格父→子级联：父 cancel 时子立即 Done。

// DeriveQueryCtx 派生 query 层 ctx（顶层，包裹整个 Run）。
func DeriveQueryCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}

// DeriveBatchCtx 派生 batch 层 ctx（中层，包裹一次 LLM 调用 + streaming）。
func DeriveBatchCtx(query context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(query)
}

// DeriveToolCtx 派生 tool 层 ctx（底层，包裹单次工具调用）。
func DeriveToolCtx(batch context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(batch)
}
