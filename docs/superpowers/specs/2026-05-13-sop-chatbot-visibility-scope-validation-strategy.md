# S5 验证策略 — sop-chatbot-visibility-scope

**功能 ID**: sop-chatbot-visibility-scope
**ndf_version**: 1.1
**前置工件**: [spec](2026-05-13-sop-chatbot-visibility-scope-design.md) | [plan](../plans/2026-05-13-sop-chatbot-visibility-scope-plan.md)
**作者**: AI (S4 Phase 9 Task 23, NDF Rule 10 强制末尾 task)
**日期**: 2026-05-14

---

## §1 验证方式选择（NDF Rule 10）

**选定方案：Playwright E2E（必需）+ 后端单元测试（必需）+ gstack /qa 浏览器截图回归（必需）**

### 1.1 理由

本功能为**权限主流程**，spec §10.4 已声明不接受仅靠 gstack /qa 一次性验证：

- **两层 gate 串行**（visibility → run-permission）：语义边界微妙，错位会导致权限语义反转
- **跨用户身份验证**：父账户配置后，子用户 A/B 在工作区列表的可见性必须验证
- **D3 保留语义跨多次操作的稳定性**：开 → 关 → 重开恢复名单，是核心 UX 设计
- **回归保护**：未来代码改动可能破坏不变量（I-1 ~ I-10），单元测试 + E2E 是持久化守护

### 1.2 三层验证矩阵

| 验证层 | 工具 | 覆盖范围 | 持久化 |
|--------|------|---------|--------|
| **后端单元测试** | `go test` | biz 函数 + store + 4 象限矩阵 + 幂等 + 并发 + EC-6 | ✅ 持久化 (29 PASS, race-clean) |
| **E2E API 测试** | Playwright | 跨身份 API 调用 + 跨父账户越权防御 + D3 保留语义 | ✅ 持久化 (skip 状态待 fixture 解锁) |
| **浏览器手动 QA** | gstack /qa | UI 渲染 + 交互流程 + 截图回归 | ⚠️ 一次性 |

### 1.3 测试基础设施现状（诚实声明）

- **后端单元测试**：✅ 完整覆盖（Phase 6 Task 16 完成，29 visibility 相关测试全 PASS，含 `-race`）
- **E2E**：⚠️ 代码已写但 `test.describe.skip`。原因：多 actor（parent + 2+ sub-users）流程需要 `visibility-fixtures.ts` 基础设施（创建/清理 sub-user 账户），该基础设施尚不存在。与 `child-run-permission-api.spec.ts` 同款 fallback（项目 precedent）。
- **gstack /qa**：S5/S6 手动执行，截图保留到 `numind-server/docs/superpowers/qa/`

---

## §2 关键用户路径清单

S5 执行人需逐条验证以下路径。每条标注**验证方式**（unit / E2E-skip / gstack-qa）。

### 路径 1: 父账户配置 SOP 可见范围 → 子用户列表生效

```
1. 父账户登录, 进入 SOP 模板编辑页
2. 打开「仅指定子用户可见」开关
3. SubUserMultiSelectDialog 弹出, 勾选子用户 A
4. 点击确认 → 保存
5. 子用户 A 登录: 工作区 SOP 列表能看到该 SOP
6. 子用户 B 登录: 工作区 SOP 列表看不到该 SOP
```

**验证方式**：
- **后端**：`TestListVisibleTemplatesWithPermission_FourQuadrants`（V/A + V/D + H/A + H/D + I/R 5 象限）
- **E2E**：Path 1（待 fixture 解锁）
- **gstack /qa**：S5 手动浏览器跑通流程 + 截图

### 路径 2: chatbot 路径（对称）

与路径 1 完全对称，仅资源类型从 SOP 改为 chatbot。

**验证方式**：
- **后端**：`TestListVisibleChatbotsWithPermission_FourQuadrants`
- **E2E**：Path 2（待 fixture 解锁）
- **gstack /qa**：S5 手动

### 路径 3: D3 保留语义

```
1. 父账户配置 SOP visibility, 选 3 个子用户
2. 关闭开关 → 弹"已配置 3 位子用户的名单将保留, 下次打开恢复"
3. 点击确认 → 保存
4. 重新打开开关 → 弹"上次已配置 3 位子用户"提示
5. 选择"保留并打开" → 弹窗中 3 个子用户预勾选 ✓
6. 直接保存 → grant 表 3 条记录恢复
```

**验证方式**：
- **后端**：`TestUpdateSopVisibility_TurnOffPreservesGrants` + `TestUpdateChatbotVisibility_TurnOffPreservesGrants`
- **E2E**：Path 3（待 fixture 解锁）— **关键路径**，spec §1.3 D3 决策必须验证
- **gstack /qa**：S5 手动验证 UI 提示文案

### 路径 4: 子用户级联清理（**DEFERRED**）

⚠️ 此路径在 spec §10.1 中列出，但实际**实施时 DEFER**（Task 13 manifest 记录）：
> "plan 假设有 DeleteSubUser 流程, 实际 grep 后代码库零此路径. spec §5 '现有删除路径' 描述错误."

