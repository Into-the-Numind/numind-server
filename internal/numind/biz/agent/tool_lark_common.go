package agent

// tool_lark_common.go holds the shared seam + helpers for the three 飞书 (Lark)
// agent tools (feishu-integration T10): lark_create_doc / lark_send_message /
// lark_read_bitable. Each tool:
//
//   - extracts the run initiator's userID from context (runner injects it),
//   - asks a feishu.LarkAPIProvider for a per-user feishu.LarkAPI
//     (token decrypt + expiry refresh happens inside the provider),
//   - calls one 飞书 API,
//   - on ANY failure returns a SOFT tool result (never a Go error) so the agent
//     run is not killed (design.md §8; CLAUDE.md "tool 硬错误杀 run" lesson),
//   - records a langfuse SPAN named lark.<tool> (NOT a generation — design.md §9),
//     with user_id / tool / lark_app_id metadata and NEVER any token/secret.
//
// The provider is injected by the factory (factory_platform.go), lazily built
// from f.ds.ThirdPartyAccounts(); a nil provider means 飞书 integration is off,
// in which case the tool soft-errors telling the user to enable/connect 飞书.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/middleware"
)

// larkSoftError marshals a uniform soft-error tool result. The "error" key
// mirrors the shape other soft-erroring tools use (web_search/image_gen) so the
// LLM sees a clear, actionable message and self-corrects on the next ReAct turn.
// It ALWAYS returns (result, nil) — a non-nil Go error would propagate to Eino
// as a NodeRunError and terminate the run.
func larkSoftError(format string, args ...any) (ToolResult, error) {
	msg := fmt.Sprintf(format, args...)
	out, _ := json.Marshal(map[string]string{"error": "ERROR: " + msg})
	return ToolResult(out), nil
}

// larkSoftErrorForAPIErr maps a feishu provider/API error to the right user-facing
// soft message:
//   - ErrLarkNotConnected  → prompt to connect 飞书 in settings
//   - ErrLarkReauthRequired → prompt to re-authorize (token expired)
//   - anything else        → generic "飞书 call failed" with the detail
//
// All three return a SOFT result (nil Go error). The detailLabel (e.g. "创建文档")
// localises which operation failed.
func larkSoftErrorForAPIErr(detailLabel string, err error) (ToolResult, error) {
	switch {
	case errors.Is(err, errno.ErrLarkNotConnected):
		return larkSoftError("尚未连接飞书，无法%s。请先在设置中连接飞书账号后重试。", detailLabel)
	case errors.Is(err, errno.ErrLarkReauthRequired):
		return larkSoftError("飞书授权已过期，无法%s。请在设置中重新授权飞书后重试。", detailLabel)
	default:
		return larkSoftError("%s失败：%s", detailLabel, err.Error())
	}
}

// larkAPIFor resolves the per-user feishu.LarkAPI from a provider, returning a
// SOFT error result when 飞书 is off (nil provider), the user is unauthenticated
// (userID 0), or the provider cannot build an API (not connected / reauth). The
// bool reports whether the caller should proceed (true) or return softResult
// (false). When proceeding, api is non-nil.
func larkAPIFor(ctx context.Context, provider feishu.LarkAPIProvider, detailLabel string) (api feishu.LarkAPI, softResult ToolResult, proceed bool) {
	if provider == nil {
		res, _ := larkSoftError("飞书集成未启用，无法%s。", detailLabel)
		return nil, res, false
	}
	userID, _ := middleware.UserIDFromCtx(ctx)
	if userID == 0 {
		res, _ := larkSoftError("无法识别当前用户身份，无法%s。", detailLabel)
		return nil, res, false
	}
	a, err := provider.APIFor(ctx, userID)
	if err != nil {
		res, _ := larkSoftErrorForAPIErr(detailLabel, err)
		return nil, res, false
	}
	return a, nil, true
}

// larkStartSpan opens a langfuse span named lark.<toolShort> on the run's
// existing trace (design.md §9: span, not generation). It returns an endSpan
// closure that records output/error and closes the span. When there is no trace
// in context (tests / Langfuse disabled) both are no-ops. spanInput is the tool's
// raw input (safe — tool inputs carry no token/secret). appID is recorded as
// metadata once known (call endSpan with it via the fields map).
func larkStartSpan(ctx context.Context, toolShort string, userID uint, spanInput any) (endSpan func(fields map[string]any, errMsg string)) {
	tc := langfuse.FromContext(ctx)
	if tc == nil {
		return func(map[string]any, string) {}
	}
	spanID := langfuse.SpanID()
	langfuse.CreateSpan(tc.TraceID, spanID, "lark."+toolShort,
		langfuse.WithSpanParent(tc.ParentObservationID),
		langfuse.WithSpanInput(spanInput),
		langfuse.WithSpanMetadata(map[string]string{
			"user_id": fmt.Sprintf("%d", userID),
			"tool":    "lark_" + toolShort,
		}),
	)
	return func(fields map[string]any, errMsg string) {
		t := langfuse.FromContext(ctx)
		if t == nil {
			return
		}
		if errMsg != "" {
			langfuse.EndSpan(t.TraceID, spanID, langfuse.WithSpanOutput(fields), langfuse.WithSpanError(errMsg))
			return
		}
		langfuse.EndSpan(t.TraceID, spanID, langfuse.WithSpanOutput(fields))
	}
}
