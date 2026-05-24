# ADR 0001 — Sanitize LLM 由 qwen-turbo 切换到 deepseek-v4-pro (DMXAPI)

- **Feature**: agent-mode-v2-skill-marketplace
- **Stage**: S5（验收期间发现）
- **Date**: 2026-05-24
- **Status**: Accepted
- **Supersedes spec §3.2 / §9 / §12 中关于 sanitize 模型选择的所有 qwen-turbo 表述**

## 背景

Spec §3.2 原方案：sanitize Stage 2 LLM 调用走 `qwen-turbo`（DashScope，阿里云）。理由：
- body 通常 <5KB, latency budget <3s
- 输出确定性高（脱敏一致性优先）
- 成本最低（~0.01 元 / 单次）

S5 dev 验收时实测：DashScope 当前账户处于 **"use free tier only"** 模式，免费配额已耗尽。

```
HTTP 403 AllocationQuota.FreeTierOnly
"The free tier of the model has been exhausted. If you wish to continue access
 the model on a paid basis, please disable the 'use free tier only' mode in
 the management console."
```

验证 `qwen-turbo` / `qwen-plus` / `qwen-turbo-latest` 全部返回相同 403。

## 决策

Sanitize Stage 2 LLM 调用切换到 **`deepseek-v4-pro` (DMXAPI 聚合平台路由)**，对应 dev DB `ai_service.id=24`，`ai_service_route.provider_id=1 (dmxapi)`。

T7 migration `20260524_130000_register_skill_marketplace_sanitize_profile.sql` 已同步从 `WHERE model_key='qwen-turbo'` 改为 `WHERE model_key='deepseek-v4-pro'`，prod 首次部署即可拿到正确路由。

## 理由

1. **DMXAPI 不受 DashScope free-tier 限制**：dev 实测 sanitize-preview HTTP 200 通过；
2. **deepseek-v4-pro 满足 §3.2 三项要求**：非 thinking 模型（确定性高）、latency 在同量级、聚合平台单价可控；
3. **业务层不需要改动**：通过 DB Registry / task_profile 路由切换，`aiservice.Chat(ctx, profile.SkillMarketplaceSanitize, …)` 入参不变；
4. **运营可后切回 qwen-turbo**：阿里云账户解禁付费后，管理端改 task_profile.default_service_id 即可，无需代码变更。

## 影响

| 文件 | 状态 | 说明 |
|------|------|------|
| `migrations/20260524_130000_register_skill_marketplace_sanitize_profile.sql` | ✅ 已改 | model_key 改为 deepseek-v4-pro |
| `internal/numind/biz/marketplace/sanitize.go` | ✅ 已改 | Langfuse `WithGenModel` open hint 同步 |
| `docs/superpowers/specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md` §3.2 / §9 / §12 | ⚠️ inline 注脚指向本 ADR | spec 历史文本保留为档案，请以本 ADR 为准 |
| dev DB | ✅ 已通过 UPDATE 切换 | hot-fix 已生效，validation 6/6 通过 |
| prod DB | ⏳ 待 S6 部署时按 migration 跑 | verify: `SELECT id FROM ai_service WHERE model_key='deepseek-v4-pro' AND is_active=1 AND deprecated_at IS NULL` 应有结果 |

## 替代方案及其拒绝理由

- **方案 A**：让用户先去阿里云控制台关掉 "free tier only" 再切回 qwen-turbo。拒绝：账户层配置易再次回退，影响 spec 落地节奏。
- **方案 B**：给 qwen-turbo 加 DMXAPI fallback 路由。拒绝：DMXAPI 不一定接受 `qwen-turbo` 模型 ID（dev DB 当前未建该路由），需要试错；且 fallback middleware 行为还要复杂化。

## 回滚

如需切回 qwen-turbo：
1. 阿里云 DashScope 控制台关掉 "use free tier only"；
2. dev/prod 跑 `UPDATE task_profile SET default_service_id=(SELECT id FROM ai_service WHERE model_key='qwen-turbo' AND is_active=1 AND deprecated_at IS NULL LIMIT 1) WHERE task_id='skill.marketplace.sanitize';`；
3. 把本 migration model_key 改回 'qwen-turbo'，新增一个 forward migration 完成 prod 切换。

不需要 ADR-0002，因为切换是数据层操作。
