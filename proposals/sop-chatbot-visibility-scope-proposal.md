# SOP / 智能体可见范围权限 — 提案 + PRD

## §1 方案概述 [客户可见]

父账户在 SOP 模板或智能体（chatbot）的编辑页内联新增「可见范围」配置区块：

- **默认状态**：开关关闭，所有子账户都能在工作区看到这个 SOP / chatbot（与今天的行为完全一致，老用户老 SOP 不变）
- **限定可见**：父账户打开「仅指定子用户可见」开关 → 弹窗列出自己名下全部子账户 → 勾选要展示给谁
- **未勾选的子用户在工作区列表里看不到该 SOP / chatbot**（仿佛不存在）
- **后期新增的子用户默认看不到**已限定的 SOP / chatbot（白名单语义），父账户需手动补勾
- **已开始的运行和历史会话不受影响**（仅过滤列表展示）

与已有的「子账户运行权限」（客户管理 → 子账号弹窗）共存：
- 可见范围 = 列表过滤层（先决定能不能看到入口）
- 运行权限 = 执行许可层（再决定看到的能不能跑）

预期效果：父账户在创建/编辑一份新 SOP 时，自然地决定"这个东西发给谁"，不必再切换到客户管理逐个子账号配置。**实体视角**比**用户视角**在分发场景下效率高一个数量级。

---

## §2 报价与周期 [客户可见]

- 预估工作量：**3.5 工作日**
  - 后端：1.5 天（1 migration + 2 model + 2 store + 2 biz + 2 controller + 单元测试）
  - 前端：1.5 天（SOP edit 页 / chatbot edit 页 UI 块 + 子用户选择器组件 + api 层 + 列表查询过滤接入 + 工作区列表 e2e 调整）
  - S5 验证 + 联调：0.5 天
- 报价：N/A（自用项目）
- 交付时间线：建议 2026-05-13 启动 S2，2026-05-20 完成 S5 验证

---

## §3 技术可行性 [AI 内部]

### 现有功能复用

| 复用项 | 现状 | 本功能如何利用 |
|--------|------|--------------|
| `user_template_permission` / `user_chatbot_permission` 表结构模式 | 已上线 child-run-permission | 新表 `sop_visibility_grant` / `chatbot_visibility_grant` **完全复制其字段+索引+GORM 模式**（gorm.Model + ParentUserID + SubUserID + EntityID + uniqueIndex + 外键关联） |
| `customer.go:HasTemplatePermission` / `HasChatbotPermission` 函数模式 | 已上线 | 新增 `IsSopVisibleToUser(ctx, userID, sopID)` / `IsChatbotVisibleToUser(...)` 函数对称实现 |
| 列表查询「全量查 published + 本地 O(N) 过滤」模式 | `Biz.ListVisibleTemplatesWithPermission` / `ListVisibleChatbotsWithPermission` | 在现有过滤管线**前置**新一层 visibility 过滤（先 visibility，再 run-permission） |
| `GET /v1/customers/sub-users` 子用户列表 API | router.go:233 已存在 | 前端可见范围弹窗直接调用，返回字段 id+nickname+phone 已足够支持勾选 UI |
| 客户管理弹窗子用户选择器组件 | `CustomersView.vue` 已有 | 提取/复制为可复用的子用户多选组件 |

### 技术风险

| # | 风险 | 缓解方案 |
|---|------|--------|
| R1 | 两层 gate 串行（visibility 在 run-permission 之前）的语义边界混淆 | S2 spec 明确锁定优先级：visibility 决定列表展示 → run-permission 决定能否执行；S5 验证策略必须含矩阵测试（visible+allowed / visible+denied / hidden+allowed / hidden+denied 四象限） |
| R2 | 子用户级联删除的 transaction 边界（删除子用户时同步清理两张 visibility 表 + 两张 run-permission 表共 4 张表的记录） | S2 spec 锁定级联清理路径走「软删除时同事务清理」；store 层加 `CleanupVisibilityGrantsBySubUser(ctx, tx, subUserID)`；幂等设计支持重试 |
| R3 | 默认 allow-all 语义下的列表查询性能（开关关闭的 SOP 不需要查 visibility 表，但需要先看 `sop_template.visibility_restricted` 字段判断） | 在 sop_template / chatbot_config 加 `visibility_restricted` boolean 字段做短路判断；只在 restricted=true 的实体上才查 grant 表；预期对查询性能影响 <5% |
| R4 | 开关关闭后再打开，恢复名单的预期（用户已选"保留名单"）但 UI 需要明确表达"上次配置的名单仍生效" | 前端在打开开关时若 grant 表已有数据，提示「上次已配置 N 位子用户，是否保留？」+ "继续编辑" / "清空重选" 二选一 |
| R5 | GORM `default:true` bool gotcha（database.md §6 已记录） | `visibility_restricted` 字段不能用 `default:true`（默认 false=allow-all），所以本风险**不适用**；但记得 `default:false` 是 GORM 默认行为，不需要显式 tag |
| R6 | 普通无子账户用户（parent_user_id IS NULL 且无子账户）的 UI 处理 | 前端通过 `useUserStore().hasSubUsers`（基于 GET /v1/customers/sub-users 总数）判断，开关区块整体隐藏；后端不做特殊判断（即使误调用，无子账户 = 0 名单 = 行为等同 allow-all） |

