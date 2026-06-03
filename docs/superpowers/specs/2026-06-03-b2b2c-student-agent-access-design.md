# Spec: B2B2C 子账户使用父账户配置的 Agent

> 精简 Standard（S1+S2 合并）。Feature id: `b2b2c-student-agent-access`。
> 需求卡：`requirements/b2b2c-student-agent-access.md`。
> 基线：develop HEAD（feature worktree `feature/b2b2c-student-agent-access`）。

---

## 1. 问题与目标（S1 提案精简）

B2B2C：父账户（`user.parent_user_id IS NULL`）配置 agent，名下子账户（`user.parent_user_id = 父ID`，代码称 "student"）使用。**产品 v1 必需：子账户能跑父账户配置的 agent。**

现状只有父账户本人能跑，子账户 → `ErrSkillNotFound`（go-live blocker）。

**复用判断**：list 路径 `AvailableForStudent`（[biz/skill/student_query.go:16](../../../internal/numind/biz/skill/student_query.go)）的 parent/child 解析逻辑已存在且有测试 → 运行路径复用同一租户语义即可，无需新表/新端点/新服务。**工作量**：S 级（3 校验点统一 + 布线 + helper + 测试）。

---

## 2. 访问规则（权威定义）

> 调用者 `caller` 能使用 agent 定义 `ad` ⟺
> **(P)** `ad.ParentUserID == caller.id`（caller 是该 agent 的拥有者父账户），**或**
> **(C)** `ad.ParentUserID == caller.parent_user_id`（caller 是该父账户的子账户）。

附加约束：
- **R9（子账户禁跑下架）**：分支 (C) 命中时，若 `ad.IsActive == false` → 拒（`ErrSkillNotFound`）。分支 (P) 不受 `is_active` 限制（父账户保留对自己 inactive 草稿的试聊能力 — 与现状一致，现状 run 路径用 `GetByIDIncludeInactive`）。
- **跨租户**：(C) 严格要求 `caller.parent_user_id == ad.ParentUserID`，A 机构子账户不能用 B 机构 agent。
- **不暴露存在性**：所有拒绝一律返回 `ErrSkillNotFound`（404），不区分"不存在"与"无权限"。

### 真值表

| caller | ad.ParentUserID | ad.IsActive | 结果 |
|---|---|---|---|
| 父账户 id=P | P | true | ✅ 允许 (P) |
| 父账户 id=P | P | false | ✅ 允许 (P，试聊草稿) |
| 子账户(parent=P) | P | true | ✅ 允许 (C) |
| 子账户(parent=P) | P | false | ❌ 拒 (R9) |
| 子账户(parent=P) | Q≠P | true | ❌ 拒（跨租户） |
| 无父账户的独立用户 id=U | Q≠U | * | ❌ 拒 |

---

## 3. 设计

### 3.1 共享 helper（单一真相源）

新文件 `internal/numind/biz/agent/tenant_access.go`，包级函数：

```go
func agentTenantAccess(ctx context.Context, userStore store.UserStore, callerID uint, ad *model.AgentDefinition) error
```

语义实现规则 §2。要点：
- **快路径**：`ad.ParentUserID == callerID` → 立即 `nil`，**不读 user**（父账户/试聊命中，0 额外查询）。
- 仅分支 (C) 才 `userStore.GetByID(ctx, callerID)`（主键 get，亚毫秒，非循环，无 N+1）。
- **⚠️ P1 nil 守卫（强制）**：`caller.ParentUserID` 是 `*uint`，父账户为 `nil`。分支 (C) **必须**先判 nil 再解引用，否则一个"跑别的父账户 agent"的父账户调用者会走到慢路径、解引用 nil → **panic（且 run goroutine 用 context.Background()，崩在后台）**。正确写法：
  ```go
  if caller.ParentUserID == nil || *caller.ParentUserID != ad.ParentUserID {
      return errno.ErrSkillNotFound
  }
  // 到这里：caller 是 ad 拥有者的子账户
  if !ad.IsActive {
      return errno.ErrSkillNotFound // R9：子账户禁跑下架
  }
  return nil
  ```
