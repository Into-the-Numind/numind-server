# SOP / 销售智能体 父账户归属隔离 — 提案

## §1 方案概述 [客户可见]

**修什么**：当前平台上有 4 个实体（3 个 SOP + 1 个销售智能体）在 prod 上跨父账户全局可见——任何登录的父账户都能看到它们。其中销售智能体对所有父账户开放（包括未来入驻的新机构）；3 个 SOP 中有 2 个甚至没有归属字段，剩下 1 个虽归属正确但因为 SOP 列表 SQL 缺少父账户过滤也照样泄露。本次修复让这 4 个实体严格归属到莫小派（user 30）名下，其他父账户及其子账户看不到。

**为什么修**：当前 prod 只有 1 个真实客户（莫小派），所以这个数据隔离 bug 没暴露。但第 2 个机构入驻立刻会看到莫小派的内容——这是高危的数据泄露 bug，必须在第 2 个机构入驻前修。

**怎么修**（用户感知层面）：
- 莫小派父账户 user 30 和它的全部子账户体验完全不变：还能正常看到 3 个 SOP + 销售智能体
- admin 系统账户（demo 用，非真实客户）登录后看到空工作区——这是预期行为，不需补偿
- 未来新入驻的机构父账户：默认只看到自己创建的内容，看不到莫小派的任何东西

**架构理念**：建立**统一多租户隔离规则**——不管是 SOP 还是 chatbot（含销售智能体这种特殊 chatbot），谁创建/拥有它，只有他和他的子账户看得到；其他租户及其子账户看不到。

**范围明确不含**：admin 管理界面 / 系统级功能（content_monitor、self_service_config）/ 用户端 UI 重设计 / "自定义智能体类别" 升级路径（Option B 留口，本次不实现，详见 §3）。

## §2 报价与周期 [客户可见]

- 预估工作量：**1 个工作日**（最小可行范围 Option A，对比未实现的 Option B 工作量 3-4 天）
- 报价：内部需求，无外部报价
- 交付时间线：**2026-05-19 ~ 2026-05-20**（S2 spec + S3 plan + S4 编码连续推进，S5-S7 视 dev 验证情况）

## §3 技术可行性 [AI 内部]

### 现有功能复用

| 复用对象 | 复用方式 |
|---------|---------|
| `chatbot_config.user_id` + `ListPublishedByOwner` 的 multi-tenant SQL 过滤模式 | 直接照抄到 SOP 列表查询，沿用同套 owner-by-parent_user_id 语义 |
| `user.parent_user_id` 字段 + 子账户层级关系 | 解析当前用户所属父账户 ID 的现成机制 |
| `user_feature_permission` 表 + 48 行子账户 sales_agent 授权数据 | 保留作为 Layer 1 子用户授权层，本需求只在其上叠加 Layer 0 父账户 owner 检查 |
| `sop_template.creator_user_id` 字段 | 已有字段，只补 list SQL 过滤即可，2 行历史数据 UPDATE |
| `customer.HasFeaturePermission` 函数现有接口 | 仅修改 sales_agent 分支内部逻辑，其他 feature 走原 bypass，调用方零变更 |

### 技术风险

| 风险 | 缓解方案 |
|------|---------|
| SOP 列表 SQL 需要按"租户"过滤而当前 `creator_user_id` 语义是 actor 不是 owner | **S2 spec D1 决议**：把 `creator_user_id` 语义升级为"始终存父账户 id"，与 `chatbot_config.user_id` 模式对齐。两个写入路径都改：user 路径 biz 层加 assertion + 写 parent.id；admin 路径 signature 加 adminUserID + 写 admin.id。列表 SQL 用单值 `WHERE creator_user_id = parentID`，无子查询 |
| Admin SOP 创建路径继续制造 NULL `creator_user_id` 行 | **S2 spec D6**：本次同步修 `sopBiz.CreateTemplate(ctx, adminUserID, ...)`，要求强制传入非零 adminUserID。否则本次修复完毕，下一次 admin 创建 SOP 立刻又产生 NULL 行 |
| `HasFeaturePermission` 改造可能误伤其他 feature_key | 严格限定 `featureKey == "sales_agent"` 分支内修改，content_monitor / self_service_config / 未来 feature 走原 bypass。S4 review 必查的回归点。**S2 spec §6.2** 强制 biz 层单测包含 ContentMonitor_ParentBypass + SelfServiceConfig_ParentBypass 两个回归测试 |
| 既有 middleware → store 直调跳过 biz 的 layer violation | **S2 spec D2**：dispatch 逻辑上移到 biz 层 `CheckFeaturePermission`；store 层只保留 `CheckSubUserFeatureGrant` + `SalesAgentOwners.Exists` 纯查询；middleware 改调 biz |
| 移除父账户硬 bypass 后 admin 自己作为父账户访问销售智能体被拒 | 这是**预期行为**，admin 是 demo 不是真客户。明确写入 PRD §4 验收标准 |
| migration 部分失败导致中间态（MySQL DDL 不是事务的）| §7.1 设计为顺序幂等操作：CREATE TABLE IF NOT EXISTS + INSERT IGNORE + UPDATE（重复无副作用）。CI 不跑 migration，prod 部署手动 SSH 执行（参见 memory dev_deploy_migration_gap）|
| 父账户被 hard-delete 后 sales_agent_owner 残留孤儿行 | **S2 spec D3**：加 FK 约束 `ON DELETE CASCADE`。INT UNSIGNED 类型与 user.id 对齐，避免 JOIN 索引退化 |
| `creator_user_id IS NULL` 历史数据成为 Layer 0 过滤盲区 | SQL 加防御性 `AND creator_user_id IS NOT NULL`（4 行历史数据迁移后理论上应该无 NULL 行，防御性兜底防意外）|
| 子用户被 reparent 后他创建的 SOP 行为 | D1 设计副作用：creator_user_id = 旧父账户 → SOP 留在旧父账户租户内。**正确行为**（SOP 不"飘"到新父账户）|

