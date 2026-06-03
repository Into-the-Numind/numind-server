# B2B2C 子账户使用父账户配置的 Agent

## 来源
- 提出人：产品（已拍板 v1 必需）；技术根因来自 `docs/agent-mode/agent-mode-prod-readiness-test-plan.md` §0.1 附加红线候选 + §5.1 USR-CRIT
- 提出日期：2026-06-03

## 需求描述
莫小派是 B2B2C 平台：父账户（机构，`user.parent_user_id IS NULL`）在 `/config/agents` 配置 AI 助手（agent mode），其名下**子账户**（学员/员工，`user.parent_user_id = 父账户ID`，代码中称 "student"）在 `/agent/chat` 使用。

**产品要求 v1 子账户必须能使用父账户配置的 agent。**

## 业务目标
解除 B2B2C 定位与现状的直接冲突：当前只有父账户本人能跑 agent，子账户一跑就 `ErrSkillNotFound`，使"机构配置→学员使用"这一核心商业闭环无法成立。

## 现状 Bug（已确认）
agent **run 路径**有 3 个访问校验点，全部要求 `ad.ParentUserID == 调用者userID`，对子账户必然失败（子账户 id ≠ agent 拥有者=父账户 id）：

1. `resolveDefinition` — [student_run_lifecycle.go:683](../internal/numind/biz/agent/student_run_lifecycle.go) — estimate/create/stream 入口（调用点 119/228/435）
2. `agentRunner.Run` — [runner.go:470](../internal/numind/biz/agent/runner.go) — 非流式路径
3. `agentRunner.RunStream` — [runner_runstream.go:134](../internal/numind/biz/agent/runner_runstream.go) — **生产默认流式路径**

> 注：**list 路径已修复**。`AvailableForStudent`（[biz/skill/student_query.go:16](../internal/numind/biz/skill/student_query.go)）已按 learner 的 parent_user_id 列出父账户的 active agent，并有 parent/child/inactive 测试。所以子账户能"看到"但不能"跑"。

## 目标访问规则
把"调用者必须是 agent 的 ParentUserID 本人"改为"调用者属于该 agent 的租户"：

> 调用者能用某 agent ⟺ `ad.ParentUserID == caller.id`（父账户本人）**或** `ad.ParentUserID == caller.parent_user_id`（该父账户的子账户）。

**附加约束（R9）**：子账户**禁止**跑已下架（`is_active=false`）的 agent；父账户本人对自己的 agent 行为不变（仍可跑 inactive，保留试聊草稿能力）。

## 务必保持的隔离（不可放开）
- run/session/feedback 的归属校验（`verifyRunOwnership`、`GetSessionSnapshot`、`verifySessionOwnership`、`WriteFeedback`）保持**严格 per-userID**：子账户只看自己的运行记录，看不到父账户或其他子账户的。**只放开 agent 定义的访问，绝不放开别人的 run/session/feedback。**
- 不可跨租户：A 机构子账户不能用 B 机构 agent（规则要求 `caller.parent_user_id == ad.ParentUserID`）。
- 计费交汇（与并行任务 BLK-2）：run 仍以子账户 userID 发起，扣子账户自己的积分池——本任务不改发起方 userID，保持现状。

## 优先级
高（go-live blocker，B2B2C exit criteria 之一）

## Triage
- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：否
  2. 新增 API 端点：否
  3. 新外部服务集成：否
  4. 影响文件数：**>3**（3 个校验点 + biz.go 布线 + 共享 helper + 测试）
  5. 高风险业务逻辑（支付/**权限**）：**是**——这是访问控制改动
- 人类决定：**确认 Standard**（用户 2026-06-03 经 AskUserQuestion 确认）

## 备注
- 性能：resolveDefinition / RunStream 现需多读一次 user 拿 parent_user_id。设计上以 `ad.ParentUserID == caller.id` 作为快路径（父账户/试聊命中，0 额外查询），仅子账户才走 GetByID（主键查，亚毫秒级，非循环，无 N+1）。
- 共享 helper 候选：`biz/agent` 包级 `agentTenantAccess(ctx, userStore, callerID, ad) error`，三处复用，单一真相源。
- 测试矩阵（S5）：父跑自己 agent(允许)/ 子跑父 agent(允许)/ 子跑别租户 agent(拒 ErrSkillNotFound)/ 子跑已下架 agent(拒)/ 子看 run 列表(只看自己)。
