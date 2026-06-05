package permission

import (
	"context"
	"encoding/json"

	einotool "github.com/cloudwego/eino/components/tool"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// WrapHooks 把 base hooks（通常是 SandboxHookManager.AsRunHooks()）包成 permission-aware hooks。
//
// PreToolCall 顺序：
//
//  1. permission.Check
//  2. deny / ask → Registry.Record(HookActionPermissionDeny) + sink send + 短路返回（不调 base.PreToolCall）
//  3. allow → 透传 base.PreToolCall（sandbox 启动容器；UpdatedInput 透传 framework 就绪）
//
// PostToolCall 透传 base.PostToolCall（permission 不在 post 做决策）。
//
// Registry 字段透传 base.Registry；若 base nil，wrapper 不创建（runner.Run auto-inject）。
func WrapHooks(base *agent.RunHooks, gate *PermissionGate) *agent.RunHooks {
	return &agent.RunHooks{
		PreToolCall: func(ctx context.Context, t einotool.BaseTool, input string) (agent.HookAction, error) {
			req, err := buildRequest(ctx, t, input)
			if err != nil {
				log.Warnw("WrapHooks.PreToolCall: buildRequest failed; permission check skipped",
					"tool", toolName(ctx, t),
					"error", err)
				return forwardPre(ctx, base, t, input)
			}

			result := gate.Check(ctx, req)

			switch result.Behavior {
			case BehaviorDeny, BehaviorAsk:
				detail := &agent.PermissionDenialDetail{
					ToolName:       req.Tool.Name(),
					Behavior:       result.Behavior,
					DecisionReason: string(result.DecisionReason),
					ValidatorID:    result.ValidatorID,
					Message:        result.Message,
				}
				// agent-security-hardening: feed the deny reason to the per-run soft-deny
				// controller (independent of sink presence) so the adapter surfaces it to the LLM.
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
						log.Warnw("WrapHooks.PreToolCall: sink full",
							"agent_run_id", req.AgentRunID,
							"tool", req.Tool.Name())
					}
				}
				return agent.HookActionPermissionDeny, nil

			case BehaviorAllow, BehaviorPassthrough:
				effectiveInput := input
				if result.UpdatedInput != nil {
					if b, merr := json.Marshal(result.UpdatedInput); merr == nil {
						effectiveInput = string(b)
					} else {
						log.Warnw("WrapHooks.PreToolCall: UpdatedInput marshal failed; using original input",
							"tool", req.Tool.Name(),
							"error", merr)
					}
				}
				log.Infow("WrapHooks.PreToolCall: dispatching tool execution",
					"agent_run_id", req.AgentRunID,
					"tool_name", toolName(ctx, t),
					"input_json", effectiveInput)
				return forwardPre(ctx, base, t, effectiveInput)

			default:
				log.Warnw("WrapHooks.PreToolCall: unknown behavior; fail-open",
					"behavior", result.Behavior,
					"tool", req.Tool.Name())
				return forwardPre(ctx, base, t, input)
			}
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

// narrationProviderFromBase preserves base.NarrationProvider through the
// permission wrapper so downstream (e.g. budget wrapper, sandbox base, adapter)
// can still emit narration events. #12 agent-mode-billing-integration P1-2 fix.
func narrationProviderFromBase(base *agent.RunHooks) *narration.Provider {
	if base == nil {
		return nil
	}
	return base.NarrationProvider
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

// buildRequest 从 ctx + tool + input 构造 PermissionRequest。
//
// 关键 ctx-bound 字段：
//   - runID:        agent.RunIDFromContext
//   - userID:       middleware.UserIDFromCtx
//   - agentDef:     agent.AgentDefAndParentFromCtx
//   - FullTool:     agent.FullToolFromCtx (runner.Run 已 stash)
func buildRequest(ctx context.Context, t einotool.BaseTool, input string) (PermissionRequest, error) {
	info, err := t.Info(ctx)
	if err != nil {
		return PermissionRequest{}, err
	}
	runID := agent.RunIDFromContext(ctx)
	userID, _ := middleware.UserIDFromCtx(ctx)
	agentDefID, parentUserID := agent.AgentDefAndParentFromCtx(ctx)
	fullTool := agent.FullToolFromCtx(ctx, info.Name)

	return PermissionRequest{
		AgentRunID:        runID,
		UserID:            userID,
		ParentUserID:      parentUserID,
		AgentDefinitionID: agentDefID,
		Tool:              fullTool,
		InputJSON:         input,
		SandboxID:         "",
	}, nil
}

func toolName(ctx context.Context, t einotool.BaseTool) string {
	if i, err := t.Info(ctx); err == nil {
		return i.Name
	}
	return "?"
}
