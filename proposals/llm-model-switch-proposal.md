# LLM 模型切换 — 提案

## §1 方案概述 [客户可见]
为 SOP 执行和智能体对话提供多模型选择能力。用户在输入框附近可以切换 LLM 模型（Claude、GPT、Gemini、豆包等），系统记住上次选择。所有模型通过 DMXAPI 聚合平台统一调用，用户无需关心底层提供商。

## §2 报价与周期 [客户可见]
- 预估工作量：2-3 天
- 交付时间线：2026-04-12

## §3 技术可行性 [AI 内部]

### 现有功能复用
- **DMXAPI 客户端**：`internal/pkg/llm/dmxapi_client.go` 已封装 `ChatCompletion` / `StreamChatCompletion`，支持任意模型名传入，可直接复用
- **计费系统**：`internal/pkg/billing/` 已支持按 provider+model 查找 pricing_rule 并记录 usage，新增模型只需补 seed 数据
- **Langfuse 追踪**：已有 trace/generation 模式，model 参数动态传入即可
- **前端下拉组件**：`SalesStageDropdown.vue` 可作为模型选择器的参考模式
- **SSE 流式调用**：chatbot store 和 SOP 执行均已有 SSE 流式处理

### 技术风险
1. **SOP 执行链路改造**：SOP 目前直接 HTTP 调用 volc API（executor.go），需改为通过 DMXAPI 客户端调用。影响面可控，executor 是唯一入口
2. **不同模型的参数差异**：Claude 支持 thinking，GPT 不支持 reasoning_effort，需要按模型做参数适配。DMXAPI 作为聚合层应已处理大部分差异
3. **模型可用性**：某些模型可能在 DMXAPI 上临时不可用，需要前端展示模型状态或 fallback

### 涉及仓库
- [x] numind-server
- [x] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性（功能涉及 LLM 调用）
- [x] 涉及 LLM 调用：是
- Trace 起点：现有 trace 起点不变（`ChatStream` 和 SOP 执行各自的 trace）
- Generation 点：现有 generation 点不变，但 model 参数从硬编码改为动态传入用户选择的模型名
- 关键元数据：`model_name`（用户选择的模型）需附在 trace metadata 上，便于按模型分析调用质量

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事
- 作为用户，我需要在智能体对话时选择不同的 LLM 模型，以便根据任务复杂度和偏好选择最合适的模型
- 作为用户，我需要在 SOP 执行时选择 LLM 模型，让所有节点使用我选择的模型执行
- 作为用户，我需要系统记住我上次选择的模型，以便下次打开时不需要重新选择

### 验收标准
- [ ] 智能体对话输入框附近有模型选择器，展示可用模型列表
- [ ] SOP 执行输入区域有模型选择器，展示可用模型列表
- [ ] 用户选择的模型全局覆盖 SOP 所有节点的模型配置
- [ ] 智能体对话使用用户选择的模型进行 LLM 调用
- [ ] 系统记住用户上次选择的模型（按功能区分：SOP 记一个，智能体记一个）
- [ ] 所有用户等级均可使用模型切换功能
- [ ] 不同模型的计费正确记录（按实际使用的模型计价）
- [ ] 可用模型列表从后端 API 获取（非前端硬编码），便于后续增减模型
- [ ] Langfuse trace 中记录用户实际使用的模型名

### 边界情况
- 用户上次选择的模型已下线/不可用 → 自动回退到默认模型，提示用户
- DMXAPI 调用某模型失败 → 返回错误信息，不自动切换模型（让用户决定）
- 用户未选择模型（首次使用）→ 使用系统默认模型（deepseek-v3-2-251201）
- 模型列表为空（配置错误）→ 使用系统默认模型，前端不展示选择器

### 权限规则
- 所有用户等级（free/trial/standard/premium）均可使用模型切换
- free 用户虽然不能运行 SOP，但可以在智能体对话中切换模型
- 管理端暂不涉及（模型列表通过配置管理，不需要管理端 UI）

### UI 行为规格

#### 智能体对话 — 模型选择器
- 页面位置：`ChatbotChat.vue` 输入框工具栏区域（send 按钮左侧）
- 布局要求：紧凑型下拉按钮（pill 样式），显示当前模型名称 + 下拉箭头
- 交互模式：点击展开模型列表，选择后收起，立即生效于下次发送
- 状态处理：
  - loading：模型列表加载时显示 skeleton
  - empty：无可用模型时隐藏选择器，使用默认模型
  - error：加载失败时使用默认模型，不阻塞对话
  - success：显示模型列表，标记当前选中项

#### SOP 执行 — 模型选择器
- 页面位置：`SOPView.vue` 产品/脚本输入区域上方或旁边
- 布局要求：与智能体相同的 pill 下拉样式，保持视觉一致
- 交互模式：选择后对当次 SOP 执行生效，覆盖所有节点模型
- 状态处理：同智能体

#### 模型偏好持久化
- 存储位置：后端数据库（user_model_preference 表）
- 按功能区分：`chatbot` 和 `sop` 各自独立记忆
- 持久化时机：用户切换模型时立即保存

### 初期开放模型列表（通过 DMXAPI 调用）

| 显示名称 | 模型 ID | 提供商 |
|----------|---------|--------|
| DeepSeek V3 | deepseek-v3-2-251201 | DMXAPI |
| Claude Sonnet 4 | claude-sonnet-4-20250514 | DMXAPI |
| GPT-4o | gpt-4o | DMXAPI |
| Gemini 2.5 Flash | gemini-2.5-flash-preview-05-20 | DMXAPI |
| 豆包 | doubao-seed-2-0-lite-260215 | DMXAPI |

> 注：具体模型 ID 需与 DMXAPI 平台确认可用性，以上为初始候选列表。
