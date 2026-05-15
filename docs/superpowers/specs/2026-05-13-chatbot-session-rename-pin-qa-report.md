# Chatbot 会话改名与置顶 — S5 QA 报告

- **Feature ID**: `chatbot-session-rename-pin`
- **NDF Stage**: S5 (自动验收完成 / 部分 deferred)
- **Created**: 2026-05-14
- **Source**: validation-strategy.md + plan §4 验收总结

---

## §1 验证总览

| 验证项 | 范围 | 结果 |
|--------|------|------|
| 后端 `task lint` | numind-server-rename-pin worktree | ✅ exit 0 |
| 后端 `task test` (含 race detection + coverage) | numind-server-rename-pin worktree | ✅ 0 FAIL（biz/chatbot coverage 34.6%）|
| 后端 store 单元测试 (T2, 10 tests) | `internal/numind/store/chatbot_session_test.go` | ✅ 10/10 PASS |
| 后端 biz 单元测试 (T3, 8 tests) | `internal/numind/biz/chatbot/chatbot_test.go` | ✅ 8/8 PASS（含 EC-14 重复置顶刷新 + D2 不变量）|
| 前端 `npm run lint` | numind-web-v3-rename-pin worktree | ✅ 0 errors（5 pre-existing warnings）|
| 前端 `npm run type-check` | numind-web-v3-rename-pin worktree | ✅ exit 0 |
| 前端 `npm run test:unit` (Vitest) | 40 test files, includes T6 8 chatbot tests | ✅ 380 PASS / 23 skipped / 0 fail |
| 前端 Vitest store 测试 (T6, 8 tests) | `src/stores/__tests__/chatbot.spec.ts` | ✅ 8/8 PASS（含 pessimistic UI 失败不更新本地 + 排序 3 case）|
| Playwright E2E (10 关键路径) | `numind-web-v3/e2e/chatbot-session-rename-pin.spec.ts` | ⏸️ **Deferred to S6** — 见 §3 |
| 手动 curl 401 / 403 验证 | dev backend | ⏸️ **Deferred to S6** — 同 |
| Langfuse trace 验证 | N/A | ⏭️ 本 feature 不涉及 LLM 调用 |

---

## §2 单元测试覆盖（已 PASS）

### 后端 store 测试 (T2)
| 测试名 | 验证内容 | 结果 |
|--------|---------|------|
| `TestUpdateTitle_Success` | 正常 rename | ✅ |
| `TestUpdateTitle_NotFound_ReturnsErrRecordNotFound` | RowsAffected==0 → ErrRecordNotFound | ✅ |
| `TestUpdateTitle_DoesNotRefreshUpdatedAt` | **D2 关键不变量** | ✅ |
| `TestSetPinnedAt_SetThenClear` | pin → unpin 链路 | ✅ |
| `TestSetPinnedAt_DoesNotRefreshUpdatedAt` | **D2 不变量** | ✅ |
| `TestSetPinnedAt_NotFound` | RowsAffected==0 ErrRecordNotFound | ✅ |
| `TestListSessionsByChatbot_PinnedFirstThenUnpinned` | **spec §4.1 3-行 case** A pinned 10:00 / B pinned 09:00 / C unpinned 13:00 → A,B,C | ✅ |
| `TestListSessionsByChatbot_OnlyPinned_OrderByPinnedAtDesc` | 全置顶按 pinned_at DESC | ✅ |
| `TestListSessionsByChatbot_OnlyUnpinned_OrderByUpdatedAtDesc` | 全非置顶按 updated_at DESC | ✅ |
| `TestListSessionsByChatbot_FilteredByChatbotID` | chatbot_id WHERE 隔离 | ✅ |

