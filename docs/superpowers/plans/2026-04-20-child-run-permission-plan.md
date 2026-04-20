# 子账号运行权限管理 — 实施 Plan (S3)

- **Feature ID**: child-run-permission
- **NDF Stage**: S3 (Implementation Plan)
- **Created**: 2026-04-20
- **Supersedes Spec**: `numind-server/docs/superpowers/specs/2026-04-20-child-run-permission-design.md`

## §0 前置同步

Feature A (`sop-perm-dialog-show-all`) 已合入 develop（8236ebd / 1caf857）。本 feature 两个 feature 分支从 A merge 后创建：

```bash
cd numind-server && git checkout develop && git pull
git checkout -b feature/child-run-permission
cd ../numind-web-v3 && git checkout develop && git pull
git checkout -b feature/child-run-permission
```

## §1 Task 分解（共 7 个 task + 1 个 S5 策略 task）

每个 task 独立可构建、可测试、可 commit；两阶段 review（spec compliance + code quality）后才能启动下一个。

### Task 1 — DB migration + Go model （numind-server）

**目标**：新增 `user_chatbot_permission` 表 + backfill migration + Go model。

**文件改动**：
- `numind-server/migrations/20260420_230000_create_user_chatbot_permission.sql`（新建）
- `numind-server/migrations/20260420_230000_create_user_chatbot_permission_rollback.sql`（新建）
- `numind-server/migrations/20260420_230001_backfill_default_template_permissions.sql`（新建）
- `numind-server/migrations/20260420_230001_backfill_default_template_permissions_rollback.sql`（新建）
- `numind-server/internal/pkg/model/user_chatbot_permission.go`（新建）

**【P0 review 修正点】**：
- Backfill SQL 的 `NOT EXISTS` 子查询**必须**加 `AND p.deleted_at IS NULL` —— `user_template_permission` 嵌入 `gorm.Model` 有软删除，曾撤权的子账号活跃记录 = 0 但 hard rows 存在，若漏过滤会误判跳过
- Backfill INSERT 列**必须**包含 `parent_user_id`（`NOT NULL` 列，从 `user.parent_user_id` JOIN 取值） + `updated_at` —— 初版 spec 漏写
- 参考 spec §3.2 最新版 SQL 完整写法

**验收**：
- SQL 在 dev DB 运行成功（create + 二次 apply 验证幂等）
- Go `go build ./...` pass
- Migration 文件头部注释说明执行顺序（create 先、backfill 后）
- 在 dev 上手动制造一个"所有权限记录都软删除的子账号"（`UPDATE user_template_permission SET deleted_at=NOW() WHERE sub_user_id=X`），再跑 backfill → 该子账号应被写入新的权限行

**独立性**：纯 DDL/model，不依赖其他 task。

---

### Task 2 — Store 层 chatbot 权限 + SOP 语义翻转 （numind-server）

**目标**：扩展 `customerStore` 加入 4 个 chatbot 权限方法；翻转 `HasTemplatePermission` 的 0 记录分支为 deny。

**文件改动**：
- `numind-server/internal/numind/store/customer.go`（修改既有 + 新增 4 方法）
- `numind-server/internal/numind/store/customer_test.go`（扩展既有 + 新 chatbot 测试）

**新增方法**：
- `HasChatbotPermission(ctx, userID, chatbotID) (bool, error)`
- `ListSubUserChatbotIDs(ctx, subUserID) ([]uint, error)`
- `GrantChatbotPermissions(ctx, subUserID, []chatbotIDs) error`
- `RevokeChatbotPermissions(ctx, subUserID, []chatbotIDs) error`

**翻转点**：`HasTemplatePermission` 中 `totalPermissions == 0 → return true` 改为 `return false`。

**测试用例（必须覆盖）**：
- `TestHasTemplatePermission_DefaultDeny`（新翻转的分支）
- `TestHasTemplatePermission_WhitelistHit`（既有扩展）
- `TestHasTemplatePermission_WhitelistMiss`（既有扩展）
- `TestHasChatbotPermission_ParentBypass`
- `TestHasChatbotPermission_DefaultDeny`
- `TestHasChatbotPermission_WhitelistHit`
- `TestGrantChatbotPermissions_Idempotent`（二次 grant 不报错，行数不变）
- `TestRevokeChatbotPermissions_MissingNoError`

**验收**：`go test ./internal/numind/store/...` 全 PASS。

**独立性**：依赖 Task 1 的 model。

---

### Task 3 — Biz 层 customer chatbot 方法 （numind-server）

**目标**：`customerBiz` 增加 6 个 chatbot 方法 + 父子关系校验。

**文件改动**：
- `numind-server/internal/numind/biz/customer/customer.go`（扩展）
- `numind-server/internal/numind/biz/customer/customer_test.go`（新增 ~6 测试）

