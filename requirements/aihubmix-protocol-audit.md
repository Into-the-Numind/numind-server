# AiHubMix 协议审计 + 思考管道真实打通

## 来源
- 提出人：产品负责人
- 提出日期：2026-04-20
- Pivot 背景：从 openrouter-provider S2 scope（12 task、9.4 天）拆分而来。用户决定放弃同期上 OpenRouter + 矩阵视图重构，聚焦两件核心事：把现有 AiHubMix 真的用对 + 让深度思考真的发生

## 需求描述

### T2 — AiHubMix 协议权威化（调研文档）

针对 AiHubMix 现有 4 个模型，产出一份**权威调用手册** `docs/aihubmix-protocol-reference.md`，每个模型逐条回答：

1. **Endpoint**：走哪个 URL（都是 `POST https://aihubmix.com/v1/chat/completions` 吗？还是有模型走 `/responses` 等 OpenAI 新接口？）
2. **请求字段**：`max_tokens` vs `max_completion_tokens`？`reasoning_effort` 是否支持？支持哪些取值（`none/low/medium/high/xhigh/minimal`）？thinking 是否通过**模型 slug 后缀**激活（Claude `-think`）？`temperature` 是否有模型级约束（Claude `-think` 变体强制 temp=1）？
3. **响应字段**：普通返回 `message.content`；思考模型返回什么？`message.reasoning_content`？`message.reasoning`？OpenAI 系列加密推理只返 `usage.reasoning_tokens`？
4. **SSE 流式增量**：`delta.content` + `delta.reasoning_content`（AiHubMix Claude/Gemini/DeepSeek）vs `delta.reasoning`（OpenRouter 方言，本期不涉及）
5. **已知坑**：GPT 5.4 用 `max_tokens` 直接 400；Claude base slug 不带 thinking 时就是普通模式；reasoning_content 空字符串 vs 不返回字段的区别

**4 个模型**：
| model_key | provider_model_id（AiHubMix 侧） | 思考激活方式（调研确认） |
|-----------|----------------------------------|--------------------------|
| claude-sonnet-4-6 | `claude-sonnet-4-6` | 现状用 `-think` 后缀变体；待调研是否也支持 `reasoning_effort` |
| claude-sonnet-4-6-thinking | `claude-sonnet-4-6-think`（后缀）| 温度强制 1，固定返回 reasoning_content |
| gpt-5.4 | `gpt-5.4` | `reasoning_effort` + `max_completion_tokens`，但 OpenAI 加密推理不回 reasoning_content（只 reasoning_tokens） |
| gemini-3.1-pro-preview | `gemini-3.1-pro-preview` | `reasoning_effort` 有效，返回 reasoning_content |
| deepseek-v3.2 | `deepseek-v3.2` | `reasoning_effort` 有效（用户确认 V3.2 是思考模型），返回 reasoning_content |

### T1 — 思考管道真实打通（代码修正）

**2026-04-20 hotfix（H3-done）表象上"默认思考 ON"，但实际后端链路丢弃 thinking flag**。确切位置：

- `internal/numind/biz/sop/executor.go:109` 写着 `_ = thinking // 待 Task 9 后续接通 Gateway thinking 模式` — thinking 参数直接丢到地上
- `internal/numind/biz/chatbot/stream.go:177-179` 同样的 `_ = thinking` 语义

**需要做的代码改动**（具体边界等 S1/S2 收敛，现阶段只列已知范围）：

1. **aiservice 层**：`internal/pkg/aiservice/gateway.go` 的 `ChatRequest` 增加 thinking 语义载体（具体字段名待 S2 定）
2. **Adapter 层**：`internal/pkg/aiservice/adapter/dmxapi.go` 的 `Chat` / `ChatStream` 按 T2 调研的 per-model 规则构造请求
3. **Adapter 请求结构**：`internal/pkg/aiservice/adapter/adapter.go` 的 `oaiChatRequest` 可能需要新增 `ReasoningEffort` / `MaxCompletionTokens` 字段
4. **SOP / Chatbot 入口**：删除 `executor.go:109` 和 `chatbot/stream.go:177` 的 `_ = thinking`，真实传递
5. **Registry**：是否需要在 `ResolvedRoute` 添加 `ReasoningEffort` 字段待定（也可以只在 adapter 层按 model 名推断）
6. **DB seed**（可选）：如选择路由级配置，需补 migration 给存量 AiHubMix 4 条 base 路由 seed `reasoning_effort='medium'`

## 业务目标

- **消灭假思考**：用户以为深度思考默认 ON，但实际跑普通模式——产品体验和信任危机
- **协议权威化**：每个模型的正确用法有明文依据，未来加 LinkAPI / OpenRouter 时按同一标准接入，不再靠试错
- **降低运维成本**：AI 排查"为什么没思考"不再需要翻 commit 历史和实测，查文档即可

