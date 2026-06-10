// Package budgetgate is a thin wire-layer adapter that connects the
// budget package (no dependency on biz/agent) to *agent.RunHooks.
// Placed under biz/agent/budgetgate to import biz/agent without forming
// the circular dependency `agent ← budget ← agent` that would happen if
// it lived in biz/budget directly.
//
// Decision rationale: budget package owns the 4-dim tracking + admin_test
// pool logic; budgetgate just wraps agent hooks around it.
package budgetgate

import (
	"context"
	"encoding/json"

	einotool "github.com/cloudwego/eino/components/tool"
	"gorm.io/datatypes"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/agent/callctx"
	"numind-server/internal/numind/biz/budget"
	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/pricing"
)

// UsageLookupable is an interface satisfied by *agent.aiserviceAdapter (and any
// test fake). It allows PostToolCall to pull the real LLM token Usage for the
// current call-id without importing the concrete adapter type, avoiding a
// circular import between budgetgate and the adapter.
//
// Wired in production by biz.go (M-A-wire): passes runner.go's einoAdapter
// as the UsageLookupable to WrapHooks via WithUsageLookup option.
type UsageLookupable interface {
	LookupUsage(callID string) (agent.Usage, bool)
}

// BudgetGate is the top-level entry the hook layer calls into for budget enforcement.
//
// Composition:
//
//	tracker       — 4-dim counters (BudgetTracker)
//	adminConsumer — admin_test pool (credit_service.ReserveAgentTest 透传给这里)
//	runStore      — write terminal_metadata when CanProceed=exceeded
type BudgetGate struct {
	tracker       budget.BudgetTracker
	adminConsumer budget.AdminTestConsumer
	runStore      IBudgetRunStore
}

// IBudgetRunStore is the minimal subset of store.IAgentRunStore needed by BudgetGate.
// Signature matches store.IAgentRunStore.UpdateTerminalMetadata for adapter-free wiring.
type IBudgetRunStore interface {
	UpdateTerminalMetadata(ctx context.Context, id uint64, metadata datatypes.JSON) error
}

// NewBudgetGate constructs a BudgetGate. All deps may be nil — wrapper will no-op
// gracefully (CanProceed always false, writeTerminalMetadata skip).
func NewBudgetGate(t budget.BudgetTracker, a budget.AdminTestConsumer, rs IBudgetRunStore) *BudgetGate {
	return &BudgetGate{tracker: t, adminConsumer: a, runStore: rs}
}

// Tracker exposes the BudgetTracker for runner.Run integration (Start/Close).
func (g *BudgetGate) Tracker() budget.BudgetTracker { return g.tracker }

// AdminConsumer exposes the AdminTestConsumer for credit_service injection.
func (g *BudgetGate) AdminConsumer() budget.AdminTestConsumer { return g.adminConsumer }

// WrapHooksOption configures optional behaviour of WrapHooks.
type WrapHooksOption func(*wrapHooksConfig)

type wrapHooksConfig struct {
	usageLookup UsageLookupable     // nil → fall back to legacy tokensFromOutput
	pricing     pricing.ICalculator // nil → conservative token→credit ratio fallback
}

// WithUsageLookup injects a UsageLookupable (typically the aiserviceAdapter) so
// that PostToolCall can read real LLM token counts via ctx call-id rather than
// parsing tool-output JSON. When nil or absent the legacy fallback is used.
func WithUsageLookup(a UsageLookupable) WrapHooksOption {
	return func(c *wrapHooksConfig) { c.usageLookup = a }
}

// WithPricingCalculator injects the platform pricing calculator so PostToolCall
// can convert raw LLM token usage into CREDITS before feeding the tracker —
// the MaxCredits / daily-credits dimensions are denominated in credits
// (agent_definition.credit_cap_per_session), never tokens. Without conversion
// the tracker compares token counts (thousands per call) against credit caps
// (hundreds per session) and kills every substantive run at its first tool
// call (dev run #113). When nil, a conservative fixed ratio is used instead.
func WithPricingCalculator(pc pricing.ICalculator) WrapHooksOption {
	return func(c *wrapHooksConfig) { c.pricing = pc }
}

// fallbackTokensPerCredit is the conservative token→credit conversion used when
// no pricing calculator is wired or the lookup fails (e.g. no pricing rule for
// the model). 500 tokens/credit overestimates cost for most cheap models —
// acceptable bias for an in-memory guardrail: it trips earlier, never later,
// and the authoritative three-pool deduction (Reserve/Reconcile) is unaffected.
const fallbackTokensPerCredit = 500