**新增方法**：
- `CheckChatbotPermission(ctx, userID, chatbotID) (bool, error)` — 直接委托给 store
- `ListSubUserChatbots(ctx, parentUserID, subUserID) ([]model.ChatbotConfig, error)` — 返回子账号已授权的 chatbot 详情（JOIN 查 chatbot_config）
- `GrantChatbots(ctx, parentUserID, subUserID, chatbotIDs) error` — 含父子校验 + chatbot 归属校验
- `RevokeChatbots(ctx, parentUserID, subUserID, chatbotIDs) error`
- `BatchGrantChatbots(ctx, parentUserID, []subUserIDs, []chatbotIDs) error`
- `BatchRevokeChatbots(ctx, parentUserID, []subUserIDs, []chatbotIDs) error`

**父子校验**（每个方法 entry 都跑一次）：
1. 所有 subUserIDs 的 `ParentUserID == parentUserID`（否则返回 ErrForbidden）
2. 所有 chatbotIDs 的 `UserID == parentUserID`（否则返回 ErrNotFound）

**测试重点**：越权拒绝（父 A 对父 B 的子账号授权 → 403；给非自己的 chatbot → 404）。

**独立性**：依赖 Task 2 的 store 接口。

---

### Task 4 — Biz 层 chatbot 运行时守卫 （numind-server）

**目标**：`chatbotBiz` 的 `ListVisibleChatbots` 加白名单过滤；`CreateSession` / **`ChatStream`** 加权限守卫。

**文件改动**：
- `numind-server/internal/numind/biz/chatbot/chatbot.go`（修改 `ListVisibleChatbots` + `CreateSession`）
- `numind-server/internal/numind/biz/chatbot/stream.go`（修改 `ChatStream`）
- `numind-server/internal/numind/biz/chatbot/chatbot_test.go`（新增测试）
- `numind-server/internal/numind/biz/chatbot/stream_test.go`（新增 ChatStream 测试，若不存在则新建）
- `numind-server/internal/pkg/errno/code.go` 或对应错误码文件（新增 `ErrChatbotRunDenied`）

**【P1-B review 修正点】**：
- 接口实际是 `ChatStream`（`stream.go:31`），不是 `Chat`，plan 初版引用错误
- `ChatStream` 必须在 session 所有权校验之后、LLM 调用之前**显式调用** `ds.Customers().HasChatbotPermission(ctx, userID, session.ChatbotID)`。PRD AS-5 要求"撤销即时生效" —— 父账号撤权后子账号对已有 session 再发消息要被 403 拒绝

**改造点**：
1. `ListVisibleChatbots`: 子账号时用 `ds.Customers().ListSubUserChatbotIDs` 拉白名单，set 交集过滤
2. `CreateSession(ctx, userID, chatbotID)`: entry 先调 `HasChatbotPermission`
3. **`ChatStream(ctx, userID, sessionID, ...)`**: session 所有权校验后增加 `HasChatbotPermission(userID, session.ChatbotID)` 检查
4. `ListSessions` / `ListMessages`: 不加额外守卫（session 已绑 user_id，历史 session/messages 可读不等于能继续 chat —— 读权限 vs 运行权限解耦）

**测试**（必须全部覆盖）：
- `TestListVisibleChatbots_ChildFiltered`（子账号只看到白名单内）
- `TestListVisibleChatbots_ParentAll`（父账号不受限）
- `TestCreateSession_ChatbotRunDenied`（未授权 → 403）
- `TestCreateSession_ChatbotAllowed`（授权 → 成功）
- **`TestChatStream_AfterRevoke_Denied`**（新增 P1-B）—— 创建 session，父账号撤权，再调 ChatStream → 返回 ErrChatbotRunDenied

**独立性**：依赖 Task 2 store + Task 3 biz 接口。

---

### Task 5 — Controller + Router （numind-server）

**目标**：5 个新 endpoint 对接 biz 层 + 路由注册。

**文件改动**：
- `numind-server/internal/numind/controller/v1/customer/customer.go`（扩展 5 handler）
- `numind-server/internal/numind/router.go`（注册 5 路由）

**Controller 逻辑**：参考既有 Template handler（ListSubUserTemplates / GrantTemplates / RevokeTemplates / BatchGrantTemplates / BatchRevokeTemplates）1:1 对称实现。

**Router 注册位置**：紧邻既有 template 路由（router.go:234-239）。

**验收**：
- `go build ./...` pass
- Router 启动无路径冲突
- 手动 curl 测一个 happy path（可用 Task 6 结束后联调）

**独立性**：依赖 Task 3 biz 接口。

---

### Task 6 — 前端 API + CustomersView chatbot 区块 （numind-web-v3）