### 涉及仓库
- [x] numind-server
- [x] numind-web-v3
- [ ] numind-admin-web（不涉及）

### AI 可观测性（Langfuse）

- [ ] 涉及 LLM 调用：**否**
- N/A — 本功能为权限管理纯 CRUD，无 AI 调用环节

---

## §4 产品需求定义 — PRD [AI 内部]

### 4.1 用户故事

- **US-1**：作为父账户，我需要在 SOP 模板编辑页直接配置"这个 SOP 给哪些子用户看"，以便快速完成分发，不必切换页面。
- **US-2**：作为父账户，我需要在智能体编辑页直接配置"这个智能体给哪些子用户看"，与 SOP 体验一致。
- **US-3**：作为子用户，我在工作区只看到父账户允许我看到的 SOP / 智能体，看不到的就当不存在（不出现"无权限"提示，避免泄露信息）。
- **US-4**：作为父账户，关闭"仅指定子用户可见"开关时，我之前配置的名单应被保留，下次打开恢复，避免误操作丢失数据。
- **US-5**：作为父账户，删除/移除一个子用户时，该子用户在所有 SOP / chatbot 的可见名单中自动清除，无残留数据。

### 4.2 验收标准

#### AC 后端

- **AC-1**：新增表 `sop_visibility_grant`（parent_user_id, sub_user_id, sop_template_id, gorm.Model）+ `chatbot_visibility_grant`（同形）；均含 `uniqueIndex(sub_user_id, entity_id)`
- **AC-2**：`sop_template` 表新增 `visibility_restricted boolean NOT NULL DEFAULT false`；`chatbot_config` 表同
- **AC-3**：新增 API `PUT /v1/sop/templates/:id/visibility`，请求体 `{ "restricted": bool, "sub_user_ids": [int] }`，响应 `{ "code": 0, "data": null }`
- **AC-4**：新增 API `PUT /v1/chatbot/:id/visibility`，请求/响应同形
- **AC-5**：新增 API `GET /v1/sop/templates/:id/visibility`，响应 `{ "restricted": bool, "sub_user_ids": [int] }`（用于编辑页加载现有配置）
- **AC-6**：新增 API `GET /v1/chatbot/:id/visibility`，响应同形
- **AC-7**：列表查询 `GET /v1/sop/templates`（子用户身份）在现有 `ListVisibleTemplatesWithPermission` 之前加 visibility 过滤层；对 `visibility_restricted=true` 的 SOP 校验子用户在白名单中
- **AC-8**：列表查询 `GET /v1/chatbot/list`（子用户身份）同形
- **AC-9**：父账户身份查询自己的 SOP / chatbot 列表**不过滤** visibility（父账户永远看到自己创建的全部）
- **AC-10**：删除子用户（`DELETE /v1/customers/sub-users/:user_id`）在同一事务中调用 `CleanupVisibilityGrantsBySubUser`，清理两张 visibility 表 + 已有的两张 run-permission 表（共 4 张表）记录
- **AC-11**：权限校验：只有 SOP / chatbot 的创建者（`creator_user_id` / `user_id`）父账户能调用 visibility 配置端点，其他人 403
- **AC-12**：单元测试覆盖：visibility 关闭、visibility 开启全选、visibility 开启部分选、visibility 开启零选、子用户级联清理、跨父账户越权配置 6 个场景

#### AC 前端