后端 `store.CleanupBySubUser` API 已实现，未来加 DeleteSubUser 功能时 1 行接入即可。S5 **不验证此路径**。

### 路径 5: EC-6 实体删除清理

```
1. 父账户配置 SOP visibility, 选 2 个子用户
2. 父账户删除该 SOP
3. 数据库 sop_visibility_grant 中, 该 SOP 的 2 条记录 deleted_at != NULL (软删保留审计)
```

**验证方式**：
- **后端**：`TestDeleteTemplate_CleanupVisibility` + `TestDeleteChatbot_CleanupVisibility`
- **E2E**：N/A（DB 直接查询，超出 API E2E 范围）
- **gstack /qa**：N/A

### 路径 6: 越权防御

```
1. 父账户 X 尝试配置父账户 Y 创建的 SOP 的 visibility
   → 后端返回 403 ErrEntityNotOwnedByCaller
2. 父账户 X 提交父账户 Y 名下子用户 ID
   → 后端返回 422 ErrCrossParentSubUser
3. 子账户尝试调用 PUT visibility 端点
   → 后端返回 403 ErrVisibilityPermissionDenied
```

**验证方式**：
- **后端**：`TestUpdateSopVisibility_NonOwner` + `TestUpdateSopVisibility_SubUserCallerDenied` + `TestUpdateSopVisibility_CrossParentSubUser` + `TestValidateSubUsersBelongToCaller` (4 subtests)
- **E2E**：Path 5（待 fixture 解锁）
- **gstack /qa**：S5 手动验证 (用 curl 或浏览器 devtools 触发 403/422)

### 路径 7: visibility + run-permission 矩阵串行

完整 5 象限验证：

| 象限 | visibility | run-perm | 期望 |
|------|------------|----------|------|
| V/A | OFF | grant | 列表显示 + HasPermission=true |
| V/D | OFF | no grant | 列表显示 + HasPermission=false |
| H/A | ON 不在 | grant | 不在列表（visibility 拦） |
| H/D | ON 不在 | no grant | 不在列表（visibility 拦） |
| I/R | ON 在 | grant | 列表显示 + HasPermission=true |

**验证方式**：
- **后端**：`TestListVisibleTemplatesWithPermission_FourQuadrants` (5 SOP × 5 象限完整覆盖)
- **E2E**：Path 6（待 fixture 解锁）

### 路径 8: 父账户 bypass

```
1. 父账户登录 → 自己创建的 SOP（含 visibility=true 但无 grant）仍在自己列表中
2. HasPermission=true (父账户始终可运行自己的实体)
```

**验证方式**：
- **后端**：`TestListVisibleTemplatesWithPermission_ParentBypass` + `TestListVisibleChatbotsWithPermission_ParentBypass`
- **gstack /qa**：S5 手动

### 路径 9: 并发 PUT（last-write-wins）

```
两个 goroutine 并发 PUT visibility 不同 sub_user_ids:
- 不死锁 (5 秒内完成)
- 最终状态等于其中一次写入 (不混合)
```

**验证方式**：
- **后端**：`TestUpdateSopVisibility_ConcurrentPUT_LastWriteWins` (race-clean)
- **E2E**：N/A（API 层难复现纯并发）

### 路径 10: 幂等性

```
同一 PUT 连续 2 次:
- 第二次不触发唯一索引冲突 (Unscoped 物理删覆盖)
- 最终状态等于单次 PUT
```

**验证方式**：
- **后端**：`TestUpdateSopVisibility_IdempotentReplay` + `TestUpdateChatbotVisibility_IdempotentReplay`
- **E2E**：N/A

---

## §3 验证清单（S5 执行人 checklist）

S5 执行人需逐条勾选完成：

### §3.1 后端代码层
- [ ] `cd numind-server && task lint` 退出码 0
- [ ] `cd numind-server && task test` (含 race detection + coverage) 退出码 0
- [ ] visibility 相关测试 29 个全 PASS（grep `Visibility|Update|Is|List|Delete|Validate|ReplaceGrants|CountBy`）

### §3.2 前端代码层
- [ ] `cd numind-web-v3 && npm run lint` 退出码 0（visibility 相关文件无错）
- [ ] `cd numind-web-v3 && npm run type-check` 退出码 0
- [ ] `cd numind-web-v3 && npx playwright test --list e2e/sop-chatbot-visibility-scope.spec.ts` 列出 8 tests (7 skip + 1 sanity)
- [ ] `cd numind-web-v3 && npm run test:e2e -- sop-chatbot-visibility-scope` 跑通 sanity test (其余 skip)

### §3.3 数据库 schema
- [ ] dev 库 migration 已跑（`sop_visibility_grant` + `chatbot_visibility_grant` 两张表存在）
- [ ] `sop_template.visibility_restricted` + `chatbot_config.visibility_restricted` 两个字段已加
- [ ] 唯一索引 `idx_svg_sub_template_unique (sub_user_id, sop_template_id)` 不含 deleted_at（P0-2 关键）