### 涉及仓库

- [x] **numind-server**（主战场，~9 文件改动：sop_template store / biz、customer store / biz、sales_agent_owner 新 model + store、migration SQL、middleware、admin controller、测试）
- [ ] ~~numind-web-v3~~（前端无感，后端透明过滤）
- [ ] ~~numind-admin-web~~（无 admin UI，本需求范围明确不含）

### AI 可观测性

- 涉及 LLM 调用：**否**
- 本需求纯多租户数据隔离修复，零 LLM 调用变更
- N/A

### Option B（自定义智能体通用类别）未来升级路径

本次选 Option A（sales_agent_owner 小表）。如果未来 3-6 个月内出现第 2-3 个"自定义智能体卡片"（如 AI 营销助手、AI 客服助手），升级到 Option B 的迁移路径：

1. 新建 `custom_agent(id, user_id, kind, name, description, status, ...)` 表
2. 把 sales_agent_owner 的现有行迁移过来：`INSERT INTO custom_agent (user_id, kind, name, ...) SELECT parent_user_id, 'sales_agent', '销售智能体', ... FROM sales_agent_owner`
3. 调整 `HasFeaturePermission` 的 sales_agent 分支改查 custom_agent 表
4. 前端 HomeView 加新分类区块或调整现有 chatbot 区块渲染
5. 删除 sales_agent_owner 表

升级是局部 ALTER + 数据迁移，不破坏已运行用户体验。这是选 Option A 的关键论据。

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事

1. 作为**莫小派父账户 user 30**，我需要确保我的 SOP / chatbot / 销售智能体只对我自己和我的子账户可见，未来其他机构入驻平台后看不到我的数据
2. 作为**未来入驻的新机构父账户**，我需要默认只看到我自己机构创建的资源，看不到 user 30 或其他机构的资源
3. 作为**莫小派的任一子账户**（如运营、销售等），我需要修复前后我能访问的资源集合完全一致——能看见的 SOP/chatbot/销售智能体不增不减
4. 作为**平台 admin 账户**（demo 性质），我接受登录后看到空工作区（admin 的 chatbot id=8 是 demo 数据，admin 自创 SOP 0 条），admin 不需销售智能体磁贴
5. 作为**平台维护者**（你），我需要 sales_agent 的多租户隔离逻辑改造**不影响** content_monitor 和 self_service_config 这两个系统级 feature 的现有行为

### 验收标准

#### 数据层面（迁移正确性）

- [ ] migration 执行后 `sop_template.id=1` 和 `sop_template.id=2` 的 `creator_user_id` 都等于 30
- [ ] migration 执行后 `sales_agent_owner` 表存在且仅 1 行：`parent_user_id=30`
- [ ] migration 是 transaction，部分失败时全回滚（注：MySQL DDL 不严格事务，本需求用顺序幂等操作保证近事务行为）
- [ ] migration 是幂等的：dev → qa → prod 多环境重复执行不报错也不重复插入
- [ ] `user_feature_permission` 表 48 行 sales_agent 数据零变更

#### 行为层面（修复前后回归对比）

- [ ] user 30 登录 prod，SOP 列表返回 3 行：id=1, 2, 4（修复前同款，has_permission 字段值不变）
- [ ] user 30 登录 prod，agentCards 列表包含 8 个 chatbot（user_id=30 published）+ 1 个销售智能体磁贴（hasSalesPermission=true）
- [ ] user 30 的子账户（任选 sub_user_id=345 验证）登录 prod，看到的 SOP / chatbot / 销售智能体集合修复前后**完全一致**
- [ ] admin（id=1）登录 prod，SOP 列表返回 0 行
- [ ] admin 登录 prod，agentCards 不含销售智能体磁贴（hasSalesPermission=false）
- [ ] admin 登录 prod，访问 `/v1/monitor`（content_monitor 路径）行为修复前后一致（仍能访问，父账户 bypass 保留）
- [ ] admin 登录 prod，访问 `/v1/config/*`（self_service_config 路径）行为修复前后一致

