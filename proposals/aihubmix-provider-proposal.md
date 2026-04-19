# AiHubMix Provider 接入 — 提案

## §1 方案概述 [客户可见]

引入 AiHubMix 作为第二家 LLM 聚合平台，与现有 DMXAPI 并行运行，提升系统可用性。

**带来的变化**：
- 系统同时对接 AiHubMix + DMXAPI 两条通道，任一家故障另一家自动接管，SOP 执行和 chatbot 不中断
- 4 个核心模型（Claude 4.6 Sonnet / Gemini 3.1 Pro Preview / GPT 5.4 mini / DeepSeek V3）通过 AiHubMix 统一协议调用，thinking 深度思考模式正常工作
- **用户无感**：ModelSelector 中模型名不变，只是底层多了一条更稳定的调用路径

## §2 报价与周期 [客户可见]

- 预估工作量：**0.5 天**（4-6 小时）
- 报价：内部需求不计费
- 交付时间线：**当日上线 dev，次日进 prod**（按 NDF 流程 S2→S7 快速推进）

## §3 技术可行性 [AI 内部]

### 现有功能复用

| 已有模块 | 复用方式 |
|---------|---------|
| `internal/pkg/llm/DMXAPIClient` | 直接复用（此客户端本质是"OpenAI 兼容通用 HTTP 客户端"，命名 misleading）。通过 `NewDMXAPIClientWithConfig(baseURL, apiKey)` 可指向任意 OpenAI 兼容端点 |
| `biz/llmrouter/Router.StreamChat` | 已实现 failover 机制（遍历 routes 列表，失败自动切下一条），AiHubMix 作为第一条 route、DMXAPI 作为第二条即可 |
| `StreamChatCompletion` SSE 解析器 | 已支持 `reasoning_content` 字段（`dmxapi_client.go:362`），AiHubMix 返回的 thinking 内容无需额外解析代码 |
| `ThinkingBlock.vue` + chatbot / SOP 前端 | 已消费 `thinking` eventType，前端零改动 |
| `llm_provider` / `llm_model_provider` 表 | 设计时已支持多 provider，seed SQL 追加数据即可 |

### 技术改动点（最小化）

1. **`internal/pkg/llm/dmxapi_client.go`**：`StreamChatCompletion` 的 `switch thinkingFormat` 新增 `case "reasoning_effort"` 分支，注入 `bodyMap["reasoning_effort"] = "high"`（约 3 行）
2. **`internal/numind/biz/llmrouter/types.go`**：新增常量 `ThinkingReasoningEffort = "reasoning_effort"`
3. **`internal/numind/biz/llmrouter/router.go`**：`inferThinkingFormat` 签名扩展为 `(providerName, providerModelID string)`，provider 为 `aihubmix` 时直接返回 `ThinkingReasoningEffort`，不再按 providerModelID 推断
4. **Seed SQL**（新 migration 文件）：
   - INSERT `llm_provider` 一行（name=aihubmix, base_url=https://aihubmix.com/v1, api_key=占位，生产环境由 runtime config 覆盖）
   - INSERT `llm_model_provider` 4 行（映射现有 4 个 llm_model 到 aihubmix provider），`priority` 设为 10（高于 DMXAPI 的 20，实现主路由）
   - INSERT `pricing_rules` 4 行（输入/输出单价按 AiHubMix 官方报价填入）
5. **配置**：`config_*.yaml` 新增 `aihubmix.api_key`（按环境 local/dev/qa/prod 各配），provider seed SQL 从配置读取 api_key 而非写死

### 风险

| 风险 | 概率 | 缓解 |
|------|------|------|
| AiHubMix 官方 provider_model_id 与用户提供字符串不一致（如实际是 `gpt-5.1-mini` 而非 `gpt-5.4-mini`） | 中 | S2 开始前必须通过 `curl https://aihubmix.com/v1/models -H "Authorization: Bearer <key>"` 拉取官方清单核对 |
| `reasoning_effort` 在某些模型上不被识别触发 400 | 低 | 现有代码已有 fallback：400 且 body 含 "unknown_parameter" 自动去 thinking 重试（`dmxapi_client.go:308-316`），复用此机制即可 |
| AiHubMix 作主路由上线初期响应延迟/错误率波动 | 中 | failover 机制自动切回 DMXAPI；S5 验收时观察一轮 langfuse trace 的 latency/error 分布再决定是否保留主路由优先级 |
| 定价规则漏配导致计费错误 | 低 | 一个 4 行的 INSERT，S4 review 必查 pricing_rules 覆盖所有 4 个新 `llm_model_provider` |
| 与 ai-service-manager Service Registry 架构的后续迁移成本 | 已接受 | S0 阶段已决策（方案 A），迁移成本为 seed SQL 转换 |

### 涉及仓库

- [x] numind-server
- [ ] numind-web-v3（无前端改动）
- [ ] numind-admin-web（无管理端改动）

### AI 可观测性

- [x] 涉及 LLM 调用：是
- **Trace 起点**：不新增。复用现有 `biz/sop/executor.go` 和 `biz/chatbot/` 的 trace 起点
- **Generation 点**：不新增。`biz/llmrouter/router.go:StreamChat` 已统一记录 `llm-chat` generation（含 model、input、output、prompt/completion tokens），AiHubMix 走同一路径，zero 代码改动
- **关键元数据**：generation 的 `model` 字段已记录 `route.ProviderModelID`（如 `claude-sonnet-4-6`），provider 身份通过 `billing.RecordLLM` 的 `route.ProviderName`（如 `aihubmix`）区分，满足事后观察主备通道表现的需求

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事