## 影响范围

- **仓库**：numind-server 单仓库
- **后端改动**：
  - `internal/numind/biz/sop/executor.go:109` — 删 `_ = thinking`，真实传递
  - `internal/numind/biz/chatbot/stream.go:177` — 同上
  - `internal/pkg/aiservice/gateway.go` — `ChatRequest` 结构扩展
  - `internal/pkg/aiservice/adapter/dmxapi.go` — 请求构造按 per-model 分派
  - `internal/pkg/aiservice/adapter/adapter.go` — `oaiChatRequest` 字段扩展
  - 可选：`internal/pkg/aiservice/registry/registry.go` — `ResolvedRoute` 加字段
  - 可选：`migrations/20260421_XXXXXX_seed_aihubmix_reasoning_effort.sql` — 存量路由 seed thinking 档位
  - 新增：`docs/aihubmix-protocol-reference.md` — T2 调研文档
- **前端改动**：无
- **用户可见变化**：深度思考真的会思考（返回 reasoning_content 或 reasoning_tokens），成本按思考档位计

## 优先级
高（产品承诺的"默认深度思考"当前是空承诺，必须修）

## Triage

- **推荐轨道：Standard**
- **分类理由**：
  1. 数据库 schema 变更：否（可选 migration 只是 seed 数据 UPDATE/INSERT，不改表结构）
  2. 新增 API 端点：否
  3. 新外部服务集成：否（AiHubMix 已接入，本期只是正确化）
  4. 影响文件数：**>3**（gateway + adapter×2 + executor + stream + 可能 registry = 5-6 Go 文件 + 1 doc）
  5. 高风险业务逻辑：**是**（thinking flag 影响全线 AI 成本和用户感知行为，路径穿过 SOP/Chatbot 两大入口）
- **判定**：2 条不满足 → Standard 不可降级
- **人类决定**：确认 Standard，加速节奏推进

## 与活跃功能的关系

- **hotfix-default-thinking-mode (H3-done)**：本 feature 正是要"兑现"该 hotfix 承诺的语义，两者在同一业务链路上，但已完成的 hotfix 前端改动（下拉过滤 + default=true）不需要回滚
- **openrouter-provider (deferred)**：本 feature 为未来 OpenRouter 重启铺路——T2 的协议文档就是 OpenRouter 接入的蓝本
- **linkapi-provider (triage-pending)**：依赖本 feature 的调研产物作为标准，完成后启动

## 相对 openrouter-provider S2 的 scope 收敛

| 维度 | openrouter-provider（deferred）| aihubmix-protocol-audit（本 feature）|
|------|-------------------------------|--------------------------------------|
| 新外部服务 | ✅ OpenRouter | ❌ 只用存量 AiHubMix |
| DB schema | ✅ ai_service_route 加 reasoning_effort 列 | ❌ 只 seed 数据，不改列 |
| 管理端矩阵视图 | ✅ numind-admin-web 新视图 | ❌ 不做 |
| thinking 管道打通 | ✅ | ✅（本期唯一代码核心）|
| 协议权威文档 | 隐性在 spec 里 | ✅ 独立 docs/ 交付物 |

## 备注

**已知事实（供 S1/S2 验证）**：
- GPT 5.4 `reasoning_effort` 支持 `none/low/medium/high/xhigh`（AiHubMix 官方）
- OpenAI 加密推理策略：`/chat/completions` 不返回 `reasoning_content`，只在 `usage.reasoning_tokens` 记录（已实测确认）
- Claude `-think` 后缀变体温度强制 1
- Gemini 3.1 Pro `reasoning_effort=medium` 实测返回 241 chars + 291 reasoning_tokens
- DeepSeek V3.2 `reasoning_effort=medium` 实测返回 359 chars + 131 reasoning_tokens
- AiHubMix API key：豁免方式硬编码在 migrations/20260416_100000_seed_aihubmix_provider.sql（tech debt 已登记）

**遗留 S1 澄清点**：
1. thinking flag 在 aiservice 层的载体设计——`ChatRequest.Thinking bool` vs 细化为 `ChatRequest.ReasoningEffort string`？与 per-user preference 如何合成（忽略 pref 全按 route / 还是 pref 覆盖 route default）？
2. Claude 路由：是否趁本期把 `-think` 后缀变体（model_key=`claude-sonnet-4-6-thinking`）也改为 base slug + `reasoning_effort`？还是保留现状（因为 -think 变体温度强制 1 有业务意义）？
3. S5 验证策略：Playwright E2E（自动回归）还是 gstack /qa（一次性人工验收）？thinking 是高风险逻辑，倾向 E2E
