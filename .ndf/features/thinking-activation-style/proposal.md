# 提案 + PRD：OAI 适配器思考激活方式（thinking_style）

## §1 问题陈述
`agnes-2.0-flash`（0 价会员专属模型，provider=`agnes-ai`，走 dmxapi fallback 适配器）在 SOP/对话中不进入思考模式。根因见 requirement.md：适配器只会发 `reasoning_effort`，而 agnes（Qwen/vLLM 风格）只认 `chat_template_kwargs.enable_thinking:true`；且 dev 注册表把它配成 `supports_thinking=0`+`thinking_only=1`（矛盾）。

## §2 产品级思考（office-hours 精简）
- **真实痛点**：会员用得最多的免费模型不会思考 → 回答质量低于预期，会员感知"不如付费模型"。
- **最小切口**：让适配器支持第二种思考激活方式，并把"用哪种"做成注册表数据而非代码——这样不仅修 agnes，也让未来任何 Qwen 系/OpenAI 兼容新模型零代码接入思考。
- **挑战假设**：是否该硬编码 provider 名？否——违反"DB 注册表是 SOT、加模型零代码"架构（见 project_add_ai_model_registry_runbook）。用户已拍板走注册表加列。
- **未来契合**：列设计为枚举可扩展（reasoning_effort / enable_thinking_kwarg / none / 未来更多），新激活方式只加 case 不改 schema。

## §3 技术可行性
- 复用现有门控 `if req.Thinking && route.SupportsThinking`（dmxapi.go:141），仅替换内部分支逻辑。
- 复用现有 raw-SQL 读取路径（store.go GetResolvedRoute / GetResolvedRouteByModelKey），加一列即可。
- `oaiChatRequest` 加 `chat_template_kwargs`（omitempty）→ 非思考请求 wire 格式字节级不变。
- 风险：**回归既有思考模型**（Gemini 3.1/Claude -think/DeepSeek V3.2/GPT-5）。缓解：空 thinking_style 默认 = reasoning_effort（= 今日行为），无需回填即零回归；单测断言每条路径 wire body。

## §4 PRD

### 用户故事
- US1：作为会员用户，我用 agnes-2.0-flash 跑 SOP/对话时，模型应进入思考模式并返回思考过程（reasoning_content）。
- US2：作为运营/开发，我给注册表新模型配好 `thinking_style` 即可让其思考，无需改 Go 代码。

### 验收标准（AC）
- AC1：`thinking_style='enable_thinking_kwarg'` + `Thinking=true` + `supports_thinking=1` + `thinking_only=0` → wire body 含 `chat_template_kwargs.enable_thinking=true`，**不含** `reasoning_effort`。
- AC2：`thinking_style='reasoning_effort'` 或 `''`(legacy) → wire body 含 `reasoning_effort="medium"`，**不含** `chat_template_kwargs`（零回归既有模型）。
- AC3：`thinking_style='none'` + 可选思考 → 既不发 reasoning_effort 也不发 chat_template_kwargs。
- AC4：`thinking_only=1`（intrinsic）→ 无论 thinking_style 为何，wire body 都不带激活字段，TraceMetadata 记 "intrinsic"（行为不变）。
- AC5：`supports_thinking=0` 或 `Thinking=false` → 无任何思考字段（门控不变）。
- AC6：agnes-2.0-flash 数据修正为 `thinking_style='enable_thinking_kwarg'`, `supports_thinking=1`, `thinking_only=0`，dev 实跑返回思考内容。
- AC7：既有思考模型（Claude base+Thinking 仍强制 temperature=1）行为不变。

### 范围
- 单仓库 numind-server。前端零改。
- Out（follow-up）：admin 端模型表单暴露 thinking_style 字段。

## §5 工作量估算
S4 约 3 个 code task（schema/model、registry 读取、adapter 发射）+ 1 验证策略 task。小型。