- **AC-13**：SOP 模板编辑页（`SopTemplateEdit.vue`）新增「可见范围」卡片，含开关 + 「选择子用户」按钮（开关打开时显示）+ 已选数量徽章
- **AC-14**：智能体编辑页（chatbot edit view 文件路径 S2 确认）同形新增卡片
- **AC-15**：点击「选择子用户」打开 ModalDialog，列出当前父账户名下全部子用户（昵称 + 手机号），可勾选，含全选/全反选按钮
- **AC-16**：弹窗 Confirm 后保存到本地 store，编辑页保存按钮触发 PUT API
- **AC-17**：编辑页加载时调用 GET visibility，回显现有配置
- **AC-18**：开关从开到关时弹出确认：「已配置 N 位子用户的名单将保留，下次打开恢复。仍要关闭吗？」；取消保留名单（仅切换 visibility_restricted=false）
- **AC-19**：开关从关到开时若有历史名单：弹窗内默认勾选历史名单，顶部提示「上次已配置 N 位子用户」+ 「保留」/「清空重选」按钮
- **AC-20**：普通无子账户的独立用户 UI 隐藏整个「可见范围」卡片（基于 `useUserStore().hasSubUsers` 判断）
- **AC-21**：工作区列表 `HomeView.vue` / SOP 列表 / chatbot 列表的可见性过滤由后端 API 强制，前端不做客户端过滤
- **AC-22**：4 状态处理（loading skeleton / empty 含 CTA「去客户管理添加子用户」/ error 含 retry / success）

### 4.3 边界情况

- **EC-1**：父账户先选了 3 个子用户，然后删除其中 1 个子用户，再回到 SOP 编辑页 → 弹窗中只显示剩余 2 个子用户（级联删除已生效），不应残留已删除子用户的 ID
- **EC-2**：父账户在 A 设备打开「可见范围」编辑同一 SOP 同时 B 设备也在编辑（并发） → 后保存覆盖先保存（last-write-wins，类似现有 SOP edit）；不引入乐观锁；S2 spec 锁定
- **EC-3**：visibility_restricted=true 但 grant 表中 0 条记录 → 子用户全部不可见（白名单严格语义，与现有 user_template_permission 一致）
- **EC-4**：父账户用 `PUT visibility` 提交一个不属于自己的子用户 ID（恶意请求） → 后端在保存前校验所有 sub_user_ids 的 parent_user_id 必须等于当前 caller，否则 422 ErrCrossParentSubUser
- **EC-5**：子用户已经开始一个 SOP 运行（拿到了 run_id），父账户随后将其移出可见名单 → 该 run 仍可继续运行（已在 run-permission gate 通过后），列表中 SOP 入口不再展示；运行历史页能进入查看（已开始的不撤回，per S0 决策）
- **EC-6**：父账户删除整个 SOP / chatbot → grant 表中相关记录由 GORM 软删除级联（外键 ON DELETE CASCADE 不适用因软删，由 biz 层显式清理）
- **EC-7**：开关切换不保存就关闭页面 → 不持久化（保存按钮触发 PUT，未点保存退出页面则丢弃修改，与现有编辑页行为一致）

### 4.4 权限规则

- **Caller = 父账户**（`parent_user_id IS NULL`）：
  - 能配置自己创建的 SOP / chatbot 的 visibility（`creator_user_id` 等于自己）
  - 能配置范围 = 自己名下子账户（不能选别人的子账户，EC-4 强制）
  - 自己的工作区列表**不**应用 visibility 过滤
- **Caller = 子账户**（`parent_user_id != NULL`）：
  - **不能**调用 visibility 配置端点（403 ErrPermissionDenied）
  - 工作区列表查询应用 visibility 过滤（被限制 + 未在白名单 → 不可见）
- **Caller = admin**：本 feature 不涉及 admin 端
- **错误码**：
  - `ErrCrossParentSubUser`：父账户提交了不属于自己的子用户 ID
  - `ErrEntityNotOwnedByCaller`：父账户尝试配置不属于自己的 SOP / chatbot
  - `ErrPermissionDenied`：子用户尝试调用配置端点

### 4.5 UI 行为规格

- **页面位置**：
  - SOP 模板编辑页（`numind-web-v3/src/views/sop/SopTemplateEdit.vue` 或类似路径，S2 spec 确认）
  - 智能体编辑页（`numind-web-v3/src/views/chatbot/` 下的编辑视图，S2 spec 确认具体文件名）
- **布局**：
  - 「可见范围」卡片放在编辑页主表单**底部**（保存按钮上方），与近期 sop-edit-save-button-bottom 形成的"配置→保存"阅读流一致
  - 卡片包含：标题「可见范围」+ 开关「仅指定子用户可见」+ （开关打开时）「选择子用户」按钮 + 「已选 N 位」徽章
