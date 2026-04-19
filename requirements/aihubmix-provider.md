# AiHubMix Provider 接入

## 来源
- 提出人：产品负责人
- 提出日期：2026-04-16

## 需求描述

引入 AiHubMix（`https://aihubmix.com/v1`）作为新的 LLM 聚合提供商，与现有 DMXAPI 并行。以下 4 个模型通过 AiHubMix 接入，统一使用 OpenAI 兼容 `/chat/completions` 端点，thinking 模式通过 `reasoning_effort` 参数激活：

| 模型 | AiHubMix provider_model_id |
|------|----------------------------|
| Claude 4.6 Sonnet | `claude-sonnet-4-6` |
| Gemini 3.1 Pro Preview | `gemini-3.1-pro-preview` |
| GPT 5.4 mini | `gpt-5.4-mini` |
| DeepSeek V3 | `DeepSeek-V3` |

这 4 个模型已有 DMXAPI 路由，AiHubMix 作为**新增主路由**接入，DMXAPI 作 failover 备份（不删除现有 DMXAPI 路由）。

## 业务目标

- 提供主备两套 LLM 通道，提升 SOP 执行和 chatbot 的可用性（单一聚合商故障不影响业务）
- 尝试 AiHubMix 的统一 `reasoning_effort` 协议，避免 DMXAPI 下 Gemini/GPT 各走不同原生端点带来的代码分支复杂度
- 为后续引入更多模型铺路

## 影响范围

- **使用入口**：SOP 执行（`biz/sop/executor.go`）、Chatbot（`biz/chatbot/`），两者均经 `biz/llmrouter/` 统一调度，改一处通吃
- **用户可见变化**：上述 4 个模型在 ModelSelector 中仍然存在，底层路由增加 AiHubMix 主路径；thinking 内容通过 `reasoning_content` 返回并渲染到 `ThinkingBlock`

## 优先级
高（用户要求立即上线）

## Triage

- **推荐轨道：Standard**
- **分类理由**：
  1. 数据库 schema 变更：否（仅 seed 数据 INSERT，不改表结构）
  2. 新增 API 端点：否（纯内部路由调整）
  3. 新外部服务集成：**是**（AiHubMix）
  4. 影响文件数：**>3**（`dmxapi_client.go` + `llmrouter` 映射 + config × 4 + seed SQL ≈ 6-7 文件）
  5. 高风险业务逻辑（支付/权限）：边界（新增 pricing rules，只加不改，failover 兜底）
- **判定**：2 条明确不满足 → Standard 不可降级
- **人类决定**：确认 Standard，快节奏推进

## 与活跃功能 ai-service-manager 的关系

ai-service-manager 当前处于 S4（暂停中），其 deferred 列表声明"本期不新增 Provider"。用户确认走**方案 A**：当前在旧 `llm_provider` / `llm_model_provider` 表上接入 AiHubMix，日后随 ai-service-manager 恢复时跟随迁移到 Service Registry 架构。迁移成本为 seed SQL 转换，不是逻辑重写。

## 备注

**已确认的技术路径**（供 S1/S2 参考，非本阶段产出）：
- AiHubMix 统一推理规范参考：https://docs.aihubmix.com/cn/api/unified-inference
- 请求注入：`reasoning_effort: "high"`（取值 low/medium/high/xhigh）
- 响应字段：`reasoning_content`（thinking 流式内容）+ `reasoning_details`（结构化元数据）
- API key 管理：遵循 `.claude/rules/ai-service.md`，禁止硬编码，config_*.yaml 注入（不复用 DMXAPI 当前硬编码的反例）

**遗留待 S1 澄清**：
- AiHubMix 各模型定价（按 token 费率，用于 `seed_pricing_rules.sql`）
- 4 个 provider_model_id 在 AiHubMix 官方 models 清单的最终确认（避免 404）
