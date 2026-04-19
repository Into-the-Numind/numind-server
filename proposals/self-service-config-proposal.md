# B端自助配置工具（Chatbot + SOP 配置器）— 提案

## §1 方案概述 [客户可见]

为 B 端客户（父用户）在用户端新增"配置中心"模块，提供两项自助配置能力：

1. **智能体配置器**：B 端客户可创建 chatbot 智能体，设置系统提示词，选择挂载知识库，发布后其 C 端用户可直接使用。
2. **SOP 工作流配置器**：B 端客户可自定义多步骤 SOP 工作流，为每个步骤设置提示词，发布后其 C 端用户可在首页看到并执行。

配置完成后，B 端客户可随时编辑、暂停、下线已发布的智能体和 SOP。C 端用户使用时，积分消耗算在 C 端用户自己头上。

## §2 报价与周期 [客户可见]

- 预估工作量：8-12 天（AI 辅助开发）
- 交付时间线：2026-04-21（预估）

## §3 技术可行性 [AI 内部]

### 现有功能复用

| 模块 | 复用内容 | 改造点 |
|------|---------|--------|
| SOP 模板系统 | `SopTemplate` + `SopNode` 模型、store CRUD、执行引擎完整可用 | 新增 `creator_user_id` 字段区分谁创建的模板；B 端 API 复用 admin 的模板管理逻辑 |
| 多租户权限 | `ParentUserID` 层级、`UserTemplatePermission` 白名单、`UserFeaturePermission` 功能开关 | 扩展：父用户创建模板后自动授权给其子用户 |
| 文档基础设施 | `KnowledgeDocument` + `KnowledgeChunk` 上传/解析/向量化/检索全链路 | 需新增"知识库"抽象层（`KnowledgeBase` 表），将文档打包为可挂载单元 |
| 积分计费 | `UsageRecord` + `BillingAccount` | 无需改造，C 端用户运行时自动扣费 |
| SSE 流式输出 | SOP 节点执行和 SalesRAG 的 streaming 模式 | Chatbot 对话复用相同的 SSE 模式 |
| 前端组件 | `AppButton`, `AppInput`, `MainLayout`, `AppSidebar` | 新增配置中心页面，复用现有组件体系 |

### 技术风险

| 风险 | 等级 | 缓解方案 |
|------|------|---------|
| SalesRAG 业务层与 sales 深度耦合 | 中 | Chatbot 独立实现 biz 层，仅复用底层文档/向量检索基础设施（store 层），不复用 SalesRAG biz 层的销售特定逻辑 |
| 知识库抽象层引入新表 | 低 | 数据模型简单（KnowledgeBase + 关联表），不影响现有 KnowledgeDocument 表 |
| B 端用户配置 SOP 时的模型/API Key 选择 | 中 | B 端不暴露模型选择，使用平台预设的默认模型配置；SopNode 的 BaseURL/APIKey/ModelName 由系统自动填充 |
| 前端路由守卫——区分父用户和子用户 | 低 | 用户登录后 `/v1/user/profile` 已返回 `parent_user_id`，前端据此判断 |

### 涉及仓库

- [x] numind-server（后端 API + 数据模型）
- [x] numind-web-v3（B 端配置 UI + C 端展示）
- [ ] numind-admin-web（不涉及）

### AI 可观测性（功能涉及 LLM 调用）

- [x] 涉及 LLM 调用：是（Chatbot 对话、SOP 节点执行）
- **SOP 执行**：已有完整 Langfuse tracing，无需额外工作
- **Chatbot 对话**（新增）：
  - Trace 起点：`biz/chatbot/ChatStream()` 创建 trace
  - Generation 点：每轮 LLM 对话 → `generation:chatbot-chat`；RAG 检索 → `generation:chatbot-retrieval`
  - 关键元数据：`chatbot_id`, `user_id`, `session_id`, `knowledge_base_ids`

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事