- 作为 **SOP 执行用户**，我希望选择 Claude 4.6 Sonnet / Gemini 3.1 Pro Preview / GPT 5.4 mini / DeepSeek V3 运行 SOP 时，系统优先通过 AiHubMix 调用，以便获得更稳定的响应和 thinking 输出。
- 作为 **Chatbot 用户**，我希望在 chatbot 页面（`/chatbot`）中使用上述 4 个模型时，thinking 内容仍然正常流式渲染到 ThinkingBlock，以便看到模型的推理过程。
- 作为 **运维 / 开发者**，我希望 AiHubMix 任一 API 故障时，系统自动切换到 DMXAPI 完成调用，以便业务不中断。
- 作为 **财务 / 计费**，我希望 AiHubMix 调用产生的 token 用量按 AiHubMix 官方单价准确扣减用户积分，不出现零费或错费。

### 验收标准

- [ ] 在 `/chatbot` 页面选择 **Claude 4.6 Sonnet**，发送一条问题，返回内容包含 thinking 段落（前端 ThinkingBlock 渲染），且 Langfuse trace 中该 generation 的 `model` 字段为 `claude-sonnet-4-6`，`usage.promptTokens` 和 `usage.completionTokens` 非零
- [ ] 同上操作对 **Gemini 3.1 Pro Preview**、**GPT 5.4 mini**、**DeepSeek V3** 分别验证一次
- [ ] 在 SOP 运行时选择上述任一模型，Langfuse billing 记录的 `provider_name` 为 `aihubmix`（非 `dmxapi`）
- [ ] 人为构造 AiHubMix 故障（如临时把 config 中 AiHubMix api_key 改成无效值），重新触发同一调用，Langfuse 记录应出现：一次 AiHubMix generation 失败 → 一次 DMXAPI generation 成功（failover 生效）
- [ ] 查询 usage_record 表，对新调用产生的记录的 `cost_cents` 字段 > 0 且与 AiHubMix 官方单价一致（按 prompt+completion tokens 计算）
- [ ] `task lint` + `go test ./...` 通过
- [ ] `gstack /qa` 覆盖上述 4 模型各一条问答，无视觉/功能回归

### 边界情况

- **AiHubMix 返回非标准 reasoning_content 结构**（如 `reasoning_details.type != "thinking"`）：当前 SSE 解析器只读 `delta.reasoning_content`，对 `reasoning_details` 结构化字段不做处理。接受此限制（Phase 1），若后续需要展示 reasoning_details 再扩展
- **`reasoning_effort: "high"` 被某个模型拒绝**：已有 400 重试机制去掉 thinking 再跑，但会失去 thinking 输出。记录 warn 日志即可，不阻塞调用
- **failover 级联失败**（AiHubMix 挂 + DMXAPI 也挂）：`Router.StreamChat` 返回 `all routes failed for model %q` 错误，上层 SOP/chatbot 已处理，无需新增逻辑
- **配置缺失**（`aihubmix.api_key` 未配）：启动时 provider seed SQL 跳过或写入空 key → 运行时调用 401 → 自动 failover 到 DMXAPI。建议在 `NewDMXAPIClientWithConfig` 上游校验，空 key 时 warn 日志（参照 DMXAPI 现有模式）
- **并发场景**：llmrouter 的 cache 层已支持并发安全（`cache` 结构体用 RWMutex），无需额外处理

### 权限规则

- 不涉及用户等级变化。现有 free/trial/standard/premium 权限规则（`.claude/rules/business-logic.md`）不变
- 不涉及管理端新增配置界面（Phase 1 通过 SQL seed 开关 AiHubMix，上下架不需要 UI 操作；未来 ai-service-manager 的管理端会统一接管）

### UI 行为规格

- 无前端改动。`ModelSelector.vue` 组件已动态拉取 `llm_model` 列表渲染，不需要硬编码模型名
- Chatbot 页 `/chatbot`：thinking 流式输出复用 `ThinkingBlock.vue`（已实现）
- SOP 页：thinking 流式输出复用现有 `ChatBubble.vue` + `TrailingChat.vue`（已实现）
- **状态处理**：loading/error 均已由现有 executor / chatbot store 处理，failover 切换对用户透明（不抛出 "尝试下一路由" 之类的中间状态）

## §5 实施里程碑（供 S3 plan 参考）

预计 4-6 个 task，全部在 `numind-server` 单仓库：

1. Seed migration：新增 `llm_provider` / `llm_model_provider` / `pricing_rules` 数据
2. `dmxapi_client.go` 扩展：新增 `reasoning_effort` 分支 + 配套单元测试
3. `llmrouter/types.go` + `router.go` 扩展：provider 维度派发 ThinkingFormat
4. `config_*.yaml` 新增 `aihubmix.api_key` 配置项 + secret 注入到 seed（不硬编码）
5. S5 验证策略 task（必填，按 NDF 规则 10）

S2 spec 阶段会细化每个 task 的文件列表、接口签名、验收条件。
