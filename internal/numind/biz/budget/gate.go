package budget

import (
	"context"
	"encoding/json"

	einotool "github.com/cloudwego/eino/components/tool"
	"gorm.io/datatypes"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/pkg/log"
)

// BudgetGate is the top-level entry the hook layer calls into for budget enforcement.
//
// Composition:
//
//	tracker       — 4-dim counters (BudgetTracker)
//	adminConsumer — admin_test pool (credit_service.ReserveAgentTest 透传给这里)
//	runStore      — write terminal_metadata when CanProceed=exceeded
type BudgetGate struct {
	tracker       BudgetTracker
	adminConsumer AdminTestConsumer
	runStore      IBudgetRunStore
}

// IBudgetRunStore is the minimal subset of store.IAgentRunStore needed by BudgetGate.
// Signature matches store.IAgentRunStore.UpdateTerminalMetadata for adapter-free wiring.
type IBudgetRunStore interface {
	UpdateTerminalMetadata(ctx context.Context, id uint64, metadata datatypes.JSON) error
}

// NewBudgetGate constructs a BudgetGate. All deps may be nil — wrapper will no-op
// gracefully (CanProceed always false, writeTerminalMetadata skip).
func NewBudgetGate(t BudgetTracker, a AdminTestConsumer, rs IBudgetRunStore) *BudgetGate {
	return &BudgetGate{tracker: t, adminConsumer: a, runStore: rs}
}

// Tracker exposes the BudgetTracker for runner.Run integration (Start/Close).
func (g *BudgetGate) Tracker() BudgetTracker { return g.tracker }

// AdminConsumer exposes the AdminTestConsumer for credit_service injection.
func (g *BudgetGate) AdminConsumer() AdminTestConsumer { return g.adminConsumer }

// WrapHooks decorates base hooks with budget checks.
//
// PreToolCall order:
//  1. tracker.CanProceed(runID) → exceeded? → Record(HookActionBudgetExceeded) +
//     async writeTerminalMetadata + 短路（不调 base.PreToolCall）
//  2. allow → forward to base.PreToolCall
//
// PostToolCall order:
//  1. forward to base.PostToolCall first（sandbox 关容器/写日志）
//  2. RecordUsage（从 output 解析 token 数；v1 简化—若解析失败 noop，
//     不阻塞 base 返回）
//
// 关键不变量：保留 base.Registry / NarrationProvider / NarrationRunID 透传
// （permission.WrapHooks 也保留同样字段以确保链式无丢失，#12 M11 同步补丁）。
func (g *BudgetGate) WrapHooks(base *agent.RunHooks) *agent.RunHooks {
	if g == nil {
		return base
	}
	return &agent.RunHooks{
		PreToolCall: func(ctx context.Context, t einotool.BaseTool, input string) (agent.HookAction, error) {
			runID := agent.RunIDFromContext(ctx)
			if runID == 0 || g.tracker == nil {
				return forwardPre(ctx, base, t, input)
			}
			exceeded, dim, detail := g.tracker.CanProceed(ctx, runID)
			if exceeded {
				if reg := registryFromBase(base); reg != nil {
					reg.Record(agent.HookActionBudgetExceeded)
				}
				// Async writeTerminalMetadata；不阻塞 hook 返回。
				go g.writeTerminalMetadata(context.Background(), runID, dim, detail)
				return agent.HookActionBudgetExceeded, nil
			}
			return forwardPre(ctx, base, t, input)
		},
		PostToolCall: func(ctx context.Context, t einotool.BaseTool, output string, err error) (agent.HookAction, error) {
			// 1. forward to base first
			action, baseErr := forwardPost(ctx, base, t, output, err)
			// 2. RecordUsage — v1 simplification: token count typically 0 here
			//    (tool output ≠ LLM response). #14 will wire ctx-based tokens.
			runID := agent.RunIDFromContext(ctx)
			if runID != 0 && g.tracker != nil {
				if tokens := tokensFromOutput(output); tokens > 0 {
					g.tracker.RecordUsage(ctx, runID, tokens)
				}
			}
			return action, baseErr
		},
		Registry:          registryFromBase(base),
		NarrationProvider: narrationProviderFromBase(base),
		NarrationRunID:    narrationRunIDFromBase(base),
	}
}

func (g *BudgetGate) writeTerminalMetadata(ctx context.Context, runID uint64, dim Dimension, detail map[string]any) {
	if g.runStore == nil {
		return
	}
	meta := map[string]any{"budget_dimension": string(dim)}
	for k, v := range detail {
		meta[k] = v
	}
	b, err := json.Marshal(meta)
	if err != nil {
		log.Warnw("BudgetGate.writeTerminalMetadata: marshal failed", "agent_run_id", runID, "error", err)
		return
	}
	if err := g.runStore.UpdateTerminalMetadata(ctx, runID, datatypes.JSON(b)); err != nil {
		log.Warnw("BudgetGate.writeTerminalMetadata: update failed",
			"agent_run_id", runID, "dim", dim, "error", err)
	}
}

func forwardPre(ctx context.Context, base *agent.RunHooks, t einotool.BaseTool, input string) (agent.HookAction, error) {
	if base != nil && base.PreToolCall != nil {
		return base.PreToolCall(ctx, t, input)
	}
	return agent.HookActionContinue, nil
}

func forwardPost(ctx context.Context, base *agent.RunHooks, t einotool.BaseTool, output string, err error) (agent.HookAction, error) {
	if base != nil && base.PostToolCall != nil {
		return base.PostToolCall(ctx, t, output, err)
	}
	return agent.HookActionContinue, nil
}

func registryFromBase(base *agent.RunHooks) *agent.HookActionRegistry {
	if base == nil {
		return nil
	}
	return base.Registry
}

func narrationProviderFromBase(base *agent.RunHooks) *narration.Provider {
	if base == nil {
		return nil
	}
	return base.NarrationProvider
}

func narrationRunIDFromBase(base *agent.RunHooks) uint64 {
	if base == nil {
		return 0
	}
	return base.NarrationRunID
}

// tokensFromOutput parses LLM token usage from tool output JSON if present.
// v1 simplification: looks for {"usage":{"total_tokens": N}} shape; returns 0 otherwise.
// #14 will replace with ctx-based RecordUsage from aiservice adapter.
func tokensFromOutput(output string) int {
	if output == "" {
		return 0
	}
	var parsed struct {
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		return 0
	}
	return parsed.Usage.TotalTokens
}