**目标**：API 层 6 个新函数 + CustomersView 弹窗新增 chatbot 区块。

**文件改动**：
- `numind-web-v3/src/api/customers.ts`（追加 6 函数 + TypeScript 类型）
- `numind-web-v3/src/views/CustomersView.vue`（加 state / method / template block）

**UI 复制模板**：chatbot 区块完整复用 SOP 区块结构（perm-group-title + badge + perm-toggle-all 按钮 + perm-list + perm-item + checkbox-mark），只替换数据源。

**保存行为**：
- 打开弹窗时 parallel 拉 `fetchAllTemplates + fetchUserTemplates + fetchAllChatbots + fetchUserChatbots`
- 保存按钮计算 SOP 和 chatbot 两个 diff（`toGrant`/`toRevoke`）各自调用对应 API
- 成功后合并 toast "权限已更新"
- 部分失败时分别 toast 告知哪一半失败

**验收**：
- `npm run lint && npm run type-check` pass
- 手动在 dev 环境打开弹窗，能看到 chatbot 列表和勾选状态
- 能保存成功

**独立性**：依赖 Task 5 后端 API。

---

### Task 7 — Dev migration apply + 联调验证 （运维）

**目标**：在 dev 环境**先**运行 migration **再**触发 code deploy，执行端到端联调。

**【P1-A review 修正的执行顺序】**：原版把 SSH apply 和 merge 的顺序搞错 —— merge 触发 CI deploy 必须发生在 backfill 完成后，否则翻转代码会先于存量保护上线，造成 0 记录子账号临时 deny-all。

**严格步骤顺序**：
1. **Step 0** SSH dev，手动 apply `20260420_230000_create_user_chatbot_permission.sql`（创建 chatbot 权限表）
2. **Step 1** SSH dev，手动 apply `20260420_230001_backfill_default_template_permissions.sql`（backfill 存量）
3. **Step 2** 立即跑二次 apply 验证幂等（行数不变）
4. **Step 3** 抽 3 个存量子账号，执行 `/v1/sop/templates` API（用各自 token）→ 记录 before/after 模板列表差异为 0
5. **Step 4**【确认 Step 1-3 全部 PASS 后】merge feature branch → develop → push origin develop（CI 自动触发 dev deploy）
6. **Step 5** CI deploy 完成后 gstack /qa 走关键流：
   - 父账号打开弹窗 → 看到 10 SOP + 9 chatbot（只 published，per D5 修正）
   - 取消某个 SOP 的授权 → 子账号登录 → SOP 消失
   - 授权某个 chatbot → 子账号登录 → chatbot 出现 + 能开会话
   - **撤销该 chatbot 授权 → 子账号在已有 session 继续发送消息 → 返回 ErrChatbotRunDenied**（P1-B 新增关键路径）
   - 子账号直连 API 传未授权 chatbot_id `POST /v1/chatbot/sessions` → 403（P0.5 路径，无法走 UI）

**独立性**：依赖 Task 1~6 全部完成。

---

### Task S5 — 验证策略确认 （独立文档任务）

**目标**：锁定 S5 验证方式，产出 `numind-server/docs/superpowers/qa/2026-04-20-child-run-permission-qa.md` 的验收清单模板。

**选择**：**gstack /qa 浏览器手动验证 + backfill migration SQL 级验证 + minimal Playwright request-level E2E**

**【S3 Gate reviewer 建议升级】**：纯 gstack /qa 对 API 直连和 ChatStream 撤销场景覆盖不稳定，加最小 Playwright 补齐。

**理由**：
1. 权限判定是后端核心，已由 Task 2~4 的 Go TDD 密集覆盖（~18 unit test 含 review 追加的 3 个缺口），回归保护充分
2. 前端弹窗新增 chatbot 区块是对称复制 SOP 区块，UI 回归面有限
3. 然而存量保护 + 撤销即时生效是 P0 约束，需要显式 SQL 和 HTTP-level 验证
4. Playwright request-level test 只跑 API 直调（不需要浏览器），维护成本低

**分工**：
- **gstack /qa**：UI 弹窗交互、列表渲染、勾选保存、子账号登录后可见性
- **SQL pre/post 对比**：backfill 幂等 + 抽样 3-5 个存量子账号的 `user_template_permission` 行数 before/after
- **Playwright request API**：高风险路径纯 HTTP 验证（不走 UI）

**关键用户路径**（S5 必须验证）：

