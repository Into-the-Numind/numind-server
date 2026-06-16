package meeting

import (
	"context"

	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/billing"
)

// internalCallCtx 构造「内部试用、仅记录用量、不扣费、不拦截」的 AI 调用上下文（SPEC §1 计费纪律）。
//
// 会议副驾是内部试用功能：LLM/ASR 调用必须走 aiservice 统一入口（保证 Langfuse +
// UsageRecord 自动记录），但**绝不**触发 credits 三池扣减或会员门槛拦截。本函数复刻
// biz/sessiontitle.Generate 已验证的内部调用配方（adaptive-session-titles S2 review P0）：
//
//  1. billing.WithBilling(ctx, 0, op)  — userID 置 0：网关 ContextBudgetCredits 的
//     EnforceModelMembership 门以 billing userID（其次 ctxKeyUserID）判断；置 0 后
//     gateUserID==0，会员门**整段跳过**（context_budget.go 中 `if gateUserID != 0` 守卫），
//     保证非会员也能用、不被 0 价会员专属模型拦截。
//  2. aismw.WithUserID(ctx, 0) — UsageRecord 归属字段亦取 0；与现有内部后台任务
//     （memory 抽取、session.title）一致：内部非计费调用的用量记到 user_id=0。
//  3. WithoutGatewayBillingOnly + WithSkipLegacyBilling — 清除 bill-only / legacy 扣费旁路。
//  4. 调用方**不**设置 ChatRequest.ContextFragments —— 网关因此走「无 fragment 直通」分支
//     （context_budget.go step 1），既不 Reserve 也不 Reconcile，credits 三池零变动。
//
// 取舍说明（偏离 SPEC §4「传真实 userID」的理由）：网关的会员门 fallback 到 ctxKeyUserID，
// 若传真实 userID 且路由解析到 0 价会员专属模型（dev 默认模型可能如此），非会员用户会被
// ErrModelMembershipOnly 拦截——这违反 SPEC §1/§8「内部试用不做会员门槛」的硬规则。无法
// 同时满足「真实归属」与「绝不拦截」，故按硬规则优先取 userID=0（与 sessiontitle / memory
// 后台任务一致）。Langfuse trace 仍可用 langfuse.WithUserID 标注真实用户做可观测归属。
func internalCallCtx(ctx context.Context, operation string) context.Context {
	ctx = billing.WithBilling(ctx, 0, operation)
	ctx = aismw.WithUserID(ctx, 0)
	ctx = aismw.WithoutGatewayBillingOnly(ctx)
	ctx = aiservice.WithSkipLegacyBilling(ctx)
	return ctx
}
