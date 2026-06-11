# OAI 适配器思考激活方式（thinking_style）

## 来源
- 提出人：用户（zchen）
- 提出日期：2026-06-11

## 需求描述
用户反馈 `agnes-2.0-flash` 模型在 SOP/对话中没有开启思考模式。排查确认：

1. 思考 flag 已正确从用户偏好透传到 `ChatRequest.Thinking`（旧的 executor 丢弃缺口早已修复）。
2. `agnes-2.0-flash` 走 `agnes-ai` provider（`apihub.agnes-ai.com/v1`，OpenAI 兼容），无专属适配器 → fallback 到 dmxapi 适配器。
3. **根因（适配器能力缺口）**：dmxapi 适配器构造的 `oaiChatRequest` 只有 `reasoning_effort` 字段，**没有 `chat_template_kwargs`**。而 agnes-2.0-flash（Qwen/vLLM 风格）激活思考靠请求体 `chat_template_kwargs.enable_thinking: true`，不认 `reasoning_effort`。
4. **次因（注册表配错）**：dev `ai_service` 里 agnes-2.0-flash 配成 `supports_thinking=0` + `thinking_only=1`（矛盾）→ 门控 `if req.Thinking && route.SupportsThinking` 为假 → 任何思考参数都不发。

## 业务目标
agnes-2.0-flash 是 0 价会员专属模型（见 feature `free-model-member-only`）——会员用得最多的模型却不会思考，直接影响会员体验与回答质量。修复后让该模型在 SOP/对话中真正进入思考模式，并把"思考激活方式"做成注册表可配置项，使未来任何 OpenAI 兼容新模型（Qwen 系等）在管理端配置即可，无需改适配器代码（符合"DB 注册表是 SOT、加模型零代码"架构原则）。

## 优先级
中（影响会员核心模型回答质量，但非生产事故/数据损失）

## Triage
- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：**是**（`ai_service` 新增 `thinking_style` 列 + 回填 + agnes 数据修正）
  2. 新增 API 端点：否（管理端字段暴露列为 follow-up，本 feature 不含）
  3. 新外部服务集成：否（agnes-ai provider 已集成，仅改其思考激活方式）
  4. 影响文件数：>3（migration + registry store/struct + adapter struct/逻辑 + 测试）
  5. 高风险业务逻辑（支付/权限）：否（但触及计费间接相关：思考会产生 reasoning token；需保证不回归既有思考模型行为）
- 人类决定：用户已确认走 Standard + 注册表加列方案（2026-06-11，AskUserQuestion 选定）

## 范围边界
- **In**：`ai_service.thinking_style` 列（migration + 回填，零回归既有 reasoning_effort/intrinsic 模型）；registry 读取该列；dmxapi 适配器按 `thinking_style` 选择发 `reasoning_effort` / `chat_template_kwargs.enable_thinking` / 不发；agnes-2.0-flash 数据修正（`thinking_style='enable_thinking_kwarg'`, `supports_thinking=1`, `thinking_only=0`）；配套单测。
- **Out（follow-up）**：管理端模型编辑表单暴露 `thinking_style`（admin API + numind-admin-web 表单）——本 feature 用 migration 直接配 agnes 值，UI 自服务留后续。
- **回归保护**：必须保证 Gemini 3.1 / Claude -think / DeepSeek V3.2 / GPT-5 等既有思考模型行为字节级不变。

## 备注
- 关键代码落点：`internal/pkg/aiservice/adapter/dmxapi.go:141` buildOAIRequest 门控；`adapter.go:156` oaiChatRequest 结构；`registry/store.go` + `registry/registry.go` ResolvedRoute；`gateway.go:173` dmxapi fallback。
- 验证策略候选：后端 Go 单测为主（dmxapi_thinking_test.go 扩展，断言 wire body 按 thinking_style 出对应字段）；dev 部署后用 agnes-2.0-flash 实跑确认 reasoning_content 返回。
