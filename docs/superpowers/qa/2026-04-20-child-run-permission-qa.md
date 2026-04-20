# child-run-permission — S5 QA Report

- **Date**: 2026-04-20
- **Environment**: dev (49.233.219.254)
- **Feature branches**:
  - numind-server `feature/child-run-permission` @ `05cc585` (latest: Task 5 review fixes)
  - numind-web-v3 `feature/child-run-permission` @ `454f8d1` (latest: Task 6 review fixes)
- **Spec**: [`2026-04-20-child-run-permission-design.md`](../specs/2026-04-20-child-run-permission-design.md)
- **Plan**: [`2026-04-20-child-run-permission-plan.md`](../plans/2026-04-20-child-run-permission-plan.md)

---

## 1. 单元测试结果

| Layer | Tests | Status | Notes |
|-------|-------|--------|-------|
| Store (`internal/numind/store/`) | 13 | PASS | incl. P0 regression `TestHasTemplatePermission_WhitelistMissAfterSoftDelete` |
| Biz Customer (`internal/numind/biz/customer/`) | 14 | PASS | incl. `TestGrantChatbots_SelfParentBypassed` (S3 review追加) |
| Biz Chatbot (`internal/numind/biz/chatbot/`) | 5 | PASS | incl. P1-B regression `TestChatStream_AfterRevoke_Denied` |
| **Total new/regression tests** | **32** | **PASS** | All P0/P1 review-mandated regressions covered |

由 Tasks 2/3/4 的 implementer + reviewer subagent 在 `go test ./...` 下逐 task 验证。

---

## 2. Migration Dev 验证结果

### Step 1a — Create migration `20260420_230000_create_user_chatbot_permission.sql`

```text
CREATE TABLE user_chatbot_permission ✅
  - id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY
  - sub_user_id BIGINT UNSIGNED NOT NULL
  - chatbot_id BIGINT UNSIGNED NOT NULL
  - created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)  ← P2-B fix verified
  - UNIQUE KEY uk_ucp_sub_chatbot (sub_user_id, chatbot_id)       ← spec §3.1 verified
  - KEY idx_ucp_chatbot (chatbot_id)
  ENGINE=InnoDB CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
```

幂等保护：`CREATE TABLE IF NOT EXISTS`，二次运行无变化。

### Step 1b — Backfill baseline (BEFORE)

| Metric | Value |
|--------|-------|
| Total active rows in `user_template_permission` (deleted_at IS NULL) | **114** |
| Distinct sub-users with parent_user_id IS NOT NULL (deleted_at IS NULL) | **20** |
| Sub-users with `active_perms = 0` (eligible for backfill) | **0** |

存量 20 个 dev 子账号，每人都已经有 ≥2 条活跃权限记录（最少的 sub_user_id=59 有 2 条；其余 5~7 条）。

### Step 1c — Backfill apply #1 (`20260420_230001_backfill_default_template_permissions.sql`)

```text
total_active AFTER  = 114
total_active DELTA  = 0  (no zero-perm sub-users → NOT EXISTS guard correctly skipped all)
```

### Step 1d — Backfill apply #2 (idempotency check)

```text
total_active AFTER  = 114    (unchanged)
per-sub-user counts = unchanged
```

✅ **关键断言全部 PASS：**
- AFTER ≥ BEFORE（114 ≥ 114）— backfill 只加不减
- 二次 apply 无副作用 — `INSERT IGNORE` + `NOT EXISTS` + `deleted_at IS NULL` 三层幂等保护生效
- 无 sub-user 在 backfill 后处于 0 记录状态 — 翻转代码 deploy 后 deny-all 风险为 0

### Step 1e — 软删除场景（Task 1 已验证）

Task 1 在 dev 上跑过 `sub_user_id=51` 软删 + 重写 backfill 验证：曾被父账号撤权的 hard rows（deleted_at IS NOT NULL）不会让 NOT EXISTS 误判跳过 backfill。详见 manifest decisions「Task 1 done+reviewed」。

---

## 3. 关键用户路径状态（plan §S5）

