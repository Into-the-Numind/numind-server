# S5 验证策略 — sop-salesrag-parent-scope

**Feature**: sop-salesrag-parent-scope
**Date**: 2026-05-18
**对应 NDF S5 阶段**：本需求 S4 编码完毕后，按本文档跑 S5 自动验收。

## 验证方式选择

**选择**: Playwright E2E + Go 单测 + 集成测试 三层组合

**理由**: 本需求是**后端跨层多租户隔离**修复，UI 完全无感（前端零改动）。
但功能涉及**高风险数据可见性**——bug 会导致跨机构数据泄露或本机构业务停摆。

| 候选方式 | 选 / 不选 | 理由 |
|---------|----------|------|
| 仅 Go 单测 | 不够 | 单测 mock store 层，无法验证 SQL 是否真的过滤了跨租户数据 |
| 仅 gstack /qa | 不够 | /qa 是一次性截图验证，不留持久化回归保护；高风险路径需要 commit 化的 spec |
| Playwright E2E | ✓ 选 | 端到端，持久化在 e2e/ 目录作为回归保护 |
| Go 单测 + 集成测试 | ✓ 选 | 单测覆盖矩阵广度，集成测试覆盖 SQL/DB 真实行为 |

## 关键用户路径（5 条，Playwright spec 必覆盖）

每条路径在 `numind-web-v3/e2e/sop-salesrag-parent-scope.spec.ts` 中独立 test。
登录凭据来自 `E2E_USERNAME` / `E2E_PASSWORD` 环境变量（参见 .claude/rules/testing.md §2）。

### 路径 1: user 30 父账户登录 — 修复前后行为完全一致

1. 用 user 30 的凭据登录用户端 (E2E_USERNAME=user_moxiaopai)
2. 导航到 /home
3. **断言**: AI 工作流区显示 3 张卡片（小红书图文生成智能体 / AI文稿创作：流量选题口播稿 / AI朋友圈：深度思考文案）
4. **断言**: AI 智能体区显示销售智能体磁贴
5. **断言**: 销售智能体磁贴点击后跳转 /sales

### 路径 2: user 30 子账户登录 — 体验不变

1. 用 user 30 子账户凭据登录（如 sub_user_id=345）
2. 导航到 /home
3. **断言**: 看到 3 SOP（has_permission 状态按子账户授权）
4. **断言**: 销售智能体磁贴可见（因父账户 owner + 子账户在 user_feature_permission 有授权）

### 路径 3: admin 登录 — 空工作区

1. 用 admin 凭据登录用户端
2. 导航到 /home
3. **断言**: AI 工作流区 0 张卡片
4. **断言**: AI 智能体区无销售智能体磁贴
5. **断言**: 调用 `GET /v1/sales-rag/check-permission` 返回 `has_permission=false`

### 路径 4: admin 访问 content_monitor / self_service_config — 行为不变（回归保护）

1. 用 admin 凭据登录
2. **断言**: 访问 `/v1/monitor` 端点返回 200（父账户 bypass 保留）
3. **断言**: 访问 `/v1/config/*` 系列端点正常（self_service_config bypass 保留）

### 路径 5: 模拟新机构父账户隔离

1. 通过 admin 端创建一个临时测试父账户 `test_parent_isolation`（测试结束删除）
2. 登录 `test_parent_isolation`
3. **断言**: AI 工作流区 0 卡片
4. **断言**: 销售智能体磁贴不可见
5. **断言**: 数据库 `sales_agent_owner` 表无该用户行（验证新机构默认不享有销售智能体）

## Go 单测覆盖矩阵（参考 spec §6.1, §6.2）

| 文件 | 测试数 | 覆盖点 |
|------|--------|--------|
| `store/sales_agent_owner_test.go` | 4 | Exists 真/假 + 不同 parent + DB 错误 |
| `store/sop_template_visibility_test.go`（扩展） | 3 | owner 过滤 / IS NOT NULL 防御 / 不存在 parent |
| `store/customer_test.go`（迁移） | 3 | CheckSubUserFeatureGrant 真/假/feature_key 不串味 |
| `store/customer_permission_lifecycle_test.go`（迁移） | 11 | 11 处历史 HasFeaturePermission 调用全部迁移 |
| `biz/customer/customer_test.go`（扩展） | 9 | CheckFeaturePermission 矩阵（spec §6.2 表）|
| `biz/sop/sop_test.go`（扩展） | 7 | SOP biz：list 父/子用户、Create 父成功/子拒、admin Create 成功/缺 ID |
| **合计** | **37 单测** | |

## 集成测试（migration 真实跑通）

参考 visibility-scope feature 的 `migrations/audit/test_migration_*.go` 模式：

| Test | 覆盖点 |
|------|--------|
| `TestMigration_Idempotent` | 跑两次迁移 → 第二次零错误，行数不变 |
| `TestMigration_AfterRunUser30Visibility` | 跑完后 user 30 list 行数 = 3（id 1, 2, 4）|
| `TestMigration_AfterRunAdminVisibility` | 跑完后 admin list 行数 = 0 |
| `TestMigration_AfterRunSalesAgentPermission` | user 30 → has_permission=true; admin → false |
| `TestMigration_FKCascade` | 在 user 表 DELETE id=30 后，sales_agent_owner(parent_user_id=30) 自动消失 |

## 回归保护承诺

- **Playwright E2E 持久化**：5 条路径的 spec 提交到 git (`numind-web-v3/e2e/sop-salesrag-parent-scope.spec.ts`)，未来任何 SOP/chatbot/sales_agent 权限相关改动都会触发完整跑。**不是 gstack /qa 一次性验证**。
- **Go 单测自动 race detection**：S4 编码完毕用 `task test`（含 `-race`）跑完整测试套件。
- **集成测试纳入 CI**：在 numind-server `task test` 中包含 migration audit 测试。

## S5 执行顺序

S4 完成后，按以下顺序：

1. **启动本地 numind-server**（`cd numind-server && task dev`）
2. **跑后端单测**：`go test ./... -race` 期望全 PASS
3. **跑 migration 集成测试**：覆盖 §"集成测试" 表中 5 个 test
4. **启动本地 numind-web-v3**（`cd numind-web-v3 && npm run dev`）
5. **跑 Playwright E2E**：`E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e -- sop-salesrag-parent-scope.spec.ts`
6. **填 QA 报告**到 `numind-server/docs/superpowers/qa/2026-05-19-sop-salesrag-parent-scope-qa.md`（用 `templates/ndf/qa-report.md`）

## 不在范围内

- 性能/压测：本需求不涉及性能敏感路径
- Langfuse trace 验证：本需求 0 LLM 调用
- 视觉/像素 diff：前端零改动

## 与 NDF Rule 10 对齐

NDF Rule 10 强制 S3 plan 中必须有"S5 验证策略" task（本文档由 Task 6 产出）。
本文档由 S3 plan reviewer 一并审过原子性。
进 S5 前主控 AI 必须再次核对本文档的 5 条用户路径都被 Playwright spec 实现。
