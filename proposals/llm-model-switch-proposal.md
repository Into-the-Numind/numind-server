# LLM 模型切换 — 提案

## §1 方案概述 [客户可见]
为 SOP 执行和智能体对话提供多模型选择能力。用户在输入框附近可以切换 LLM 模型（Claude、GPT、Gemini、DeepSeek 等），系统记住上次选择。后端支持多供应商智能调度，自动选择最便宜/可用的供应商调用，用户无需关心底层路由。

## §2 报价与周期 [客户可见]
- 预估工作量：3-4 天（因增加多供应商路由和管理端）
- 交付时间线：2026-04-14

## §3 技术可行性 [AI 内部]

### 现有功能复用
- **DMXAPI 客户端**：`internal/pkg/llm/dmxapi_client.go` 已封装 OpenAI 兼容格式的 `ChatCompletion` / `StreamChatCompletion`，可泛化为通用 OpenAI 兼容客户端，支持任意 base_url + api_key
- **计费系统**：`internal/pkg/billing/` 已支持按 provider+model 查找 pricing_rule 并记录 usage
- **Langfuse 追踪**：已有 trace/generation 模式，model 参数动态传入即可
- **前端下拉组件**：`SalesStageDropdown.vue` 可作为模型选择器的参考模式
- **SSE 流式调用**：chatbot store 和 SOP 执行均已有 SSE 流式处理

### 技术风险
1. **SOP 执行链路改造**：SOP 目前直接 HTTP 调用 volc API（executor.go），需改为通过统一 LLM 路由层调用。影响面可控，executor 是唯一入口
2. **不同模型的参数差异**：Claude 支持 thinking，GPT 不支持 reasoning_effort。大部分聚合平台已处理这些差异，但需测试验证
3. **供应商故障处理**：某供应商不可用时需 fallback 到次优供应商，需要优雅降级
4. **API key 管理复杂度**：部分平台（如 LinkAPI）按分组分 key，同一供应商可能需要多个 key。设计上将 api_key 放在路由映射层（模型×供应商）而非供应商层

### 涉及仓库
- [x] numind-server
- [x] numind-web-v3
- [x] numind-admin-web（供应商/模型管理页面）

### AI 可观测性（功能涉及 LLM 调用）
- [x] 涉及 LLM 调用：是
- Trace 起点：现有 trace 起点不变（`ChatStream` 和 SOP 执行各自的 trace）
- Generation 点：现有 generation 点不变，但 model 和 provider 参数从硬编码改为动态传入
- 关键元数据：`model_key`（用户选择的逻辑模型）、`provider`（实际路由到的供应商）、`provider_model_id`（供应商侧模型名）需附在 trace metadata 上

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事
- 作为用户，我需要在智能体对话时选择不同的 LLM 模型，以便根据任务复杂度和偏好选择最合适的模型
- 作为用户，我需要在 SOP 执行时选择 LLM 模型，让所有节点使用我选择的模型执行
- 作为用户，我需要系统记住我上次选择的模型，以便下次打开时不需要重新选择
- 作为管理员，我需要管理供应商配置和模型路由映射，以便灵活调整模型来源和成本

### 验收标准
- [ ] 智能体对话输入框附近有模型选择器，展示可用模型列表
- [ ] SOP 执行输入区域有模型选择器，展示可用模型列表
- [ ] 用户选择的模型全局覆盖 SOP 所有节点的模型配置
- [ ] 智能体对话使用用户选择的模型进行 LLM 调用
- [ ] 系统记住用户上次选择的模型（按功能区分：SOP 记一个，智能体记一个）
- [ ] 所有用户等级均可使用模型切换功能
- [ ] 后端根据路由表自动选择最优供应商（价格最低+可用）
- [ ] 供应商调用失败时自动 fallback 到次优供应商
- [ ] 不同供应商的计费正确记录（按实际使用的供应商+模型计价）
- [ ] 可用模型列表从后端 API 获取（非前端硬编码）
- [ ] 管理端可管理供应商配置（增删改查）
- [ ] 管理端可管理模型和路由映射（增删改查）
- [ ] Langfuse trace 中记录用户选择的逻辑模型 + 实际路由的供应商

### 数据模型

#### llm_provider（供应商表）
| 字段 | 类型 | 说明 |
|------|------|------|
| id | uint | 主键 |
| name | string | 供应商名称（如 dmxapi, linkapi, openrouter） |
| display_name | string | 显示名称 |
| base_url | string | API 基础地址 |
| is_active | bool | 是否启用 |