**B 端客户（父用户）：**
- 作为 B 端客户，我需要创建并配置 chatbot 智能体（设置名称、描述、头像、系统提示词、挂载知识库），以便快速上线给我的 C 端用户使用
- 作为 B 端客户，我需要创建并配置 SOP 工作流（设置名称、描述、多个步骤及其提示词），以便 C 端用户可以按步骤执行工作流
- 作为 B 端客户，我需要管理知识库（创建知识库、上传文档到知识库、管理文档列表），以便挂载到 chatbot 智能体上
- 作为 B 端客户，我需要管理已发布的智能体和 SOP（编辑、暂停、下线、重新上线），以便灵活控制 C 端可见内容
- 作为 B 端客户，我需要预览配置好的智能体和 SOP 的效果，以便发布前确认

**C 端用户（子用户）：**
- 作为 C 端用户，我需要在首页看到所属 B 端客户发布的智能体和 SOP（分区展示），以便选择使用
- 作为 C 端用户，我需要与智能体进行多轮对话（支持流式输出），以便获取知识库中的信息或完成任务
- 作为 C 端用户，我需要执行 SOP 工作流（逐步执行，每步输入→AI 输出），复用现有 SOP 执行体验

### 验收标准

**配置中心（B 端）：**
- [ ] 父用户登录后可见"配置中心"入口，子用户不可见
- [ ] 可创建 chatbot 智能体：填写名称、描述、系统提示词、选择挂载知识库
- [ ] 可创建 SOP 工作流：填写名称、描述、添加/删除/排序步骤、每步设置提示词
- [ ] 可创建知识库：命名知识库、上传文档（复用现有文档解析链路）、查看文档列表
- [ ] 发布后，该父用户的所有子用户可在首页看到
- [ ] 可编辑已发布的智能体/SOP（编辑后实时生效）
- [ ] 可下线/重新上线智能体/SOP
- [ ] SOP 步骤支持拖拽排序

**C 端展示与交互：**
- [ ] 首页分区展示智能体和 SOP（沿用现有分区布局）
- [ ] 智能体：点击进入对话界面，支持多轮对话、流式输出
- [ ] SOP：点击进入执行界面，复用现有 SOP 执行流程
- [ ] 对话和执行过程中正确扣除 C 端用户积分

**权限与隔离：**
- [ ] B 端客户只能看到和管理自己创建的配置
- [ ] C 端用户只能看到其所属父用户发布的内容
- [ ] 不同 B 端客户之间的配置完全隔离

### 边界情况

- B 端客户删除知识库时，已挂载该知识库的 chatbot 如何处理？→ 解挂但不删 chatbot，下次对话时无 KB 上下文
- B 端客户下线 SOP 时，C 端用户正在执行中的 run 如何处理？→ 正在执行的 run 允许完成，新 run 不可创建
- C 端用户积分不足时？→ 复用现有积分不足拦截逻辑
- B 端客户没有创建任何配置时？→ C 端首页对应区域显示空状态
- 知识库文档上传中（解析/向量化未完成）就挂载到 chatbot？→ 允许挂载，但检索时跳过未完成的文档
- 步骤数量上限？→ SOP 最多 20 步，chatbot 无步骤概念

### 权限规则

| 操作 | 父用户（B 端） | 子用户（C 端） | admin |
|------|--------------|--------------|-------|
| 创建/编辑/删除配置 | 可以 | 不可以 | 可以（内部运维） |
| 查看已发布的智能体/SOP | 可以（自己的） | 可以（所属父用户的） | 可以（全部） |
| 使用智能体/执行 SOP | 可以 | 可以（受积分限制） | 可以 |
| 管理知识库 | 可以（自己的） | 不可以 | 可以（全部） |

- 父用户使用配置功能需要具有 `self_service_config` feature permission
- C 端用户使用智能体/SOP 受现有 tier 体系限制

### UI 行为规格

