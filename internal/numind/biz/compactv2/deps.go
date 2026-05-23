package compactv2

import (
	"context"
	"time"

	"numind-server/internal/pkg/model"
)

// Local store-shaped interfaces.
//
// 平行重做（D3）下 store 包已经 import compactv2 来引用 CompactStateV2 / MessageV2 等类型；
// 如果再让 compactv2 直接 import store 就会形成 import cycle。
//
// 解决：在本包定义"compactv2 需要的 store 行为子集"接口，让 caller（runner / biz / cmd）
// 把具体 store 注入进来。store.IAgentToolArtifactStore / store.IAgentRunStore 在 method
// 签名上完全满足这些接口（结构性接口约束），无需任何 adapter 代码。

// ArtifactStore is the subset of store.IAgentToolArtifactStore that the
// compactv2 package needs (Create + Get + ownership lookup + cleanup helpers).
type ArtifactStore interface {
	Create(ctx context.Context, art *model.AgentToolArtifact) error
	Get(ctx context.Context, uuid string) (*model.AgentToolArtifact, error)
	GetByToolCallID(ctx context.Context, runID uint64, toolCallID string) (*model.AgentToolArtifact, error)
	MarkExpired(ctx context.Context, uuid string) error
	ListExpiredBefore(ctx context.Context, cutoff time.Time, limit int) ([]model.AgentToolArtifact, error)
	DeleteBatch(ctx context.Context, uuids []string) error
}

// AgentRunReader is the subset of store.IAgentRunStore that
// read_tool_artifact needs (ownership check via agent_run.user_id).
type AgentRunReader interface {
	Get(ctx context.Context, id uint64) (*model.AgentRun, error)
}

// UserIDExtractor 是 ctx → user_id 的提取函数。
//
// compactv2 不能直接 import internal/pkg/middleware（middleware → store → compactv2 import cycle）。
// 调用方（runner / wiring 层）在构造 ReadArtifactTool 时注入 middleware.UserIDFromCtx 即可。
// 返回 ok=false 时 read_tool_artifact 视为 "not accessible"。
type UserIDExtractor func(ctx context.Context) (uint, bool)