#### llm_model（逻辑模型表 — 用户看到的）
| 字段 | 类型 | 说明 |
|------|------|------|
| id | uint | 主键 |
| model_key | string | 逻辑模型标识（如 claude-sonnet-4-6-thinking） |
| display_name | string | 显示名称（如 Claude Sonnet 4.6 Thinking） |
| icon | string | 模型图标标识（可选） |
| sort_order | int | 排序权重 |
| is_active | bool | 是否对用户可见 |

#### llm_model_provider（路由映射表 — 模型×供应商）
| 字段 | 类型 | 说明 |
|------|------|------|
| id | uint | 主键 |
| model_id | uint | 关联 llm_model |
| provider_id | uint | 关联 llm_provider |
| provider_model_id | string | 供应商侧实际模型名（可能与 model_key 不同） |
| api_key | string | 该路由使用的 API key（支持同一供应商不同 key） |
| priority | int | 优先级（数字越小越优先，通常按价格排） |
| input_price | decimal | 输入价格（元/百万tokens） |
| output_price | decimal | 输出价格（元/百万tokens） |
| is_active | bool | 该路由是否启用 |

#### user_model_preference（用户偏好表）
| 字段 | 类型 | 说明 |
|------|------|------|
| id | uint | 主键 |
| user_id | uint | 用户 ID |
| feature | string | 功能标识（chatbot / sop） |
| model_key | string | 选择的逻辑模型标识 |
| updated_at | datetime | 最后更新时间 |

### 路由算法
```
输入：逻辑 model_key
1. 查 llm_model_provider WHERE model_key = ? AND is_active = true ORDER BY priority ASC
2. 依次尝试供应商，用对应的 base_url + api_key + provider_model_id 调用
3. 成功 → 返回结果 + 记录计费（用实际的 provider + model）
4. 失败 → 尝试下一个供应商
5. 全部失败 → 返回错误
```

### 边界情况
- 用户上次选择的模型已下线/不可用 → 自动回退到默认模型，提示用户
- 所有供应商对某模型都失败 → 返回错误信息，不自动切换到其他模型
- 用户未选择模型（首次使用）→ 使用系统默认模型
- 模型列表为空（配置错误）→ 使用系统硬编码默认模型，前端不展示选择器
- 供应商 API key 为空或无效 → 跳过该供应商，尝试下一个
- 管理员禁用某供应商 → 该供应商的所有路由自动失效

### 权限规则
- 所有用户等级（free/trial/standard/premium）均可使用模型切换
- free 用户虽然不能运行 SOP，但可以在智能体对话中切换模型
- 管理端：供应商/模型管理需要管理员权限

### UI 行为规格

#### 用户端 — 模型选择器（智能体 + SOP 共用组件）
- 页面位置：输入框工具栏区域（send 按钮左侧）
- 布局要求：紧凑型下拉按钮（pill 样式），显示当前模型名称 + 下拉箭头
- 交互模式：点击展开模型列表，选择后收起，立即生效于下次发送/执行
- 状态处理：
  - loading：模型列表加载时显示 skeleton
  - empty：无可用模型时隐藏选择器，使用默认模型
  - error：加载失败时使用默认模型，不阻塞使用
  - success：显示模型列表，标记当前选中项

#### 管理端 — 供应商管理页面
- 表格展示所有供应商（名称、API 地址、状态）
- 支持增删改查
- API key 字段脱敏显示

#### 管理端 — 模型管理页面
- 表格展示所有逻辑模型（名称、标识、排序、状态）
- 每个模型可展开查看/编辑路由映射
- 路由映射：供应商、供应商侧模型名、API key、优先级、价格

#### 模型偏好持久化
- 存储位置：后端数据库（user_model_preference 表）
- 按功能区分：`chatbot` 和 `sop` 各自独立记忆
- 持久化时机：用户切换模型时立即保存

### 初期开放模型列表

| 显示名称 | 逻辑模型 key | 初始供应商 |
|----------|-------------|-----------|
| Claude Sonnet 4.6 Thinking | claude-sonnet-4-6-thinking | DMXAPI / LinkAPI |
| Gemini 3.1 Pro | gemini-3.1-pro-preview | DMXAPI / LinkAPI |
| DeepSeek V3.2 | deepseek-v3.2 | DMXAPI / LinkAPI |
| GPT 5.4 | gpt-5.4 | DMXAPI / LinkAPI |
| DeepSeek V3.2 Thinking | deepseek-v3.2-thinking | DMXAPI / LinkAPI |

> 注：具体模型 ID 和供应商路由在管理端配置，以上为初始候选列表。每个模型可配置多个供应商路由，按价格优先级自动选择。
