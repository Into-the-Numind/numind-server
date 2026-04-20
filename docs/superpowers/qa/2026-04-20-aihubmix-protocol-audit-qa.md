# aihubmix-protocol-audit — S5 QA Report

> **Feature**: aihubmix-protocol-audit
> **NDF 阶段**: S5 — 本地验收（Playwright E2E + 手动 Langfuse 面板检视 + DB verification）
> **S4 commits**: c09686a / b3ce22b / 59c7b31 / 2cdcf81 / 2bbb9b9 / e5904f6 / edd5a0b / cad5511 / (Task 9 TBD) / (Task 10 TBD)
> **S3 plan**: `docs/superpowers/plans/2026-04-20-aihubmix-protocol-audit-plan.md`
> **S2 spec**: `docs/superpowers/specs/2026-04-20-aihubmix-protocol-audit-design.md`

**本文档骨架在 S3 plan Task 11 产出。S5 阶段填充 §1-§6 执行结果。**

---

## §1 Playwright E2E 8 路径执行结果

**执行环境**：本地 `numind-web-v3` dev server（`LOCAL_SITE_URL=http://localhost:5173`）
**后端**：`LOCAL_API_URL=http://localhost:9091`（连接 dev DB）
**凭据**：`E2E_USERNAME` + `E2E_PASSWORD`（env variables per CLAUDE.md §7）

**运行命令**：
```bash
cd numind-web-v3
E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD \
  npm run test:e2e -- aihubmix-thinking-audit.spec.ts
```

| # | 路径 | 预期 | 实际 | Pass/Fail |
|---|------|------|------|-----------|
| 1 | Claude thinking | thinking event + reasoning_content 非空 | — | — |
| 2 | SOP thinking | thinking SSE event | — | — |
| 3 | GPT 5.4 no CoT | content 非空 + 前端无报错 | — | — |
| 4 | qwen-turbo skip | 无 thinking event + 200 | — | — |
| 5 | Thinking=false 显式 | 出站 body 不含 reasoning_effort | — | — |
| 6 | Claude -think variant | thinking event（或 skipped，UI 限制记录下方） | — | — |
| 7 | Gemini intrinsic | thinking event + 后端 trace metadata `intrinsic` | — | — |
| 8 | Preference 保存-变体 bug 回归 | 保存 thinking=true 变体 → 200（非 400） | — | — |

**UI 限制记录（若 Path 6 或 8 UI 不暴露）**：
- Path 6：hotfix-default-thinking-mode 把 -thinking 变体从 ModelSelector 过滤掉，若 dev 环境实测 UI 无法选中该 model_key，记录"跳过，仅靠后端单测覆盖"
- Path 8：thinking toggle 被 `v-if="false"` 藏起来，采用 **Playwright request API 直接 POST** `/v1/web/user-model-preferences` fallback（见 spec.ts 实现）

---

## §2 Langfuse Dev 面板实测

**目的**：验证 Task 9 的 TraceMetadata 嵌入到 `output.metadata` 后，Langfuse UI 能查到 resolved 字段。

**步骤**：
1. 登录 Langfuse dev（地址 per ai-service.md § 4 配置）
2. 在 Chatbot 发一条触发 Claude 4.6 thinking 的消息（prompt: "用三步推导斐波那契第 10 项"）
3. 在 Langfuse Trace 面板按 user_id 过滤最新一条 trace
4. 展开该 trace 的 generation（aiservice.ChatStream 入口）
5. 打开 **Output 标签页（JSON viewer）**（**不是** Metadata 标签——SDK 无 WithGenMetadata，resolved 字段嵌在 output）
6. 搜索 `resolved_reasoning_effort` / `resolved_model_family` / `temp_overridden`

**期望 JSON 内容**（Claude base + thinking=true）：
```json
{
  "content": "...Fibonacci 推导文本...",
  "reasoning_content": "...思考过程...",
  "metadata": {
    "task_id": "chatbot.stream",
    "service_id": "1",
    ...
    "resolved_reasoning_effort": "medium",
    "resolved_model_family": "claude",
    "temp_overridden": true
  },
  ...
}
```

**实测结果**：
- [ ] Langfuse trace 可见
- [ ] Output 标签含 `metadata.resolved_reasoning_effort="medium"`
- [ ] Output 标签含 `metadata.resolved_model_family="claude"`
- [ ] Output 标签含 `metadata.temp_overridden=true`
- [ ] `usage.reasoning_tokens` 字段显示非零值（若 Claude，因 T2 §2.1 证实 Claude 不暴露 reasoning_tokens，此字段为 0 是预期）

**偏差记录**：若 Langfuse 配置不在 dev 环境启用（参见 ai-service.md §4 "dev: enabled"），改为手工日志验证——将后端 log level 临时调 debug，搜 "WithGenOutput" 附近日志。

---

## §3 gstack /qa 补充截图验证（可选）

**前置**：使用 gstack `/qa` skill 在 dev 环境自动截图验证前端渲染。

**场景**：
- 登录 dev 环境（`DEV_SITE_URL` + `E2E_USERNAME/PASSWORD`）
- 进入 Chatbot，选 Claude 4.6
- 发消息"解释 REST vs GraphQL 3 个关键差异"
- 截图 chat window 展示的 thinking 块（若有）+ content 区域

