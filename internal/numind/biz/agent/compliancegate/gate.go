// Package compliancegate is a thin wire-layer adapter that connects the
// compliance package (no dependency on biz/agent) to *agent.RunHooks.
// Placed under biz/agent/compliancegate to import biz/agent without forming
// the circular dependency `agent ← compliance ← agent`.
//
// Decision rationale (parallel to #12 budgetgate): compliance package owns
// the 4-method ComplianceGate + 3-layer rule logic; compliancegate just
// wraps agent hooks around it.
package compliancegate

import (
	"context"

	einotool "github.com/cloudwego/eino/components/tool"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/compliance"
	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/numind/biz/permission"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// WrapHooks decorates base hooks with compliance checks at PreToolCall.
//
// PreToolCall order:
//  1. compliance.CheckToolCall → deny? → Record(HookActionPermissionDeny) +
//     sink narration + 短路（不调 base.PreToolCall）
//  2. allow → forward to base.PreToolCall
//
// PostToolCall: pass-through (compliance does not decide post-tool).
//
// 关键不变量：保留 base.Registry / NarrationProvider 透传
// （permission.WrapHooks / budgetgate.WrapHooks 也保留同样字段）。
//
// gate may be nil — returns base unchanged.
func WrapHooks(base *agent.RunHooks, gate compliance.ComplianceGate) *agent.RunHooks {
	if gate == nil {
		return base
	}
	return &agent.RunHooks{
		PreToolCall: func(ctx context.Context, t einotool.BaseTool, input string) (agent.HookAction, error) {
			req, err := buildRequest(ctx, t, input)
			if err != nil {
				log.Warnw("compliancegate.PreToolCall: buildRequest failed; compliance check skipped",
					"tool", toolName(ctx, t), "error", err)
				return forwardPre(ctx, base, t, input)
			}
			result, cerr := gate.CheckToolCall(ctx, req)
			if cerr != nil {
				// fail-open：compliance 出错不阻止工具调用
				log.Warnw("compliancegate.PreToolCall: CheckToolCall failed; fail-open",
					"tool", req.Tool.Name, "error", cerr)
				return forwardPre(ctx, base, t, input)
			}
			if result.Decision == model.DecisionDeny {
				detail := &agent.PermissionDenialDetail{
					ToolName:       req.Tool.Name,
					Behavior:       permission.BehaviorDeny,
					DecisionReason: "compliance:" + result.RuleLayer,
					ValidatorID:    "compliance",
					Message:        result.NarrationMsg,
				}
				// agent-security-hardening: feed the compliance deny reason to the per-run
				// soft-deny controller (independent of sink presence) so the adapter surfaces it.
				if sd := agent.SoftDenyFromCtx(ctx); sd != nil {
					sd.SetPending(detail)
				}
				if reg := registryFromBase(base); reg != nil {
					reg.Record(agent.HookActionPermissionDeny)
				}
				if sink := agent.PermissionSinkFromCtx(ctx); sink != nil {
					select {
					case sink <- detail:
					default:
						log.Warnw("compliancegate.PreToolCall: sink full",
							"agent_run_id", req.AgentRunID, "tool", req.Tool.Name)
					}
				}
				return agent.HookActionPermissionDeny, nil
			}
			return forwardPre(ctx, base, t, input)
		},
		PostToolCall: func(ctx context.Context, t einotool.BaseTool, output string, err error) (agent.HookAction, error) {
			if base != nil && base.PostToolCall != nil {
				return base.PostToolCall(ctx, t, output, err)
			}
			return agent.HookActionContinue, nil
		},
		Registry:          registryFromBase(base),
		NarrationProvider: narrationProviderFromBase(base),
	}
}

// buildRequest constructs a ComplianceRequest from ctx + tool + input.
// Converts agent.FullTool → compliance.ToolInfo to keep compliance package
// agent-free (per S2 spec §4.1 P0 import-cycle fix).
func buildRequest(ctx context.Context, t einotool.BaseTool, input string) (compliance.ComplianceRequest, error) {
	info, err := t.Info(ctx)
	if err != nil {
		return compliance.ComplianceRequest{}, err
	}
	runID := agent.RunIDFromContext(ctx)
	userID, _ := middleware.UserIDFromCtx(ctx)
	agentDefID, parentUserID := agent.AgentDefAndParentFromCtx(ctx)
	fullTool := agent.FullToolFromCtx(ctx, info.Name)
	toolInfo := compliance.ToolInfo{Name: info.Name}
	if fullTool != nil {
		toolInfo.IsDestructive = fullTool.IsDestructive()
	}
	return compliance.ComplianceRequest{
		AgentRunID:        runID,
		UserID:            userID,
		ParentUserID:      parentUserID,
		AgentDefinitionID: agentDefID,
		Tool:              toolInfo,
		InputJSON:         input,
	}, nil
}

func forwardPre(ctx context.Context, base *agent.RunHooks, t einotool.BaseTool, input string) (agent.HookAction, error) {
	if base != nil && base.PreToolCall != nil {
		return base.PreToolCall(ctx, t, input)
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

func toolName(ctx context.Context, t einotool.BaseTool) string {
	if i, err := t.Info(ctx); err == nil {
		return i.Name
	}
	return "?"
}