**配置中心入口（B 端）：**
- 页面位置：左侧导航新增"配置中心"一级菜单，仅父用户可见
- 子页面：智能体管理 / SOP 管理 / 知识库管理（三个 tab 或子菜单）

**智能体管理页：**
- 布局：表格列表（名称、状态、创建时间、操作）— 遵循管理页表格规则
- 操作：新建 → 弹出表单 modal / 抽屉；编辑、下线、删除
- 表单：名称（必填）、描述、头像上传、系统提示词（textarea）、知识库选择（多选下拉/穿梭框）
- 状态处理：loading 骨架屏 / empty 引导文案+创建按钮 / error 重试

**SOP 管理页：**
- 布局：表格列表（名称、步骤数、状态、创建时间、操作）
- 操作：新建 → 进入配置编辑页（非 modal，因为步骤编辑较复杂）
- 配置编辑页：左侧步骤列表（可拖拽排序、增删）+ 右侧当前步骤的提示词编辑区
- 状态处理：同上

**知识库管理页：**
- 布局：表格列表（知识库名称、文档数、创建时间、操作）
- 操作：新建知识库 → 进入详情页（文档列表+上传）
- 文档上传：复用现有上传组件，显示解析进度

**C 端首页展示：**
- 智能体区：卡片网格（头像、名称、描述），点击进入对话
- SOP 区：卡片网格（名称、描述、步骤数），点击进入执行
- 两个区域分开展示，各自有空状态处理

**Chatbot 对话页（C 端新增）：**
- 布局：类似现有 SalesRAG 对话界面（左侧会话列表 + 右侧对话区）
- 消息：支持 Markdown 渲染、流式打字效果
- 输入：文本输入框 + 发送按钮
- 状态：AI 回复中显示 loading 指示器

### 数据模型概要（新增表）

| 表名 | 核心字段 | 说明 |
|------|---------|------|
| `chatbot_config` | id, user_id, name, description, avatar, system_prompt, status(draft/published/offline), created_at, updated_at | 智能体配置 |
| `chatbot_knowledge_base` | id, chatbot_id, knowledge_base_id | 智能体-知识库挂载关系 |
| `knowledge_base` | id, user_id, name, description, created_at, updated_at | 知识库抽象（文档分组） |
| `knowledge_base_document` | id, knowledge_base_id, document_id | 知识库-文档关系 |
| `chatbot_session` | id, user_id, chatbot_id, title, status, created_at, updated_at | 对话会话 |
| `chatbot_message` | id, session_id, role, content, thinking, trace_id, token counts, created_at | 对话消息 |

- SOP 配置复用现有 `sop_template` + `sop_node` 表，新增 `creator_user_id` 字段
- 知识库文档复用现有 `knowledge_document` + `knowledge_chunk` 表

### API 端点概要（新增）

**B 端配置 API（/v1/config/...）：**
- `POST/GET/PUT/DELETE /v1/config/chatbots` — 智能体 CRUD
- `PUT /v1/config/chatbots/:id/publish` — 发布/下线
- `POST/GET/PUT/DELETE /v1/config/sop-templates` — SOP 模板 CRUD
- `POST/PUT/DELETE /v1/config/sop-templates/:id/nodes` — 步骤管理
- `PUT /v1/config/sop-templates/:id/publish` — 发布/下线
- `POST/GET/PUT/DELETE /v1/config/knowledge-bases` — 知识库 CRUD
- `POST/DELETE /v1/config/knowledge-bases/:id/documents` — 文档管理

**C 端使用 API（/v1/chatbot/...）：**
- `GET /v1/chatbot/list` — 获取当前用户可见的智能体列表
- `POST /v1/chatbot/sessions` — 创建对话会话
- `GET /v1/chatbot/sessions` — 会话列表
- `POST /v1/chatbot/sessions/:id/chat` — 发送消息（SSE 流式）
- `GET /v1/chatbot/sessions/:id/messages` — 获取历史消息