### 后端 biz 测试 (T3)
| 测试名 | 验证内容 | 结果 |
|--------|---------|------|
| `TestRenameSession_TrimEmpty_ReturnsBindError` | 空白 title → ErrBind | ✅ |
| `TestRenameSession_OverLimit_ReturnsBindError` | 超 200 字 → ErrBind | ✅ |
| `TestRenameSession_NotOwner_ReturnsForbidden` | session.UserID != userID → ErrForbidden | ✅ |
| `TestRenameSession_SoftDeleted_ReturnsSessionNotFound` | GetSession ErrRecordNotFound → ErrSessionNotFound | ✅ |
| `TestPinSession_PinFirstTime` | pinned=true 返回非 nil *time.Time | ✅ |
| `TestPinSession_Unpin` | pinned=false 返回 nil + 写 NULL | ✅ |
| `TestPinSession_RepinRefreshesPinnedAt` | **EC-14 重复置顶刷新** | ✅ |
| `TestPinSession_NotOwner_ReturnsForbidden` | ownership check | ✅ |

### 前端 Vitest 测试 (T6)
8 个测试覆盖：renameSession 成功/失败、togglePin pin/unpin + 排序、sortSessionsLocally 3 场景、fetchSessions 携带 chatbot_id 参数。**全 PASS**.

---

## §3 Deferred — E2E + 手动 curl 留给 S6

### 决策理由

1. **config_local.yaml 实际指向 dev DB**（`49.233.219.254:13306` numind-dev），不是真本地数据库。本地启动后端需要 dev DB 已 apply migration
2. **dev DB 上 `chatbot_session.pinned_at` 列未存在** — 必须先 SSH 到 dev server 手动 ALTER TABLE。该步骤本来就是 S6 的责任（参考 memory `project_dev_deploy_migration_gap`：CI 镜像无 migrations 目录，所有 schema 变更都需手动 SSH apply）
3. **S5 自动化覆盖已经充分**：
   - 后端 10 + 8 = 18 个单元测试覆盖 store + biz 全部逻辑分支（含 D2 不变量、EC-14 重复置顶、ownership、SQL 排序 3-case）
   - 前端 8 个 Vitest 覆盖 store actions + 排序 + API 参数
   - controller 层 + router 注册由 Go build + lint 编译期验证（spec §3.3 严格 controller 纯度，逻辑全在 biz 已被 unit test 覆盖）
4. **S6 阶段必跑**：S6 protocol 是 merge develop → CI 部署 dev → 人类验收。这一步骤将 SSH apply migration 后跑 E2E + 手动 curl，与 visibility-scope 在 S6 用过的 pattern 一致
5. **风险评估**: 跳过 S5 E2E 的风险**低** — 单元测试覆盖核心契约，前端 UI 改动有清晰路径在 S6 dev 浏览器手动 QA + Playwright 验证

### S6 必跑清单

按 validation-strategy.md §3 10 条路径：

| 路径 | 类型 | S6 执行方式 |
|------|------|------------|
| #1 改名 happy path | E2E | Playwright + 浏览器 QA |
| #2 改名空白校验 | E2E | Playwright（toast 验证）|
| #3 置顶 happy path | E2E | Playwright + 视觉验证（左边框 + 📌） |
| #4 重复置顶 | E2E | **双重断言**: API response pinned_at_B > pinned_at_A + UI 位置 |
| #5 取消置顶 | E2E | Playwright + 位置变化 |
| #6 **`updated_at` 不变量验证（核心 D2）** | E2E + API | 操作前后 GET 比较 updated_at 应相等 |
| #7 删除菜单迁移 | E2E | Playwright 验证菜单内删除走 ConfirmModal |
| #8 跨 chatbot 隔离 | E2E | chatbot A 改名 → chatbot B 不可见 |
| #9 未登录 401 | 手动 curl | 不带 token → 401 |
| #10 非本人 403 | 手动 curl | user A token 改 user B session → 403 |

### S6 阻塞前置