| # | 路径 | 验证工具 |
|---|------|---------|
| P0.1 | backfill 前后任一存量子账号的 `/v1/sop/templates` 返回条目数 0 差异 | SQL + HTTP assertion |
| P0.2 | 新建子账号默认 empty list（SOP + chatbot 都为空） | gstack /qa UI |
| P0.3 | 父账号授权 + 子账号登录 → 能看到、能运行 | gstack /qa UI |
| P0.4 | 父账号撤销 + 子账号运行 → 403 | gstack /qa UI |
| **P0.5** | **子账号直连 API `/v1/chatbot/sessions` 传未授权 chatbot_id → 403** | **Playwright request-level**（必需，UI 测不到） |
| **P0.6** | **撤销即时生效：已有 session + 撤权 + 再 chat → ErrChatbotRunDenied** | **Playwright request-level**（必需） |
| P0.7 | 父账号自己调 API 不受限（regression 确认） | gstack /qa |

**Playwright spec 文件**（Task 7 实施时创建）：
- `numind-web-v3/e2e/child-run-permission-api.spec.ts` —— 纯 request.fetch，2 个 test 用例 P0.5 + P0.6

**单测追加**（S3 Gate review 要求）：
- `TestHasTemplatePermission_WhitelistMissAfterSoftDelete`（Task 2 加）—— 软删除的 permission 行应视为无效
- `TestChatStream_AfterRevoke_Denied`（Task 4 加）—— 已在 Task 4 测试列表中
- `TestGrantChatbots_SelfParentBypassed`（Task 3 加）—— 父账号调 grant 对自己不触发子账号校验

## §2 依赖关系

```
Task 1 (migration + model)
   │
   ├──▶ Task 2 (store) ──┐
   │                     │
   │                     ├──▶ Task 3 (biz customer)
   │                     │         │
   │                     │         ├──▶ Task 5 (controller + router)
   │                     │         │         │
   │                     │         │         └──▶ Task 6 (frontend)
   │                     │         │                   │
   │                     └──▶ Task 4 (biz chatbot)     │
   │                                      │            │
   └──────────────────────────────────────┴──────────▶ Task 7 (联调验收)
```

**并行机会（subagent teams）**：
- Task 3 和 Task 4 都依赖 Task 2，但彼此独立 → 可并行 dispatch
- Task 5 依赖 Task 3，可在 Task 4 完成后启动（或 Task 3 完成即可，Task 5 只用 customer biz 不用 chatbot biz）
- Task 6 依赖 Task 5 API 就绪

**按依赖拓扑的最快路径**：
1. Task 1（串行）
2. Task 2（串行）
3. Task 3 + Task 4（并行 dispatch）
4. Task 5（等 Task 3 done）
5. Task 6（等 Task 5 done）
6. Task 7（串行收尾）

## §3 每个 task 的两阶段 review（NDF 规则 6）

每个 task 完成并 commit 后，**必须**dispatch 2 个 Sonnet subagent 做 review：

1. **Spec Compliance Review**：对照 `spec + plan + this task's 验收` 检查完整性、边界漏洞、越权路径
2. **Code Quality Review**：语义清晰、错误处理、测试覆盖、Go idiom、命名规范

任何 P0 必须修复并重新 review；P1 必须修复；P2 能现场修则修。两个 review 都 PASS 后 `progress.reviewed_tasks += 1`。

## §4 Git 流程

- 每个 task 一个 commit（Conventional Commits 格式）
- 合入 develop 之前所有 task 都在 `feature/child-run-permission` 分支
- manifest 在每个 task 完成后更新 `progress.completed_tasks` 和 `progress.reviewed_tasks`
- 每个 task 的 commit 顺序由依赖决定，但都可以随时 push feature branch 到 remote
- **【P1-A review 约束】S6 merge 必须满足前置**：
  1. 先 SSH dev 手动 apply create + backfill migration（Task 7 Step 0-1）
  2. SQL 级验证 P0.1 通过（backfill pre/post 0 差异）
  3. **然后才能**执行 `git checkout develop && git merge --no-ff feature/child-run-permission && git push origin develop`
  4. CI deploy 完成后跑 Playwright + gstack /qa 覆盖 P0.2-P0.7

## §5 Rollback 场景

| 场景 | 动作 |
|------|------|
| backfill 出错（跑到一半崩） | 运行 backfill_rollback.sql（DELETE 时间窗口内记录），重新跑 backfill |
| 代码上线后发现权限误屏蔽存量子账号 | 立即 revert develop 代码 commit + 不必回滚 DB（backfill 行保留不影响旧代码语义） |
| 代码 + migration 都需要回滚 | 先 revert 代码 → 再运行 backfill_rollback.sql → 再 drop user_chatbot_permission 表（若已启用 chatbot 权限） |

## §6 Gate checklist

进入 S4 前确认：
- [ ] 本 plan 通过独立 Sonnet reviewer 审查（原子性 + S5 策略合理性）
- [ ] Feature A 已 merge 到 develop 且 dev 验证 OK
- [ ] 两个 feature 分支创建完成
- [ ] manifest 更新到 `stage: S3-done`
