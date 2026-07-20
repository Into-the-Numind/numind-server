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
//   - 可信 lark_skill_read → 在最终封装硬上限内原子返回，避免完整命令说明与
//     references 被 persisted-output 预览截断
//   - 可信 file_read → 在 384 KiB 完整 JSON envelope 硬上限内原子返回，避免
//     64 KiB 内容页被通用 16 KiB artifact preview 二次截断
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
	// Generic artifact persistence is triggered above 16 KiB and leaves only a
	// 1 KiB model-visible preview. That can separate later instruction pages or
	// controlled references from the main guide. Only server-owned concrete
	// adapters get bounded atomic paths; same-named external/mock tools remain on
	// the ordinary artifact path.
	if adapter, ok := inner.(*fullToolEinoAdapter); ok {
		switch adapter.ft.(type) {
		case *larkSkillReadTool:
			return &boundedAtomicSkillTool{inner: inner}
		case *fileReadTool:
			return &boundedAtomicFileReadTool{inner: inner}
		}
	}
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

// larkSkillReadAtomicOutputLimit bounds the complete JSON envelope delivered
// to the model, not only aggregated SkillReadPage.Content. The trusted skill
// tool applies this same limit while it follows internal cursors; this wrapper
// remains an independent final-envelope defense.
const larkSkillReadAtomicOutputLimit = 64 << 10

// fileReadAtomicOutputLimit bounds the complete model-visible JSON envelope,
// not only its content field. A normal file_read page is at most 64 KiB; the
// larger envelope ceiling leaves room for JSON escaping and metadata while
// still preventing an unexpected implementation from flooding model context.
const fileReadAtomicOutputLimit = 384 << 10

type boundedAtomicSkillTool struct {
	inner einotool.InvokableTool
}

var _ einotool.InvokableTool = (*boundedAtomicSkillTool)(nil)

func (w *boundedAtomicSkillTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return w.inner.Info(ctx)
}

func (w *boundedAtomicSkillTool) InvokableRun(ctx context.Context, args string, opts ...einotool.Option) (string, error) {
	output, err := w.inner.InvokableRun(ctx, args, opts...)
	if err != nil {
		return output, err
	}
	if len(output) <= larkSkillReadAtomicOutputLimit {
		return output, nil
	}
	log.Warnw("boundedAtomicSkillTool: rejecting oversized skill envelope", "size", len(output))
	softError, _ := larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
	return string(softError), nil
}

type boundedAtomicFileReadTool struct {
	inner einotool.InvokableTool
}

var _ einotool.InvokableTool = (*boundedAtomicFileReadTool)(nil)

func (w *boundedAtomicFileReadTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return w.inner.Info(ctx)
}

func (w *boundedAtomicFileReadTool) InvokableRun(ctx context.Context, args string, opts ...einotool.Option) (string, error) {
	output, err := w.inner.InvokableRun(ctx, args, opts...)
	if err != nil {
		return output, err
	}
	if len(output) <= fileReadAtomicOutputLimit {
		return output, nil
	}
	log.Warnw("boundedAtomicFileReadTool: rejecting oversized file envelope", "size", len(output))
	softError, _ := (&fileReadTool{}).returnSoftError(
		"",
		"file_read output exceeds %d-byte atomic delivery limit; retry with a smaller limit_bytes",
		fileReadAtomicOutputLimit,
	)
	return string(softError), nil
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