1. ✅ S5 自动化测试 PASS
2. ⏸️ SSH 到 dev server `49.233.219.254` 跑 forward migration（参 `migrations/20260513_120000_add_chatbot_session_pinned_at.sql`）
3. ⏸️ Merge feature 分支到 develop（两仓）→ CI 自动 deploy dev
4. ⏸️ 跑 #1-#8 Playwright E2E
5. ⏸️ 手动 curl #9-#10
6. ⏸️ 人工 QA 截图（gstack /qa 或浏览器手动）

---

## §4 S5 Gate 检查清单

按 NDF §3 S5 gate：

- ✅ S4 计算检查重跑: `task lint` + `task test` (完整版) — PASS
- ✅ 前端 `npm run lint` + `npm run type-check` + `npm run test:unit` — PASS
- ⏸️ `npm run test:e2e` — Deferred to S6（理由见 §3）
- ⏸️ 浏览器 QA — Deferred to S6
- ✅ 可观测性: N/A（本 feature 不涉及 LLM 调用，spec §3 已明确）

**Conditional PASS**: S5 gate **以 deferred E2E + 手动 curl** 的明确条件通过。S6 必须在 dev 部署后补齐 #1-#10 验证步骤，作为 S6 人工验收的硬门禁前置。

---

## §5 与 spec/plan 对应

| Spec / Plan 节 | S5 验证状态 |
|---------------|------------|
| spec §1 数据模型 | Migration SQL 文件已 commit；dev apply 留 S6 |
| spec §2 API 契约 | controller 编译通过 + biz unit tests 覆盖；E2E 留 S6 |
| spec §3 store/biz/controller 实现 | 18 unit tests 全 PASS |
| spec §4 排序 SQL 兼容性 | T2 3-行 case in-memory SQLite 验证 PASS（MySQL 8 在 S6 dev apply 后实跑验证）|
| spec §5 前端设计 | lint + type-check + 8 Vitest PASS；UI 浏览器验证留 S6 |
| spec §6 17 条边界 case | 18 + 8 = 26 unit tests + Go build/lint 覆盖大多数；剩 401 / 403 / 跨设备同步 留 S6 |
| spec §7 测试策略 | 单元测试三件套全跑；Playwright E2E + curl 留 S6 |
| D2 `updated_at` 不变量 | T2 + T3 单元测试已验证（UpdateColumn 行为锁定）;  S6 E2E 复测真实 MySQL |
| EC-14 重复置顶刷新 | T3 `TestPinSession_RepinRefreshesPinnedAt` PASS；S6 E2E 双重断言复测 |

---

## §6 风险评估

| 风险 | 概率 | 严重性 | 缓解 |
|------|------|--------|------|
| MySQL 8 `ORDER BY pinned_at IS NULL ASC, pinned_at DESC, updated_at DESC` 行为与 SQLite test 不同 | Low | High | S6 dev apply migration 后第一时间手动 query 验证排序输出符合 expectation |
| `UpdateColumn` 在真实 MySQL 上仍刷新 updated_at（GORM 版本差异）| Low | High | S6 E2E 路径 #6 操作前后 GET 比较 updated_at（核心 D2 不变量）|
| ChatbotChat.vue UI 在真实浏览器渲染问题（hover / dropdown / inline modal）| Med | Med | S6 人工 QA 关键路径截图 |
| 跨 feature 冲突（visibility-scope 已 S7 上线） | Low | Low | reviewer 已验证不耦合；merge develop 时若 manifest 冲突用 worktree 隔离解决 |

---

## §7 总评

S5 **Conditional PASS**：
- 所有可自动化的本地验证全部 PASS（lint / type-check / 单元测试 26 个 / Go build / Vitest 380 个）
- E2E + 手动 curl + 真实 MySQL schema 验证 deferred 到 S6（这是 numind 项目历史 pattern，visibility-scope 同样做法）
- S6 阻塞前置清单已明确（SSH apply migration + merge develop + E2E + curl + QA 截图）

Feature 可以进入 S6 dev 部署阶段。
