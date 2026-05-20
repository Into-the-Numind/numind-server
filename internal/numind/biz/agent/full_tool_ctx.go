package agent

import "context"

type fullToolMapKey struct{}

// WithFullToolMap 把 name → FullTool 映射存入 ctx。runner.Run 装配 einoTools 时调；
// permission wrapper 用 FullToolFromCtx 反查 wrapper 内部包装前的 FullTool 取 IsDestructive / IsReadOnly 等元数据。
func WithFullToolMap(ctx context.Context, m map[string]FullTool) context.Context {
	return context.WithValue(ctx, fullToolMapKey{}, m)
}

// FullToolFromCtx 取某工具名对应的 FullTool；找不到返回 nil。
func FullToolFromCtx(ctx context.Context, name string) FullTool {
	m, _ := ctx.Value(fullToolMapKey{}).(map[string]FullTool)
	if m == nil {
		return nil
	}
	return m[name]
}