// creditsForUsage converts an LLM call's token usage into credits. Primary path
// uses the pricing calculator (cost cents == credits 1:1, same convention as the
// billing gateway); fallback divides total tokens by fallbackTokensPerCredit
// (ceil) so the guardrail still advances when pricing is unavailable.
func creditsForUsage(ctx context.Context, pc pricing.ICalculator, u agent.Usage) int {
	total := u.PromptTokens + u.CompletionTokens
	if total <= 0 {
		return 0
	}
	if pc != nil && u.Model != "" {
		if cents, err := pc.CalculateCost(ctx, "llm_chat", u.Provider, u.Model, u.PromptTokens, u.CompletionTokens); err == nil {
			return int(cents)
		}
		// fallthrough: no pricing rule / lookup failure → conservative ratio.
	}
	return (total + fallbackTokensPerCredit - 1) / fallbackTokensPerCredit
}

// WrapHooks decorates base hooks with budget checks.
//
// PreToolCall order:
//  1. tracker.CanProceed(runID) → exceeded? → Record(HookActionBudgetExceeded) +
//     async writeTerminalMetadata + 短路（不调 base.PreToolCall）
//  2. allow → forward to base.PreToolCall
//
// PostToolCall order:
//  1. forward to base.PostToolCall first（sandbox 关容器/写日志）
//  2. RecordUsage — primary: ctx call-id + UsageLookupable (real token counts
//     from aiservice, wired by M-A-wire via WithUsageLookup option), converted
//     tokens→credits via creditsForUsage before recording (tracker is
//     credit-denominated). Fallback: legacy tokensFromOutput, same conversion.
//
// 关键不变量：保留 base.Registry / NarrationProvider 透传
// （permission.WrapHooks 也保留同样字段以确保链式无丢失，#12 M11 同步补丁）。
func (g *BudgetGate) WrapHooks(base *agent.RunHooks, opts ...WrapHooksOption) *agent.RunHooks {
	if g == nil {
		return base
	}
	cfg := &wrapHooksConfig{}
	for _, o := range opts {
		o(cfg)
	}
	usageLookup := cfg.usageLookup // captured by PostToolCall closure
	pricingCalc := cfg.pricing     // captured by PostToolCall closure

	return &agent.RunHooks{
		PreToolCall: func(ctx context.Context, t einotool.BaseTool, input string) (agent.HookAction, error) {
			runID := agent.RunIDFromContext(ctx)
			if runID == 0 || g.tracker == nil {
				return forwardPre(ctx, base, t, input)
			}
			// agent-mode-billing T6: count this ReAct turn so the MaxTurns
			// dimension actually advances (RecordStep had no caller before).
			// Recorded before CanProceed so the Nth turn trips the limit.
			g.tracker.RecordStep(ctx, runID)
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
			// 2. RecordUsage
			runID := agent.RunIDFromContext(ctx)
			if runID != 0 && g.tracker != nil {
				// #14 A8b — primary path: read real LLM token counts via ctx call-id.
				// usageLookup is the aiserviceAdapter; wired by biz.go M-A-wire.
				recorded := false
				if usageLookup != nil {
					if callID := callctx.CallIDFromCtx(ctx); callID != "" {
						if usage, ok := usageLookup.LookupUsage(callID); ok {
							// Units invariant: the tracker is denominated in
							// CREDITS — convert tokens before recording.
							// recorded=true even when credits==0: the primary
							// source found the real usage; a zero-cents price
							// would yield 0 via the fallback too, so skipping
							// the legacy path is correct.
							if credits := creditsForUsage(ctx, pricingCalc, usage); credits > 0 {
								g.tracker.RecordUsage(ctx, runID, credits)
							}
							recorded = true
						}
					}
				}
				// Legacy fallback: parse {"usage":{"total_tokens":N}} from tool output.
				// Preserved for nil-adapter callers and pre-#14 tests that inject output JSON.
				// No model info here → creditsForUsage takes the ratio path.
				if !recorded {
					if tokens := tokensFromOutput(output); tokens > 0 {
						if credits := creditsForUsage(ctx, pricingCalc, agent.Usage{PromptTokens: tokens}); credits > 0 {
							g.tracker.RecordUsage(ctx, runID, credits)
						}
					}
				}
			}
			return action, baseErr
		},
		Registry:          registryFromBase(base),
		NarrationProvider: narrationProviderFromBase(base),
	}
}

func (g *BudgetGate) writeTerminalMetadata(ctx context.Context, runID uint64, dim budget.Dimension, detail map[string]any) {
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

// tokensFromOutput parses LLM token usage from tool output JSON if present.
// v1 simplification: looks for {"usage":{"total_tokens": N}} shape; returns 0 otherwise.
// Retained as the permanent legacy fallback — the primary path (#14/A8b,
// WithUsageLookup) takes precedence whenever a call-id stash is present.
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