### §3.4 浏览器 QA（gstack /qa）
- [ ] 父账户 SOP 模板编辑页 — VisibilityScopeCard 渲染正常（开关 + 已选 N 位 + 按钮）
- [ ] 父账户 chatbot 编辑页 — VisibilityScopeCard 渲染正常
- [ ] SubUserMultiSelectDialog 弹出 — 子用户列表加载 + 全选 + 搜索 + 手机号脱敏正常
- [ ] D3 关闭确认弹窗（"将保留 N 位"）+ 重开历史名单提示（"上次已配置 N 位"）显示正确
- [ ] 子用户工作区 — SOP/chatbot 列表按 visibility 过滤后正确隐藏
- [ ] 父账户工作区 — 自己创建的 SOP/chatbot 全部可见（bypass visibility）

### §3.5 路径手动验证（10 条路径全跑）
- [ ] 路径 1: SOP 配置 → A 可见 / B 不可见
- [ ] 路径 2: chatbot 对称
- [ ] 路径 3: D3 保留语义（开→关→重开恢复）
- [ ] 路径 4: ~~子用户级联清理~~（DEFERRED）
- [ ] 路径 5: EC-6 实体删除清理（用 DB 客户端查 grant 表 deleted_at）
- [ ] 路径 6: 越权防御（curl 或前端 dev tools 触发 403/422）
- [ ] 路径 7: 4 象限矩阵（后端单元测试已 PASS，可不重跑）
- [ ] 路径 8: 父账户 bypass
- [ ] 路径 9: 并发 PUT（后端单元测试已 PASS）
- [ ] 路径 10: 幂等性（后端单元测试已 PASS）

---

## §4 回归保护诚实声明

| 风险 | 当前保护 |
|------|---------|
| 后端 biz 修改破坏 visibility 过滤顺序 | ✅ `TestListVisibleTemplatesWithPermission_FourQuadrants` 持久化守护 |
| 唯一索引误改回含 `deleted_at` 形式 | ✅ `TestReplaceGrantsTx_PhysicalDeleteIncludesSoftDeleted` 守护 (P0-2 关键回归) |
| D3 保留语义被破坏 | ✅ `TestUpdateSopVisibility_TurnOffPreservesGrants` 守护 |
| 跨父账户越权防御被绕过 | ✅ `TestValidateSubUsersBelongToCaller` (4 subtests) + `_CrossParentSubUser` (sop) 守护 |
| 并发 PUT 死锁/数据混乱 | ✅ `TestUpdateSopVisibility_ConcurrentPUT_LastWriteWins` (race-clean) 守护 |
| 幂等 PUT 失败 | ✅ `IdempotentReplay` (sop + chatbot) 守护 |
| 前端 visibility 流程退化 | ⚠️ E2E skip 状态，仅 gstack /qa 一次性验证。**风险**：UI 改动可能破坏 D3 流程但无自动检测。**缓解**：未来建立 visibility-fixtures.ts 基础设施后解锁 E2E |
| 子用户级联清理（DeleteSubUser） | ⚠️ DEFERRED，路径不存在。**风险**：未来加 DeleteSubUser 功能时遗忘接入 cleanup。**缓解**：manifest 显式标注 + `store.CleanupBySubUser` API 已就绪 |

---

## §5 S6 部署后健康检查（dev / prod）

S6 部署后人工验证：

### dev 验证（必做）
1. 父账户登录 dev → SOP 编辑页 → 配 visibility → 保存 → 子用户登录确认列表过滤
2. 检查 dev 数据库 grant 表数据正确：`SELECT COUNT(*) FROM sop_visibility_grant WHERE deleted_at IS NULL`
3. 重新打开 visibility → 验证 D3 保留语义

### prod 验证（部署后 24h 内）
1. 监控 prod logs 中 `ErrVisibilityPermissionDenied` / `ErrCrossParentSubUser` 错误码数量（应 ≈ 0；高数量说明有客户端误调）
2. 抽样检查 1 个父账户的 visibility 配置 + 子用户列表过滤效果
3. 如发现路径 1-7 任一异常，触发回滚（rollback migration + revert feature commits）

---

## §6 回滚预案

如 S5/S6 发现 P0 级问题：

```bash
# Step 1: 应用层 revert (feature branch 未 merge)
git -C numind-server reset --hard <pre-feature-commit>
git -C numind-web-v3 reset --hard <pre-feature-commit>

# Step 2: 如 migration 已跑 (dev/prod)
cd numind-server
mysql -u<user> -p<pass> numind_<env> < migrations/20260513_120000_sop_chatbot_visibility_scope_rollback.sql

# Step 3: 验证回滚
- sop_visibility_grant + chatbot_visibility_grant 表已删
- sop_template.visibility_restricted + chatbot_config.visibility_restricted 列已删
- 子用户列表查询恢复 visibility 过滤前行为
```

---

## §7 不在本验证范围

- 销售知识库（SalesRAG）visibility — 不在 feature 范围
- Admin 端管理 visibility — 不在 feature 范围
- 子用户级联清理（路径 4） — DEFERRED，路径不存在
- 跨父账户的 SOP 分享/转让 — 产品不支持
