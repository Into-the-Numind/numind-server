package agent

import (
	"context"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"numind-server/internal/numind/biz/compactv2"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
)

// V1.5 板块 2 task 2.2 — L0 tool result 写盘 wrapper（V2 路径专用）。
//
// 决策：通过 wrap Eino InvokableTool 的方式实现 L0 处理 — InvokableRun 返回的字符串
// 就是 Eino 写入 messages 数组的 content。当 V2 启用时，我们拦截原工具输出：
//   - len(output) <= ToolArtifactSizeLimit → 原样返回，行为与 V1 完全一致
//   - 超阈值 → 调 compactv2.ProcessToolResult 写盘 + 元数据入库，返回
//     `<persisted-output ref="UUID" tool="..." size="...">PREVIEW...</persisted-output>` 引用
//
// 写盘/DB 失败时 compactv2 自动 fallback 到 inline 截断（不返回 error），保证 agent run
// 不被写盘问题打断 — 整套 L0 是"尽力而为"的优化，失败也不阻塞。
//
// V1 路径（run.UseCompactV2 == false）完全不经过本 wrapper，与现状零行为差异。

// wrapToolWithV2ArtifactProcessing wraps inner Eino tool so that the result
// is funnelled through compactv2.ProcessToolResult before Eino writes it to
// agent_run.messages.
//
// 参数：
//   - inner: 原工具 adapter（adaptFullToEinoTool 的输出）
//   - toolName: 工具固定名字（来自 FullTool.Name()）
//   - runID: 当前 agent run id（写到 agent_tool_artifact.agent_run_id）
//   - artifactStore: agent_tool_artifact 存取接口（来自 store.S.ToolArtifact()）
//   - dataDir: agent_artifacts 文件根目录的绝对路径
//
// artifactStore == nil 或 dataDir == "" → 走 V1 行为（直接返回 inner 的 output，不写盘）。
// runner.go 已在调用前判定 useCompactV2 = (...artifactStore != nil && artifactDir != "")，
// 但这里再次防御性 nil-check 让 wrapper 自身可单测。
func wrapToolWithV2ArtifactProcessing(
	inner einotool.InvokableTool,
	toolName string,
	runID uint64,
	artifactStore store.IAgentToolArtifactStore,
	dataDir string,
) einotool.InvokableTool {
	return &v2ArtifactWrappedTool{
		inner:    inner,
		toolName: toolName,
		runID:    runID,
		deps: compactv2.ArtifactDeps{
			Store:   artifactStore,
			DataDir: dataDir,
		},
	}
}

type v2ArtifactWrappedTool struct {
	inner    einotool.InvokableTool
	toolName string
	runID    uint64
	deps     compactv2.ArtifactDeps
}

var _ einotool.InvokableTool = (*v2ArtifactWrappedTool)(nil)

// Info delegates to inner.Info — wrapper does not change tool schema.
func (w *v2ArtifactWrappedTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return w.inner.Info(ctx)
}

// InvokableRun calls inner, then post-processes the output via L0 write-to-disk.
// On any processing failure (degraded Deps, etc), returns the original output —
// agent run must not be blocked by L0 artifact issues.
func (w *v2ArtifactWrappedTool) InvokableRun(ctx context.Context, args string, opts ...einotool.Option) (string, error) {
	output, err := w.inner.InvokableRun(ctx, args, opts...)
	if err != nil {
		// 工具本身报错 → 不处理 output（output 可能是 nil / 错误信息），直接透传
		return output, err
	}

	// 防御：deps 不完整时跳过 L0（行为退化为 V1）。runner 在调用 wrap 之前已经判定过，
	// 但这里再判一次便于本 wrapper 单测时只构造 inner 不构造 deps。
	if w.deps.Store == nil || w.deps.DataDir == "" {
		return output, nil
	}

	// fallback toolCallID — Eino 不在 InvokableRun 参数里给 tool_call_id。
	// agent runner 在 messages 序列化时由 Eino 自己处理 tool_call_id，所以这里用
	// 空串占位，artifact 的 (runID, "") 仍能写入。后续若 spec 强制 toolCallID 非空，
	// 由调用方注入 ctx value 即可（向 task 2.3/2.4 留扩展空间）。
	const toolCallIDFromCtx = ""

	content, perr := compactv2.ProcessToolResult(ctx, w.deps, w.runID, toolCallIDFromCtx, w.toolName, output)
	if perr != nil {
		log.Warnw("v2ArtifactWrappedTool: ProcessToolResult error; returning raw output",
			"tool", w.toolName, "run_id", w.runID, "error", perr)
		return output, nil
	}
	return content, nil
}
