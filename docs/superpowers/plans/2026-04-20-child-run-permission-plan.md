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

**验收**：
- SQL 在 dev DB 运行成功（create + 二次 apply 验证幂等）
- Go `go build ./...` pass
- Migration 文件头部注释说明执行顺序（create 先、backfill 后）

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

**目标**：`chatbotBiz` 的 `ListVisibleChatbots` 加白名单过滤；`CreateSession` / `Chat` 加权限守卫。

**文件改动**：
- `numind-server/internal/numind/biz/chatbot/chatbot.go`（修改）
- `numind-server/internal/numind/biz/chatbot/chatbot_test.go`（新增 ~4 测试）
- `numind-server/internal/pkg/errno/code.go` 或对应错误码文件（新增 `ErrChatbotRunDenied`）

**改造点**：
1. `ListVisibleChatbots`: 子账号时用 `ds.Customers().ListSubUserChatbotIDs` 拉白名单，set 交集过滤
2. `CreateSession(ctx, userID, chatbotID)`: entry 先调 `HasChatbotPermission`
3. `Chat(ctx, userID, sessionID, ...)`: 通过 session → chatbotID → `HasChatbotPermission` 链式检查
4. `ListSessions` / `ListMessages`: 不加额外守卫（session 已绑 user_id，无法访问别人的 session；父账号撤销权限后历史 session 仍可读，符合 PRD 的"撤销即时生效"= 阻止新运行而不销毁历史）

**测试**：
- `TestListVisibleChatbots_ChildFiltered`
- `TestListVisibleChatbots_ParentAll`
- `TestCreateSession_ChatbotRunDenied`
- `TestCreateSession_ChatbotAllowed`

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

**目标**：在 dev 环境运行 2 个 migration，执行端到端联调。

**步骤**：
1. SSH dev，手动 apply create migration（`user_chatbot_permission` 表）
2. 手动 apply backfill migration
3. 验证存量子账号 before/after 的 `user_template_permission` 行数一致（抽 3 个子账号）
4. 部署最新 develop（CI 自动触发）
5. gstack /qa 走一遍关键流：
   - 父账号打开弹窗 → 看到 10 SOP + 9+1 chatbot
   - 取消某个 SOP 的授权 → 子账号登录 → SOP 消失
   - 授权某个 chatbot → 子账号登录 → chatbot 出现 + 能开会话
   - 撤销该 chatbot 授权 → 子账号尝试开会话 → 403

**独立性**：依赖 Task 1~6 全部完成。

---

### Task S5 — 验证策略确认 （独立文档任务）

**目标**：锁定 S5 验证方式，产出 `numind-server/docs/superpowers/qa/2026-04-20-child-run-permission-qa.md` 的验收清单模板。

**选择**：**gstack /qa 浏览器手动验证 + backfill migration SQL 级验证**

**理由**：
1. 权限判定是后端核心，已由 Task 2~4 的 Go TDD 密集覆盖（~15 unit test），回归保护充分
2. 前端弹窗新增 chatbot 区块是对称复制 SOP 区块，UI 回归面有限
3. E2E Playwright 的维护成本对权限场景性价比低（权限组合爆炸，单测更适合）
4. 然而存量保护（backfill 幂等性 + 子账号可见范围 0 差异）是核心 P0 约束，需要显式 SQL level 验证（pre/post count + diff 抽样）

**关键用户路径**（S5 必须验证）：
- P0.1 backfill 前后任一存量子账号的 `/v1/sop/templates` 返回条目数 0 差异
- P0.2 新建子账号默认 empty list（SOP + chatbot 都为空）
- P0.3 父账号授权 + 子账号登录 → 能看到、能运行
- P0.4 父账号撤销 + 子账号运行 → 403
- P0.5 子账号直连 API `/v1/chatbot/sessions` 传未授权 chatbot_id → 403
- P0.6 父账号自己调 API 不受限（regression 确认）

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
- S6 merge：两仓库同时 `git checkout develop && git merge --no-ff feature/child-run-permission && git push origin develop`

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
