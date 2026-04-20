# 子账号运行权限管理（SOP + Chatbot）— 提案

## §1 方案概述 [客户可见]

客户管理页的"权限管理"窗口升级为统一的运行权限面板：

- **SOP 模板**：继续沿用现有的白名单授权，但列表显示所有已发布模板（修掉此前"只显示前 20 条"的 UI 截断）
- **智能体（chatbot）**：新增白名单授权机制，父账号可按子账号粒度开关每一个智能体的运行权限

同时把默认语义从"不授权就默认全开"翻转为"不授权就默认全关"。**存量子账号不受影响** —— 上线时一次性将他们当前的可见范围冻结为白名单，保证 0 业务中断。

上线后，父账号每新建一个 SOP 或智能体，对子账号默认是"未授权"，必须在权限面板里手动勾选才能让子账号运行。

## §2 报价与周期 [客户可见]

- 预估工作量：1.5-2 人·日
- 报价：内部项目，不计费
- 交付时间线：2026-04-22 前 dev 可验收，dev 验收无误后按用户节奏上线 prod

## §3 技术可行性 [AI 内部]

### 现有功能复用

- **SOP 权限架构**：`user_template_permission` 表 + `customerStore.HasTemplatePermission` + `customerBiz.GrantTemplates/RevokeTemplates` + 5 个既有 customer 端点（`/v1/customers/sub-users/:user_id/templates` GET/POST/DELETE + `/v1/customers/batch/{grant,revoke}-templates`）全部保留。本次仅翻转"0 记录 → deny"的默认判定。
- **Chatbot 架构对称复制**：新表 `user_chatbot_permission(sub_user_id, chatbot_id, created_at)` 结构完全对称 `user_template_permission`；新 API 路径对称 `/v1/customers/sub-users/:user_id/chatbots` GET/POST/DELETE + `/v1/customers/batch/{grant,revoke}-chatbots`；biz 层 `customerBiz.HasChatbotPermission/GrantChatbots/RevokeChatbots` 仿照 Template 三元组。
- **前端弹窗扩展**：`CustomersView.vue:540+` 的"管理模板权限"弹窗新增 chatbot 区块，复用现有 perm-item / checkbox-mark / perm-toggle-all 样式和交互，API 层 `src/api/customers.ts` 追加 `fetchAllChatbots/fetchUserChatbots/grantChatbots/revokeChatbots/batchGrantChatbots/batchRevokeChatbots` 六个函数。
- **权限判定接入点**：
  - SOP 运行点已有 `Customers().HasTemplatePermission(ctx, userID, templateID)` 在 `biz/sop/sop.go:304, 421, 1290`，零新增接入点
  - Chatbot 运行点：`biz/chatbot/chatbot.go` 的 `CreateSession`（新开会话）+ `ListVisibleChatbots`（首页列表过滤）需要接入白名单检查
- **管理端不涉及**：管理员面板 `admin_router.go` 不需要改动；这是 B2B2C 父对子的治理能力，不是超管能力。

### 技术风险

1. **默认语义翻转导致存量账号误屏蔽**（P0）
   - 缓解：上线流程强约束"先跑 backfill migration、验证生效、再 merge 代码"。Migration 针对 `SELECT sub_user_id FROM user WHERE parent_user_id IS NOT NULL AND id NOT IN (SELECT DISTINCT sub_user_id FROM user_template_permission)` 这批"0 记录"子账号批量 `INSERT IGNORE` 对应的权限行。
   - 回滚：若上线后发现异常，rollback 脚本 `DELETE FROM user_template_permission WHERE created_at >= '<migration_timestamp>' AND <backfill_tag>` —— backfill 行需要加标记字段（或用 `created_at` 精确匹配）以便区分人工授权和 backfill 授权。

2. **Chatbot 权限接入点遗漏**（P1）
   - 风险：只在 `ListVisibleChatbots` 加过滤，忘了 `CreateSession` / `Chat` 直连，子账号可通过猜 ID 绕过前端直调后端。
   - 缓解：S3 Plan 枚举所有需要 `HasChatbotPermission` 守卫的入口；S4 review 强制 diff 对比 SOP 三个接入点（304 / 421 / 1290），确保 Chatbot 对称覆盖。
   - 进一步：review 时检查 `/v1/chatbot/list` + `/v1/chatbot/sessions` + `/v1/chatbot/sessions/:id/chat` + `/v1/chatbot/sessions/:id/messages` 全部入口。

