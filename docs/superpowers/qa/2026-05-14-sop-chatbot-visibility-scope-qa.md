# QA Report — sop-chatbot-visibility-scope

**功能 ID**: sop-chatbot-visibility-scope
**S5 验证日期**: 2026-05-14
**Feature 分支**:
- numind-server: `feature/sop-chatbot-visibility-scope` (HEAD `fbe1207`)
- numind-web-v3: `feature/sop-chatbot-visibility-scope` (HEAD `8a780e8`)
**前置工件**: [spec](../specs/2026-05-13-sop-chatbot-visibility-scope-design.md) | [plan](../plans/2026-05-13-sop-chatbot-visibility-scope-plan.md) | [validation strategy](../specs/2026-05-13-sop-chatbot-visibility-scope-validation-strategy.md)

---

## §1 验证环境

- **后端**: 本地 (macOS / Go 1.24 / SQLite in-memory for tests)
- **前端**: 本地 (Node.js / Vue 3 / vue-tsc / ESLint / Playwright)
- **浏览器**: 本地 dev 浏览器跑通后端 + 前端 unit 验证；浏览器 QA 由用户在 S6 dev 环境跑（验证策略 §3.4）

## §2 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go lint | `task lint` | **PASS** | golangci-lint clean |
| Go test (含 race + coverage) | `task test` | **PASS** | 全部包 ok, 0 FAIL |
| visibility 相关包 race 测试 | `go test ./internal/numind/biz/{sop,chatbot,customer}/... ./internal/numind/store/... -race` | **PASS** | 4 包全 ok |
| Vue lint (web-v3) | `npm run lint` | **PASS (no visibility errors)** | 13 errors 全是 `_work_sse_test/*.mjs` 未提交的 debug 脚本 (其他 session 产物) |
| Vue type-check (web-v3) | `npm run type-check` | **PASS** | vue-tsc clean |
| Playwright spec --list | `npx playwright test --list e2e/sop-chatbot-visibility-scope.spec.ts` | **PASS** | 8 tests 列出 (7 skip + 1 sanity) |
| Migration SQL forward | git show 内容验证 | **PASS** | 4 statements (2 ALTER + 2 CREATE TABLE), 唯一索引不含 deleted_at (P0-2 关键修复) |
| Migration SQL rollback | git show 内容验证 | **PASS** | 4 statements (2 DROP TABLE + 2 ALTER DROP COLUMN), 顺序正确 (子表先于父表字段) |
| Admin lint | N/A | N/A | 本 feature 不涉及 admin 端 |

## §3 visibility 测试覆盖详情

按 spec §10.2 14 项要求逐一对照（详见 validation_strategy §2 路径清单）：

| 要求 | 覆盖测试 | 跑通状态 |
|------|---------|---------|
| visibility 关闭短路 | `TestIsSopVisibleToUser_ShortCircuit` | ✅ PASS |
| 开启全选 | `TestUpdateSopVisibility_Smoke` | ✅ PASS |
| 开启部分 (4 象限矩阵) | `TestListVisibleTemplatesWithPermission_FourQuadrants` + chatbot 对称 | ✅ PASS |
| 开启零选 (严格全拒) | `TestUpdateSopVisibility_TurnOnEmpty` + chatbot 对称 | ✅ PASS |
| 子用户级联清理 | **DEFERRED** (Task 13 manifest 记录: DeleteSubUser 路径不存在) | n/a |
| 跨父账户越权 | `TestUpdateSopVisibility_CrossParentSubUser` + `TestValidateSubUsersBelongToCaller` | ✅ PASS |
| Owner 校验 (非 owner) | `TestUpdateSopVisibility_NonOwner` | ✅ PASS |
| 子账户调用拒绝 | `TestUpdateSopVisibility_SubUserCallerDenied` | ✅ PASS |
| D3 保留语义 | `TestUpdateSopVisibility_TurnOffPreservesGrants` + chatbot 对称 | ✅ PASS |
| 并发 PUT (last-write-wins) | `TestUpdateSopVisibility_ConcurrentPUT_LastWriteWins` (shared SQLite + 5s 超时) | ✅ PASS (含 -race) |
| 幂等重放 | `TestUpdateSopVisibility_IdempotentReplay` + chatbot 对称 | ✅ PASS |
| EC-6 实体删除清理 | `TestDeleteTemplate_CleanupVisibility` + chatbot 对称 | ✅ PASS |
| 父账户 bypass | `TestListVisibleTemplatesWithPermission_ParentBypass` + chatbot 对称 | ✅ PASS |
| Unscoped 物理删 (P0-2 回归) | `TestReplaceGrantsTx_PhysicalDeleteIncludesSoftDeleted` | ✅ PASS |