- `gorm.ErrRecordNotFound` → `ErrSkillNotFound`；其他 err → `fmt.Errorf("agentTenantAccess: ...: %w", err)` 包装上抛。
- **nil userStore 降级**：仅快路径可用，其余一律 `ErrSkillNotFound`（保证未接线 userStore 的单测行为 = 旧行为，全绿）。
- 返回 `nil`=允许，否则 `errno.ErrSkillNotFound`（或包装的内部 err）。

### 3.2 三个校验点改造

| # | 位置 | 现状 | 改为 |
|---|---|---|---|
| 1 | `resolveDefinition` [student_run_lifecycle.go:683](../../../internal/numind/biz/agent/student_run_lifecycle.go) | `if ad.ParentUserID != userID { return nil, ErrSkillNotFound }` | `if err := agentTenantAccess(ctx, s.userStore, userID, ad); err != nil { return nil, err }` |
| 2 | `agentRunner.Run` [runner.go:470](../../../internal/numind/biz/agent/runner.go) | `if ad.ParentUserID != req.UserID { return nil, ErrSkillNotFound }` | `if err := agentTenantAccess(ctx, r.userStore, req.UserID, ad); err != nil { return nil, err }` |
| 3 | `agentRunner.RunStream` [runner_runstream.go:134](../../../internal/numind/biz/agent/runner_runstream.go) | `if ad.ParentUserID != req.UserID { return nil, ErrSkillNotFound }` | `if err := agentTenantAccess(ctx, r.userStore, req.UserID, ad); err != nil { return nil, err }` |

> 三点全改的必要性：lifecycle.resolveDefinition 与 runner 是**独立**的二次校验（estimate/create 经 resolveDefinition；实际跑经 runner）。只改一处，子账户仍会在另一处 `ErrSkillNotFound`。

**亦被 gate #2 覆盖的路径（设计审查补记）**：`ask_user_question` 的 **Answer/resume** 路径 [answer.go:95](../../../internal/numind/biz/agent/answer.go) → `runner.Run`（即 gate #2）。无需单独改动，但 S5 测试矩阵须含"子账户 Answer 续跑"用例。

**其余确认为无关的 ownership 校验（不在本 feature 范围、勿动）**：`skill/service.go ownsAgent`（agent CRUD 配置）与 `skill/artifact/binding.go validateAgentOwnership`（skill binding CRUD）——这两条由 controller 层 `requireParentAccount`/`resolveParentUserID` 在更上游就把子账户挡成 403，是**配置端**（仅父账户）逻辑，与"使用端"访问无关，保持不变。

改造后 `ad.ParentUserID` 在下游的用途（`skillBindingService.ListByAgent(ctx, ad.ParentUserID, ...)`、`WithAgentDefCtx(ctx, defID, ad.ParentUserID)`）**保持不变** — 这些按 agent 拥有者（父账户）加载技能/工具配置，正确。

### 3.3 依赖注入（userStore）

两个结构体需新增 `userStore store.UserStore`，**非破坏性**接线：

- `agentRunner`：新增字段 + `WithUserStore(store.UserStore) RunnerOption`。biz.go `NewAgentRunner(...)` 追加 `agent.WithUserStore(ds.Users())`。
- `StudentRunService`：新增字段 + 链式方法 `WithUserStore(store.UserStore) *StudentRunService`（仿现有 `WithAttachmentStore` 模式，避免改 6 参数位置构造函数 + ~10 处 nil 调用点）。biz.go 在 `NewStudentRunService(...)` 后链 `.WithUserStore(ds.Users())`。
- `ds.Users()` 返回 `store.UserStore`，已在 `NewStudentQueryService` 用过。

未接线时（旧单测）userStore=nil → 快路径生效、其余拒 → 旧测试断言不变。

---

## 4. 严格保持的隔离（不可放开 — reviewer 红线）

**仅放开 agent 定义的访问**。以下 **per-userID** 校验**绝不修改**：
- `verifyRunOwnership`（runID 必须属 userID）
- `verifySessionOwnership` / `GetSessionSnapshot` / `PinSession` / `RenameSession` / `DeleteSession`
- `GetRun` / `WriteFeedback`（run 必须属 userID）

子账户只看/操作自己的 run/session/feedback，看不到父账户或其他子账户的。

**Out of scope**（本 feature 不动）：
- 计费/扣减对象（BLK-2 并行任务）：run 仍以子账户 `userID` 发起，扣子账户积分池 — 本 feature 不改发起方 userID。
- L2 memory keying、narration、compliance 等运行时子系统。