3. **并发授权导致数据不一致**（P2）
   - 场景：父账号在两个浏览器 tab 同时授权 / 撤销同一组权限。
   - 缓解：沿用 SOP 现有策略 —— `INSERT IGNORE` 保证授权幂等，`DELETE` 按 `(sub_user_id, chatbot_id)` 精确匹配保证撤销幂等；前端弹窗保存时对比基线 + delta，而非全量覆盖。

4. **SOP 侧 feature A 合入顺序**（P2）
   - feature A (`sop-perm-dialog-show-all`) 今日先行 merge 到 develop。B 的前端 feature 分支从 A merge 后创建，自然带上。S3 Plan 要显式记录这个前置依赖。

### 涉及仓库

- [x] numind-server（后端：新表 + migration + biz/store/controller/router + backfill SQL）
- [x] numind-web-v3（前端：api/customers.ts + views/CustomersView.vue）
- [ ] numind-admin-web

### AI 可观测性

- [x] 涉及 LLM 调用：**否**
- N/A

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事

1. 作为**父账号**，我需要在客户管理页面打开每个子账号的权限弹窗，看到所有已发布的 SOP 和 chatbot 列表，通过勾选授权或取消授权某一项给指定子账号，以便我能按团队分工管理资源访问。
2. 作为**子账号**，我登录工作区首页后，只能看到被父账号授权的 SOP 和 chatbot；没授权的资源对我完全不可见、不可运行。
3. 作为**新建的子账号**，我注册后默认看不到任何 SOP 和 chatbot —— 父账号必须显式授权我才能开始工作。
4. 作为**存量子账号**（上线前就存在的），我登录后看到的 SOP 和 chatbot 列表**和上线前完全一致**，不会有任何资源突然消失。

### 验收标准

- [ ] **AS-1 存量保护**：prod 上线 backfill migration 后，所有存量子账号调 `/v1/sop/templates` 返回的模板数等于上线前。抽样 5+ 子账号对比 before/after，零差异。
- [ ] **AS-2 默认 deny（SOP）**：父账号新建子账号 → 用该子账号登录 → 工作区 SOP 列表为空。父账号在权限面板勾选 1 个 SOP → 子账号重新登录 → 工作区能看到且能运行该 SOP。
- [ ] **AS-3 默认 deny（chatbot）**：同 AS-2，但资源改为 chatbot。父账号发布新 chatbot 后，子账号默认看不到；授权后子账号能看到、能创建 session、能 Chat。
- [ ] **AS-4 权限面板显示全量**：父账号打开任何子账号的权限面板 → SOP 区块和 chatbot 区块各自显示父账号当前**全部已发布**的对应资源，不被 pagination 截断（feature A 已覆盖 SOP 侧）。
- [ ] **AS-5 撤销即时生效**：父账号在面板里取消勾选某个 chatbot → 子账号尝试调 `/v1/chatbot/list` 时该 chatbot 消失；尝试 POST `/sessions` 开新会话 → 返回 403/权限 error。
- [ ] **AS-6 直连 API 防绕过**：子账号绕过前端，直接调 `/v1/chatbot/sessions`（指定一个未授权的 chatbot_id）→ 返回 403。对已存在但失去权限的 session 发消息 → 返回 403。
- [ ] **AS-7 批量操作**：父账号在面板批量勾选 3 个 chatbot + 取消勾选 2 个 SOP → 一次提交，服务端幂等生效。
- [ ] **AS-8 父账号不受限**：父账号自己调 `/v1/sop/templates` 和 `/v1/chatbot/list` 仍然看到自己创建的全部已发布资源，不受白名单影响。
- [ ] **AS-9 Rollback 可行**：生产 migration 上线后 1 小时内，执行 rollback SQL 能够精确撤销 backfill 行（不误删人工授权）。

### 边界情况