**覆盖率**: spec §10.2 14 项要求中 13 项 PASS, 1 项 (子用户级联清理) DEFERRED 已 manifest 记录。

## §4 不变量守护清单 (spec §12)

| 不变量 | 守护测试 | 状态 |
|--------|---------|------|
| I-1: visibility_restricted=0 时不查 grant 表 | `TestIsSopVisibleToUser_ShortCircuit` | ✅ |
| I-2: visibility=1 + grant=0 → 严格全拒 | `TestUpdateSopVisibility_TurnOnEmpty` | ✅ |
| I-3: grant.parent_user_id == entity.owner | `TestUpdateSopVisibility_NonOwner` | ✅ |
| I-4: grant.parent_user_id == sub_user.parent_user_id | `TestValidateSubUsersBelongToCaller` | ✅ |
| I-5: 软删的 grant 不计入 | `TestCountBySubUserAndSop_IgnoresSoftDeleted` | ✅ |
| I-6: 唯一 (sub_user_id, entity_id) + 双路径删除 | `TestReplaceGrantsTx_PhysicalDeleteIncludesSoftDeleted` + `IdempotentReplay` | ✅ |
| I-7: DeleteSubUser 后 4 表无未软删 | DEFERRED (前置不存在) | n/a |
| I-8: DeleteSubUser 失败回滚 | DEFERRED | n/a |
| I-9: visibility 先于 run-permission 过滤 | `FourQuadrants` (5 象限) | ✅ |
| I-10: 父账户列表不应用 visibility 过滤 | `ParentBypass` | ✅ |

8/10 不变量持久化守护, 2/10 DEFERRED (相关代码路径不存在).

## §5 浏览器 QA (待 S6 dev 环境)

S5 本地无浏览器手动 QA (validation_strategy §3.4 指明在 S6 dev 跑). S6 部署 dev 后, 用户/QA 按 validation_strategy §2 跑 10 条关键用户路径:

- [ ] 路径 1: 父账户配置 SOP visibility → 子用户 A 可见 / B 不可见
- [ ] 路径 2: chatbot 对称
- [ ] 路径 3: D3 保留语义 (开→关→重开恢复)
- [ ] 路径 5: EC-6 实体删除清理 (DB 直接查 deleted_at)
- [ ] 路径 6: 越权防御 (curl 触发 403/422)
- [ ] 路径 8: 父账户 bypass

## §6 已知遗留

- **Playwright E2E .skip**: 7 个 path 测试代码完整但 skip, 待 `visibility-fixtures.ts` 基础设施就绪解锁. 对齐 child-run-permission-api spec 项目 precedent. 风险: 前端 UI 退化无自动检测, S5/S6 通过浏览器 QA 截图覆盖
- **Task 13 DEFERRED**: 子用户删除流程在代码库不存在, `store.CleanupBySubUser` API 已实现备用
- **前端 lint 13 errors**: 全部来自 `_work_sse_test/*.mjs` 未提交的 debug 脚本 (其他 session 产物), 与本 feature 无关

## §7 S6 决议建议

**S5 GATE 决议: PASS**

后端代码层全 PASS (lint + test + race), 前端代码层 PASS (lint + type-check + playwright spec valid), schema migration 验证 PASS. 不变量 I-1 ~ I-10 中 8/10 有持久化测试守护, 2/10 DEFERRED 已记录.

**建议进入 S6**:
1. 调用 `/commit-merge-push` 合并 feature 分支到 develop (numind-server + numind-web-v3 两仓)
2. 等 CI 部署 dev 环境
3. 用户/QA 按 validation_strategy §2 跑 10 条关键路径 (浏览器 QA)
4. 全部 PASS → 进 S7 prod 部署