- **交互**：
  - 开关切换：从关到开时立即调出选择弹窗（不允许"开了但没选"）；从开到关弹出确认
  - 选择弹窗：勾选/全选/全反选 + 搜索框（如果子用户数 > 10）+ 确认/取消
  - 保存：与编辑页主保存按钮一体，单次 PUT 提交所有改动
- **状态处理**：
  - loading：卡片 skeleton（仅在 GET visibility 加载时）
  - empty（父账户名下 0 子用户）：开关置灰 + 提示「您还没有子用户，去客户管理添加」+ CTA 按钮
  - error：弹窗或编辑页内联错误提示，含重试按钮
  - success：toast 提示「可见范围已更新」

### 4.6 测试 / 验证策略（S5 用，先列）

S3 plan 末尾的 S5 task 必须包含：
- **Playwright E2E**：模拟父账户配置 → 子用户登录验证看不到 → 父账户取消勾选 → 子用户重新登录验证看不到，全流程 happy path 自动化
- **后端单元测试**：6 个场景（AC-12）+ 跨父账户越权 + visibility/run-permission 矩阵（4 象限）
- **手动 gstack /qa**：管理端无需，仅用户端 SOP 编辑页 + 工作区列表 + chatbot 编辑页 + chatbot 列表 4 个截图回归
- **回归保护诚实声明**：visibility 与现有 run-permission 的耦合涉及权限主流程，**必须**有 Playwright E2E 保护，不接受仅靠 /qa 一次性验证

---

## §5 关键设计决策记录（S1 锁定）

| # | 决策 | 选项 | 选择 | 理由 |
|---|------|------|------|------|
| D1 | 数据表选型 | 独立新表 vs 复用现有表 + source 字段 | **独立新表** | 用户视角 deny-all 与实体视角 allow-all 语义完全不同，查询逻辑会按 source 分支高度耦合；独立表换得清晰隔离 |
| D2 | API 端点设计 | 独立端点 vs 合并到 Update | **独立端点** | visibility 独立保存，与实体其他字段解耦；错误隔离；语义清晰 |
| D3 | 开关关闭时名单数据处理 | 保留 vs 清理 | **保留** | 避免误点关闭丢失全部配置；重新打开恢复同一名单 |
| D4 | 子用户移除/转走时的清理 | 级联删除 vs LEFT JOIN 过滤 | **级联删除** | 与现有 user_template_permission 设计一致；transaction 内同步清理 4 张表（2 visibility + 2 run-permission） |
| D5 | 短路字段 | `sop_template.visibility_restricted` boolean | **加字段** | 99% SOP 不会启用限制，避免每次列表查询都 JOIN grant 表；boolean 短路判断减小性能影响 |
| D6 | 数据模型扩展性 | 是否预留 entity_type 字段做未来通用 | **不预留** | 仅 SOP + chatbot 两类；通用化是过度抽象 |
| D7 | 软删除 | 是否启用 | **启用**（与 user_template_permission 一致，不与 user_chatbot_permission 一致） | gorm.Model 自带，故意撤回时保留审计；CleanupBySubUser 用软删除 |

---

## §6 S2 Spec 必须覆盖的项目（先列）

1. 两张新表完整 DDL（字段类型/长度/默认值/索引/外键策略）
2. 6 个 API 端点完整契约（路径/方法/请求 JSON Schema/响应 JSON Schema/HTTP 状态码/错误码）
3. `IsSopVisibleToUser(ctx, userID, sopID)` / `IsChatbotVisibleToUser(...)` 函数签名 + 决策逻辑伪代码
4. 列表查询过滤集成路径（在 `ListVisibleTemplatesWithPermission` / `ListVisibleChatbotsWithPermission` 中的具体插入位置 + visibility 与 run-permission 的串行顺序图）
5. 子用户级联删除的事务序列（4 张表的清理顺序 + 失败回滚策略）
6. 前端组件层级图（编辑页加哪些组件 / 选择弹窗如何复用客户管理弹窗逻辑 / store 字段）
7. 跨父账户越权防御的具体校验代码位置
8. 错误码集中定义清单（3 个新错误码）
9. 普通无子账户用户的前后端联合判断逻辑
10. Migration 脚本（forward + rollback）

---

## §7 假设和未决事项

- **假设 1**：智能体编辑页确有一个独立 Vue 文件（S2 探明具体路径）
- **假设 2**：客户管理弹窗的子用户列表组件可被提取/复用为通用多选组件（S2 探明组件抽象方案）
- **未决 1**：S5 验证策略 task 在 S3 plan 末尾固化，包含 Playwright E2E 实跑（NDF Rule 10）；S2 spec 暂不细化测试代码，仅声明验证策略要求