1. **Sub-user 在 0 记录状态下并发被 backfill 和手动授权**：上线窗口内，若父账号在 migration 跑完前先手动给某子账号授权 1 个模板 → migration 跑到该子账号时 `INSERT IGNORE` 跳过重复 → 最终权限 = migration 覆盖的全集 ∪ 人工授权（没 bug，幂等）。
2. **Chatbot / SOP 删除软删**：若 `chatbot_config.deleted_at IS NOT NULL` 或 `sop_template.deleted_at IS NOT NULL`，权限行保留还是清理？**决策：保留**（权限行 FK 不强绑 chatbot_config），只在查询时 `JOIN WHERE deleted_at IS NULL` 过滤 —— 简化迁移负担，对子账号无功能影响。
3. **父账号变更子账号归属**（当前无此功能，理论情况）：`user_template_permission.sub_user_id` 的引用性由业务层保证；若未来有"转移子账号"能力，需同步清理或迁移权限行。不在本 feature 范围。
4. **两个父账号拥有同 ID 的子账号**：不可能 —— `user_id` 是全局唯一主键，`parent_user_id` 是多对一。
5. **Chatbot `draft` 状态的权限**：父账号可以预先给子账号授权一个 draft chatbot。Chatbot 一旦 publish，子账号立即可见、可运行。**决策**：面板只列已发布 + draft 两种状态（便于提前授权），但 **draft 状态的 chatbot 对子账号运行端照旧不可见**（由 `chatbot.status='published'` 的 SQL 条件把关）。面板列表里 draft 用淡色 + 标签"未发布"提示。

### 权限规则

- **父账号**（`parent_user_id IS NULL`）：
  - 可调用本 feature 的全部管理 API
  - 权限判定 bypass（`HasTemplatePermission` / `HasChatbotPermission` 对父账号返回 true）
  - 父账号间相互不可见（现有 `ListSubUsers` WHERE `parent_user_id = :caller_id` 已保证）
- **子账号**（`parent_user_id IS NOT NULL`）：
  - 不能调用任何 `/v1/customers/*` 管理 API（中间件 `ParentUserOnly` 或路由组已经守卫 —— 需 S2 Spec 核实现状）
  - 运行点（SOP run / Chatbot session + chat）调用时，先走 `Has*Permission` 白名单检查
  - 列表点（`/v1/sop/templates` + `/v1/chatbot/list`）返回的列表经白名单过滤

### UI 行为规格

- **页面位置**：`numind-web-v3/src/views/CustomersView.vue` 既有"管理模板权限"弹窗（行 540+）
- **布局要求**：弹窗从上到下为
  1. 用户信息区（既有）
  2. 功能权限区（既有，保留"销售智能体"硬编码 checkbox）
  3. **SOP 模板区**（既有，feature A 已修 full list 显示）
  4. **Chatbot 智能体区**（**新增**）—— 标题"可用智能体"+ badge 显示总数 + "全选/取消全选"按钮；列表每项：checkbox + 名称 + 副标题（description 截 60 字符）+ 可选"draft"tag
- **交互模式**：
  - 点击某项切换勾选状态（local state，不立即提交）
  - 底部"保存"按钮一次性 diff 提交 SOP 和 chatbot 两类 delta（grantList / revokeList）
  - 保存过程按钮 disabled + spinner
- **状态处理**：
  - **loading**：整个弹窗 `perm-loading` spinner（既有）
  - **empty**：若父账号无已发布 chatbot，chatbot 区块显示"您还没有发布智能体"提示 + 跳转 `/config/chatbots` 链接
  - **error**：保存失败 → 弹窗内 toast "部分权限保存失败，请重试"，已成功的一半保持不变
- **响应式**：弹窗按既有 .perm-dialog 样式，宽度自适应，列表区最大高度 70vh 超出滚动

### 不做范围（明确排除）

- 父账号之间的协作 / 共享 chatbot —— 不在本 feature 范围
- 子账号之间的互相授权 / 借用权限 —— 不在本 feature 范围
- 按"角色"批量授权模板（例如给子账号设角色"销售"→ 自动拥有销售相关 SOP）—— 不在本 feature 范围
- 权限审计日志（谁何时给谁授予/撤销了什么）—— 不在本 feature 范围，如需要另立 hotfix
- 管理端 admin 视角的子账号权限查看 / 重置 —— 不在本 feature 范围