**实测截图**：
- [ ] 待补 `docs/superpowers/qa/2026-04-20-qa-chatbot-claude-thinking.png`
- [ ] 待补 `docs/superpowers/qa/2026-04-20-qa-sop-deepseek-thinking.png`

---

## §4 DB Migration 执行前后 verification

**migration 7a（fix_ai_service_thinking_flags）**执行时机：Task 6 deploy 后、Task 4+5 deploy 前（见 plan 部署序 Step 2）

**执行前**（dev DB 快照）：
```
id | model_key                        | supports_thinking | thinking_only
1  | claude-sonnet-4-6                | 1                 | 0
5  | claude-sonnet-4-6-thinking       | 0                 | 0
12 | gemini-3.1-pro-preview           | 1                 | 1
13 | deepseek-v3.2                    | 1                 | 1
14 | gpt-5.4                          | 1                 | 1
15 | gemini-3.1-pro-preview-thinking  | 0                 | 0
16 | deepseek-v3.2-thinking           | 0                 | 0
17 | gpt-5.4-thinking                 | 0                 | 0
```

**执行**：SSH dev → `mysql ... < migrations/20260421_000001_fix_ai_service_thinking_flags.sql`

**执行后期望**：
```
id | model_key                        | supports_thinking | thinking_only
1  | claude-sonnet-4-6                | 1                 | 0    (unchanged)
5  | claude-sonnet-4-6-thinking       | 1                 | 1    (fixed)
12 | gemini-3.1-pro-preview           | 1                 | 1    (unchanged)
13 | deepseek-v3.2                    | 1                 | 0    (fixed)
14 | gpt-5.4                          | 1                 | 0    (fixed)
15 | gemini-3.1-pro-preview-thinking  | 1                 | 1    (fixed)
16 | deepseek-v3.2-thinking           | 1                 | 1    (fixed)
17 | gpt-5.4-thinking                 | 1                 | 1    (fixed)
```

**实际结果**：
- [ ] Pre-migration 快照 match 上表
- [ ] Migration 执行无错（pre-flight guard 返回 1，不触发除零）
- [ ] Post-migration 6 行修正到目标值
- [ ] `ROW_COUNT()` 返回 6（UPDATE 影响 6 行）

**migration 7b（audit_user_model_preference）**：

- [ ] 运行 `migrations/audit/20260421_preference_audit.sh` 落 log 到 `migrations/audit/20260421_preference_audit_output.log`，commit
- [ ] 执行 migration SQL 的 Part B UPDATE：期望 `ROW_COUNT()=0`（理论上 preference.go:242 历史上一直强推 thinking=1，不存在 thinking=0 行）；若 >0 记录并人工评估

---

## §5 计费 spike 结论引用

见 `docs/aihubmix-billing-reconciliation-spike.md`。

**S5 阶段任务**：查 AiHubMix dashboard（https://aihubmix.com/dashboard）对 spike 的 2 个 request id 的扣费：
- LOW request: `chatcmpl-DWjJLBFYRpFYAD6PF56lrsG7jpIVP`
- HIGH request: `chatcmpl-DWjJQ4A94M6vX8wj323D2ZwQ2nUvo`

**实测扣费**（¥/请求）：
- LOW:  _______  ¥
- HIGH: _______  ¥

**判定**（A/B/C per spike §4）：
- [ ] Option A 独立计价
- [ ] Option B 并入 completion（预期）
- [ ] Option C 数据不足

**行动**：
- 若 B（预期）：pricing_rule 无需改，本期 feature 闭环
- 若 A：登记独立 hotfix 加 `pricing_rule.reasoning_price_per_mtok` 列

---

## §6 已知偏差 / Tech Debt（不在本期修）

1. **Gemini 伪流式**（T2 §2.2）：Gemini 3.1 Pro SSE 只产出 4 个 chunk（批量 reasoning_content + 最后一批 content），前端 token-by-token 渲染体验不佳。前端 chunk 处理逻辑本期零改动，该体验限制保留。
2. **GPT 5.4 无 reasoning_content**（T2 §2.4）：OpenAI 加密推理策略导致 `message.reasoning_content` 字段不存在，前端 thinking block 为空（fallback 到仅显示 `reasoning_tokens` 数字 UI 为 tech debt #6 in plan §12）。
3. **DeepSeek 模型名大小写波动**（T2 observation 6）：`DeepSeek-V3.2` vs `deepseek-v3.2`——本期 code 已用 `route.ProviderModelID` 而非 `chunk.Model` 做 billing key，无影响，但记录。
4. **AiHubMix 凭据硬编码**：plan §12 Tech Debt #1，SyncProviderCredentials 机制就绪后独立 hotfix 处理。
5. **`reasoning_effort` 硬编码 medium**：plan §12 Tech Debt #2，未来 openrouter-provider feature 恢复时让用户/admin 可调。

---

## §7 S5 执行总结

- [ ] Playwright 4-8 路径全 pass（允许 1-2 UI 限制跳过）
- [ ] Langfuse trace metadata 实测可见
- [ ] DB migration 前后对比符合预期
- [ ] 计费 spike dashboard 数据补全
- [ ] gstack /qa 补充截图（可选）

**S5 gate**：以上满足则进入 S6（merge feature branch → develop → push → CI deploy dev）。
