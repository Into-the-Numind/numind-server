# S3 Gate Review: `child-run-permission`

- **Reviewer**: Claude Sonnet (独立 subagent，未参与 S0–S2)
- **Review Date**: 2026-04-20
- **Artifact Reviewed**: `numind-server/docs/superpowers/plans/2026-04-20-child-run-permission-plan.md`
- **Supporting Artifacts**: spec, proposal, requirement card, existing code

## Verdict

**PASS_WITH_CONCERNS** — 一个 P0 + 两个 P1 问题需要修复后才能进 S4。

## Concerns 原文与处理状态

### P0 — Backfill SQL `NOT EXISTS` 范围过宽（软删除漏洞）
- 问题：`user_template_permission` 嵌入 `gorm.Model` 带 `DeletedAt`，曾被父账号撤权（软删）的子账号虽然"活跃记录 = 0"，但 hard rows 存在 → `NOT EXISTS` 返回 false → 不被 backfill → 翻转后 deny-all → 误屏蔽
- **处理**：已确认属实。核实代码 `model/user_template_permission.go:9` 确有 `gorm.Model`。**并额外发现**：该 struct 还有 `ParentUserID` NOT NULL 字段，spec §3.2 backfill SQL 漏写。双重修复：
  - `NOT EXISTS` 子查询加 `AND p.deleted_at IS NULL`
  - backfill INSERT 列补 `parent_user_id`（从 `user.parent_user_id` 取）
  - 新增测试用例 `TestBackfill_SoftDeletedSubUser`

### P1-A — Task 2 翻转代码与 Task 7 backfill 的部署顺序冲突
- 问题：plan §4 说所有 task 在一个 feature branch 内 commit，S6 统一 merge。spec §6 要求"先 migration 再 deploy code"。这两个矛盾在 dev 环境会导致 deploy 触发到 backfill 完成间存在 deny-all 窗口
- **处理**：Task 7 步骤改为"Step 0: SSH dev 手动 apply backfill → Step 1: merge feature → CI 触发 deploy"，plan §4 Git 流程明确这个前置
- 注：Dev 窗口实际影响有限（dev 环境存量子账号测试数据），但 prod 部署步骤必须严格同序

### P1-B — `Chat` 应为 `ChatStream` + 撤销即时生效的守卫缺失
- 问题：接口里是 `ChatStream`（`stream.go:31`），不是 `Chat`。更关键：PRD AS-5 要求"撤销即时生效"，但 `ChatStream` 当前没有 `HasChatbotPermission` 守卫 → 父账号撤权后子账号仍能继续 chat 已有 session
- **处理**：spec §3.5 和 plan Task 4 全部修正为 `ChatStream`；在 `stream.go:40` session 所有权校验后增加 chatbot 权限检查；补 `TestChatStream_AfterRevoke_Denied`

### P2-A — `/v1/chatbot/list` 只返 published 与 D5 列 draft 决策冲突
- 问题：spec §3.7 和 §8 D5 互相矛盾
- **处理**：翻 D5。决策改为"只列 published chatbot 授权"。理由：用户工作流是"创建 → 设权限 → 发布"或"创建 → 发布 → 设权限"，default-deny 下发布+0 权限 = 零泄露，发布后再设权限不会造成可见性问题。删除 D5 复杂度，保持接口语义一致

### P2-B — Backfill rollback 时间窗口过度删除风险
- 问题：用 `created_at BETWEEN start AND end` 回滚可能误删人工授权
- **处理**：接受风险。缓解：(1) backfill 执行窗口锁在 <2 分钟 (2) 仅在维护窗口执行，期间不允许父账号做 grant 操作 (3) rollback 脚本在 DELETE 前做 dry-run `SELECT COUNT(*)` 打印 + 人工确认。不加 `source` 字段避免 schema 膨胀

## Plan 原子性结论

7 个 task 原子性全部合格。Task 3 和 Task 4 并行 dispatch 安全（文件不重叠）。唯一需要 cross-task 约束的是 P1-A 的部署顺序。

## S5 验证策略结论

**不完全同意当前 gstack /qa + SQL 验证**。部分升级：
- 保留 gstack /qa 做 UI 弹窗 + 列表可见性验证
- 保留 SQL 级 backfill pre/post 对比做存量保护验证
- **新增 minimal Playwright request-level E2E** 覆盖两条高风险路径：
  - P0.5 子账号直连 API 传未授权 chatbot_id → 403（纯 HTTP，不走 UI）
  - `TestChatStream_AfterRevoke_Denied` 对应的端到端路径（有 session → 撤权 → 再 chat → 403）
- 单测新增 3 个缺口用例：`TestHasTemplatePermission_WhitelistMissAfterSoftDelete`, `TestChatStream_AfterRevoke_Denied`, `TestGrantChatbots_SelfParentBypassed`

## 修复后状态

所有 P0/P1 在本轮 review 响应中直接修复，不需要二次 review（修改集中在 spec + plan 文档，代码未动）。进入 S4 前 manifest stage 可置为 `S3-done`。