#### 代码层面

- [ ] `biz.Customers().CheckFeaturePermission(ctx, userID, featureKey)` 仅在 `featureKey == "sales_agent"` 分支改逻辑，其他分支零变更
- [ ] sales_agent 分支：父账户走 sales_agent_owner 查询；子账户走"父账户 owner ✓ AND user_feature_permission 行存在 ✓"双层 AND
- [ ] SOP 列表 store 函数 `ListVisibleTemplates` 加 `ownerParentUserID uint` 参数 + `ctx context.Context` 参数，biz 层 `ListVisibleTemplatesWithPermission` 传入当前用户所属父账户 ID
- [ ] migration SQL 文件命名符合 `migrations/YYYYMMDD_HHMMSS_description.sql` 约定
- [ ] 新增 model `SalesAgentOwner` 在 `internal/pkg/model/`，TableName() 返回 `sales_agent_owner`
- [ ] 新增 store interface `ISalesAgentOwnerStore` 至少含 `Exists(ctx, parentUserID) (bool, error)` 方法
- [ ] `middleware.FeaturePermission` 改调 `biz.B.Customers().CheckFeaturePermission`（或等效注入方式），不再直调 store
- [ ] `sopBiz.CreateTemplateByUser` 入口 assert `user.ParentUserID == nil`，否则 return ErrForbidden
- [ ] `sopBiz.CreateTemplate` (admin) signature 改为接受 `adminUserID uint`，强制非零

### 边界情况

| 场景 | 预期行为 |
|------|---------|
| 子账户的 `parent_user_id` 指向已删除（软删）的用户 | Layer 0 owner 查询返回 false → deny |
| 子账户的 `parent_user_id` 字段为 NULL（数据异常，子账户本不应这样） | Layer 0 owner 查询返回 false → deny（安全） |
| 父账户登录但其 user.id 在 sales_agent_owner 表里被软删 / 没有行 | deny（修复后 admin 的预期路径） |
| SOP 列表查询时父账户名下零子账户（如初创机构刚入驻没建子账户） | 单值 `WHERE creator_user_id = parent_id` 仍能匹配父账户自创的 SOP |
| `creator_user_id IS NULL` 的历史 SOP 行（理论上迁移后零行） | 防御性 `creator_user_id IS NOT NULL` 过滤，永不返回 NULL 行 |
| migration 在 dev 已跑过，qa 再跑 | INSERT 用 INSERT IGNORE 保证幂等；CREATE TABLE 用 IF NOT EXISTS；UPDATE 是幂等的（重复执行无副作用） |
| 并发：父账户被加入 sales_agent_owner 的同时子账户访问 | 最终一致，无锁需求（管理路径手动 SQL，不存在 race） |
| 用户端 SOP 列表的分页 + 过滤组合 | SQL 改写后 LIMIT 500 行为不变，分页参数透传 |

### 权限规则

| 谁 | 看 SOP 列表 | 看销售智能体磁贴 | 访问 content_monitor | 访问 self_service_config |
|----|------------|-----------------|---------------------|------------------------|
| user 30（父账户） | `id IN (1, 2, 4)` 共 3 行 | ✓ | ✓（父 bypass，本需求不动）| ✓（父 bypass，本需求不动）|
| user 30 的子账户 | 3 行中过滤 visibility + 标记 has_permission | 需子账户 user_feature_permission 有行 | 子账户 user_feature_permission 查（无行→ deny）| 同左 |
| admin（id=1，父账户） | 0 行 | ✗ | ✓（父 bypass 保留）| ✓（父 bypass 保留）|
| admin 的子账户（若有） | 0 行 | ✗ | 子账户 user_feature_permission 查 | 同左 |
| 未来新机构父账户 | 自创 SOP（默认 0 行） | ✗（未在 sales_agent_owner）| ✓（父 bypass 保留）| ✓（父 bypass 保留）|

### UI 行为规格

- **页面位置**：N/A（无 UI 改动）
- **布局要求**：N/A
- **交互模式**：N/A
- **状态处理**：N/A
- 后端透明修复，前端 HomeView.vue 调用的 `/v1/sop/templates` 返回结果集合变化（其他父账户视角看到的列表变短），`/v1/sales-rag/check-permission` 返回值变化（其他父账户视角从 true 变 false），但前端代码零修改

---

## 附：S2 spec 需要锁定的设计细节（已在 spec D1-D9 完成）

1. ✓ SOP owner 过滤 SQL：单值 `WHERE creator_user_id = ?` 模式（D1）
2. ✓ `SalesAgentOwnerStore.Exists` 接口 signature 和返回模式（D3）
3. ✓ sales_agent_owner 表极简（INT UNSIGNED + 无软删 + FK CASCADE）（D3）
4. ✓ HasFeaturePermission sales_agent 分支代码结构（dispatch + 抽 helper，上移 biz 层修 layer violation）（D2）
5. ✓ 测试矩阵：5×4 角色 × 资源 + 8 边界（spec §5, §6）
