# OpenRouter Provider 接入

## 来源
- 提出人：产品负责人
- 提出日期：2026-04-20

## 需求描述

引入 **OpenRouter**（`https://openrouter.ai/api/v1`）作为新的 LLM 聚合提供商，与现有 AiHubMix / DMXAPI 并行。**OpenRouter 作为新主路由接入**（优先级高于 AiHubMix 当前的 priority=5），AiHubMix 和 DMXAPI 作 failover 备份（不删除现有路由）。

以下 4 个模型通过 OpenRouter 接入，统一使用 OpenAI 兼容 `/chat/completions` 端点，thinking 模式通过 `reasoning_effort` 参数激活：

| 模型（model_key） | OpenRouter provider_model_id |
|-------------------|------------------------------|
| Claude 4.6 Sonnet (`claude-sonnet-4-6`) | `anthropic/claude-sonnet-4.6` |
| Gemini 3.1 Pro Preview (`gemini-3.1-pro-preview`) | `google/gemini-3.1-pro-preview` |
| GPT 5.4 (`gpt-5.4`) | `openai/gpt-5.4` |
| DeepSeek V3.2 (`deepseek-v3.2`) | `deepseek/deepseek-v3.2` |

## 业务目标

- 再扩一级主备，OpenRouter 作为最高优先级主路由，降低单点故障风险
- OpenRouter 在全球聚合商中模型覆盖最广、价格透明、稳定性口碑好，适合承担主流量
- 验证三方聚合（OpenRouter + AiHubMix + DMXAPI）并行策略在实际流量下的调度效果

## 影响范围

- **使用入口**：SOP 执行（`biz/sop/executor.go`）、Chatbot（`biz/chatbot/`），两者均经 `biz/llmrouter/` 统一调度，路由顺序按 `priority` 升序取值
- **后端改动**：
  - migrations/ 新增 `20260420_XXXXXX_seed_openrouter_provider.sql`（llm_provider + llm_model_provider×8 + pricing_rule×5 + pricing_rule_tier×8）
  - `internal/pkg/aiservice/seed.go` — providerSeedEntries 追加 openrouter 条目（凭据走 SyncProviderCredentials，**不硬编码**）
  - `internal/numind/numind.go` — RegisterProviderAlias("openrouter", "dmxapi")（复用 dmxapi adapter，因协议同属 OpenAI 兼容）
  - `config_dev.yaml` + `config_prod.yaml` 追加 `ai_providers.openrouter.api_key` 和 `base_url`
  - 可能涉及 `dmxapi_client.go` 的 ThinkingFormat 分派函数（`inferThinkingFormat` 增加 openrouter 分支）
- **前端改动**：无（ModelSelector 展示的模型未变，只是底层路由新增一层）
- **用户可见变化**：无感；可观测层面 Langfuse 可按 provider tag 过滤到 openrouter 调用

## 优先级
高（用户明确要求加速推进）

## Triage

- **推荐轨道：Standard**
- **分类理由**：
  1. 数据库 schema 变更：否（仅 seed 数据 INSERT，不改表结构）
  2. 新增 API 端点：否（纯内部路由调整）
  3. 新外部服务集成：**是**（OpenRouter）
  4. 影响文件数：**>3**（seed SQL + seed.go + numind.go + config×2 + 可能 dmxapi_client.go = 5-6 文件）
  5. 高风险业务逻辑（支付/权限）：边界（新增 pricing rules，只加不改，failover 兜底）
- **判定**：2 条明确不满足 → Standard 不可降级
- **人类决定**：确认 Standard，加速节奏推进

## 与活跃功能 hotfix-default-thinking-mode 的关系

hotfix-default-thinking-mode 当前 stage=H1，涉及 `biz/llmrouter/preference.go`。本功能不触及该文件，两者可并行推进。

## 相对 aihubmix-provider 的差异（吸取教训）

| 维度 | aihubmix-provider | openrouter-provider |
|------|-------------------|---------------------|
| API key 管理 | **字面值直写 migration SQL**（CLAUDE.md §3 豁免，tech debt） | **走 SyncProviderCredentials 机制**（config→DB，清洁路径） |
| Claude thinking | `-think` 后缀变体（如 `claude-sonnet-4-6-think`） | 同 base 模型 + `reasoning_effort="high"`（OpenRouter 原生支持 Claude extended thinking 走统一协议） |
| 优先级 | priority=5（低于 DMXAPI=10，即优先选中） | priority=1（低于 AiHubMix=5，即最优先选中）|

## 备注

**已确认的技术路径**（供 S1/S2 验证，非本阶段产出）：
- OpenRouter API 文档：https://openrouter.ai/docs/api-reference
- 请求格式：OpenAI 兼容 `POST /chat/completions`，body 含 `model`, `messages`, `max_tokens`, `temperature`, `stream`
- Thinking 参数：`reasoning_effort`（low/medium/high）或 `reasoning: {effort: "high"}` 对象形式；两者 OpenRouter 都接受
- 可选 attribution header：`HTTP-Referer: <app url>` + `X-Title: <app name>`（不强制）
- API key：`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`（dev + prod 使用同一 key；用户已授权明文配置到 config_*.yaml）

**遗留待 S1 澄清**：
- OpenRouter 各模型定价（用户未明示 → S1 调查 OpenRouter 官方定价表；若成本低于 AiHubMix 同型号，可考虑降低 sell 价格；若一致则沿用 aihubmix 的 pricing 结构）
- 4 个 provider_model_id 在 OpenRouter 官方 models 清单的最终确认（避免 404；OpenRouter 的 slug 有精确版本号，如 `anthropic/claude-sonnet-4.6` 与 `anthropic/claude-4-sonnet` 可能同时存在，需选稳定长期可用的那个）
- dmxapi_client.go 对 OpenRouter base_url 的兼容性验证（理论上同协议，但 OpenRouter 的错误码/流式格式可能有细节差异，S1 预研期发一次真实请求验证）