---

## 5. 验收标准（驱动 S5 验证矩阵）

Go 单测（biz/agent，mock store）：
1. 父账户跑自己 agent（active）→ 允许
2. 父账户跑自己 agent（inactive）→ 允许（试聊草稿）
3. 子账户跑父账户 agent（active）→ 允许 **（核心修复）**
4. 子账户跑父账户 agent（inactive）→ 拒 `ErrSkillNotFound`（R9）
5. 子账户跑别租户 agent → 拒 `ErrSkillNotFound`（跨租户）
6. 无父账户独立用户跑别人 agent → 拒
7. nil userStore + 父账户 → 允许（降级回归，保旧测试）
7b. nil userStore + 子账户 + active agent → 拒 `ErrSkillNotFound`（证明降级不会误放子账户）
8. run 列表隔离：子账户 `ListByUser`/`GetRun`/session 仍只见自己的（断言 verifyRunOwnership 未被放开）
9. **P1 回归**：父账户 A 跑父账户 B（≠A）的 agent，且 A 的 `ParentUserID==nil` → 走慢路径 → 返回 `ErrSkillNotFound` **不 panic**（nil 守卫）
10. **Answer 续跑**：子账户对父账户 active agent 的 `ask_user_question` Answer → 经 gate #2 放行；对 inactive agent → R9 拒

覆盖三个校验点：helper 单测（直接测 `agentTenantAccess`，含用例 1-7b+9）+ resolveDefinition 经 Estimate/Create + runner 经 RunStream/Run（用 fake userStore + skillStore，含用例 10）。

### 已知边界行为（设计审查记录，v1 接受）
**中途下架陷阱**：父账户在子账户某个 run 处于 `waiting_for_user_choice` 时把 agent 下架（is_active=false）→ 子账户 Answer 续跑被 R9 拒 → 该 run 卡住无法续。**逃生**：Cancel 仍可用（只查 `verifyRunOwnership`，不经 agent 访问 gate）。v1 接受此行为（下架=停止一切使用语义自洽；触发条件极端罕见）；不为 Answer-resume 豁免 R9（避免给 gate #2 引入分叉状态）。

E2E（S5，dev）：真实子账户登录 → 首页见父账户 agent → 跑通一次对话（确认不再 `ErrSkillNotFound`）。

---

## 6. S5 验证策略（NDF Rule 10 — 权限改动强验证）

- **方式**：Go 单测（上 §5 矩阵 1-8，永久回归）为主 + dev 子账户 e2e 冒烟（`E2E_CHILD_USERNAME/PASSWORD`）为辅。
- **理由**：权限/访问控制必须有持久回归保护（Rule 10：高风险逻辑要 Playwright/Go test，不能只 gstack 一次性）。Go 单测覆盖允许/拒绝全矩阵成本低、确定性高；dev e2e 验真实链路（含前端首页数据源 + 流式跑通）。
- **关键路径**：① 子账户登录 → ② 首页"AI 智能体"分区能看到父账户 agent → ③ 点击进入 `/agent/chat` 发消息 → ④ 流式回答正常（无 ErrSkillNotFound）。
- **数据夹具坑**（需先解决）：父账户 admin(id=30) 当前名下无 agent，且"从零创建"有 422 bug。S5 前需 seed 或 API 造一个"父账户拥有的 active agent" + 一个挂该父账户的子账户。

---

## 7. 文件清单

| 文件 | 改动 |
|---|---|
| `internal/numind/biz/agent/tenant_access.go` | **新增** helper + 单测同包 |
| `internal/numind/biz/agent/student_run_lifecycle.go` | struct 加 userStore 字段 + WithUserStore 链式方法 + resolveDefinition 改用 helper |
| `internal/numind/biz/agent/runner.go` | agentRunner 加 userStore 字段 + WithUserStore option + line 470 改用 helper |
| `internal/numind/biz/agent/runner_runstream.go` | line 134 改用 helper |
| `internal/numind/biz/biz.go` | NewAgentRunner 加 WithUserStore；NewStudentRunService 链 .WithUserStore |
| `internal/numind/biz/agent/tenant_access_test.go` | **新增** helper 矩阵单测 |
| `internal/numind/biz/agent/student_run_lifecycle_test.go` | 加子账户 access 测试（fake userStore） |