| # | Path | 验证工具 | Status |
|---|------|---------|--------|
| P0.1 | backfill 前后任一存量子账号 `/v1/sop/templates` 条目数 0 差异 | SQL + HTTP assertion | ⏳ 待 dev deploy 后人工验证（migrations 已 apply, 等代码上线对照） |
| P0.2 | 新建子账号默认 empty list（SOP + chatbot 都为空） | gstack /qa UI | ⏳ S6 post-merge 验证 |
| P0.3 | 父账号授权 + 子账号登录 → 能看到、能运行 | gstack /qa UI | ⏳ S6 post-merge 验证 |
| P0.4 | 父账号撤销 + 子账号运行 → 403 | gstack /qa UI | ⏳ S6 post-merge 验证 |
| **P0.5** | **子账号直连 API `POST /v1/chatbot/sessions` 传未授权 chatbot_id → 403 ErrChatbotRunDenied** | **Playwright request-level** | **⏳ skipped（无 sub-user helper），转 gstack /qa 验证 — 见 `numind-web-v3/e2e/child-run-permission-api.spec.ts` 头注释** |
| **P0.6** | **撤销即时生效：已有 session + 撤权 + 再 chat → ErrChatbotRunDenied** | **Playwright request-level** | **⏳ 同上，已由 biz 层 P1-B 单测 `TestChatStream_AfterRevoke_Denied` 自动覆盖；UI 层 gstack /qa 二次确认** |
| P0.7 | 父账号自己调 API 不受限（regression 确认） | gstack /qa | ⏳ S6 post-merge 验证 |

**P0.5 / P0.6 Playwright 跳过决策依据**：现有 `e2e/auth.setup.ts` + `e2e/helpers/credits-admin.ts` 仅支持单账号登录 + page.route() mock fixture。创建子账号 + 取子账号 token 的 helper 不在 Task 7 scope 内，且实施会污染 dev DB。S3 Gate review 允许的 fallback 是 gstack /qa 人工验证 + 后端 TDD 覆盖（已有 `TestChatStream_AfterRevoke_Denied` 单测）。

---

## 4. Spec / Plan 偏离声明

| 项目 | 计划 | 实际 | 理由 |
|------|------|------|------|
| Playwright request-level E2E (P0.5/P0.6) | 自动化 | 跳过（`test.describe.skip`） | 无既有 sub-user helper，构建 helper 超出 Task 7 范围；后端 P1-B 单测 + gstack /qa 双重覆盖足够 |
| Backfill 行数对比 | sub-user 数 × 已发布 SOP 数 | 0（dev 无 0-perm 子账号） | dev 数据天然满足"已有权限"前提，验证守卫工作正常即可 |

---

## 5. S6 / S7 部署清单（交给主控 / 用户执行）

### S6 Dev Merge（按此顺序，关键路径）

1. ✅ **已完成**：dev DB migrations 应用并验证（Step 1a-1e 全部 PASS）
2. **numind-server merge**：
   ```bash
   cd numind-server
   git checkout develop && git pull origin develop
   git merge --no-ff feature/child-run-permission -m "merge feature/child-run-permission into develop (S6)"
   git push origin develop
   ```
3. **numind-web-v3 merge**：
   ```bash
   cd numind-web-v3
   git checkout develop && git pull origin develop
   git merge --no-ff feature/child-run-permission -m "merge feature/child-run-permission into develop (S6)"
   git push origin develop
   ```
4. CI 自动触发 dev deploy（推 develop 后 ~3-5 min）
5. **部署完成后 gstack /qa 覆盖剩余路径**：P0.1（抽 sub_user_id=26/52/57 三个存量子账号登录看 `/v1/sop/templates`）、P0.2（新建测试子账号）、P0.3 / P0.4（授权/撤权流）、P0.7（父账号 regression）、**P0.5 / P0.6**（用浏览器双标签页代替 Playwright 验证 chatbot 直调和撤权即时生效）

### S7 Prod Deploy（按此顺序）

1. SSH prod 备份相关表：
   ```bash
   mysqldump --single-transaction --skip-lock-tables numind-prod \
     user_template_permission user > /backup/utp_$(date +%Y%m%d_%H%M%S).sql
   ```
   （`user_chatbot_permission` 表 prod 上不存在，新建表无须备份）
2. **Prod DB apply create migration**（先）：`20260420_230000_create_user_chatbot_permission.sql`
3. **Prod DB apply backfill migration**（后）：`20260420_230001_backfill_default_template_permissions.sql`
4. **抽样验证**：prod 子账号 (38+) 抽 5 个对比 backfill 前后 `user_template_permission` 行数，应满足 AFTER ≥ BEFORE 且无空集
5. numind-server + numind-web-v3 打 release tag `v2.1.7`，CI 自动 prod deploy
6. **Prod smoke test**：用一个 prod 子账号登录用户端，验证 SOP 和 chatbot 可见范围 = backup 前快照

---

## 6. 已知限制 / Tech Debt

- **Controller 错误透传不一致**：新 chatbot controller 用 `core.WriteResponse(c, err, nil)` 透传 biz errno（正确映射 HTTP 403/404）；既有 `GrantTemplates` 用 `InternalServerError.SetMessage` collapse 到 500。两套行为在同 controller 共存属 follow-up tech debt（manifest Task 5 decision 已记）。
- **Playwright sub-user helper 缺失**：本 feature 不在 scope 内补；future feature 若需要多账号 E2E 应优先建 helper。

---

*Last updated: 2026-04-20 by S4-Task7 implementer*
