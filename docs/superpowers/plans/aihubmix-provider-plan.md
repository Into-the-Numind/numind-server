# AiHubMix Provider 接入 — 实施计划

> 继承自 spec: `numind-server/docs/superpowers/specs/2026-04-16-aihubmix-provider-design.md`
> 单仓库：numind-server
> 分支：feature/aihubmix-provider

---

## Task 依赖图

```
T1 (preflight) ─┬─> T2 (seed SQL) ──────┐
                │                       │
                └─> T3 (client extend)  ├─> T6 (S5 验证策略)
                                        │
                    T4 (router extend) ─┤
                    T5 (config yaml) ───┘
```

T1 必须最先完成（它决定 T2 的 priority 取值）。T2、T3、T4、T5 可独立实现，但 T6 S5 验证在 T2-T5 全部 commit 后执行。

---

## Task 1 — 预检：DMXAPI 当前 priority 核实

**目的**：验证 D3 决策假设（DMXAPI priority=10），最终确定 AiHubMix priority 取值。

**涉及文件**：无代码改动，只读查询。

**步骤**：
1. SSH 到 dev 数据库，执行：
   ```sql
   SELECT lmp.id, lm.model_key, lp.name AS provider, lmp.priority, lmp.is_active
   FROM llm_model_provider lmp
   JOIN llm_model lm ON lm.id = lmp.model_id
   JOIN llm_provider lp ON lp.id = lmp.provider_id
   WHERE lm.model_key IN ('claude-sonnet-4-6', 'claude-sonnet-4-6-thinking',
                          'gemini-3.1-pro-preview', 'gemini-3.1-pro-preview-thinking',
                          'deepseek-v3.2', 'deepseek-v3.2-thinking',
                          'gpt-5.4', 'gpt-5.4-thinking')
   ORDER BY lm.model_key, lmp.priority;
   ```
2. 在 spec §11 填充 DMXAPI 实际 priority 值。
3. **用户已告知 priority = 10** → AiHubMix 确定为 `priority = 5`（比 DMXAPI 小 → Router 优先选中）。

**验收条件**：
- [x] 已确认 DMXAPI priority = 10（用户提供）
- [x] AiHubMix priority 取值已确定为 5（< 10，保证主路由优先）

> **注**：priority=5 的实际写入是 T2 的责任，不在此 task 验收范畴。

**Commit**：无（纯预检，信息已记入 manifest decisions）。

---

## Task 2 — Seed Migration SQL

**文件**（新建）：`numind-server/migrations/20260416_100000_seed_aihubmix_provider.sql`

**内容**：
1. INSERT `llm_provider`（1 行）
   - name='aihubmix', display_name='AiHubMix', base_url='https://aihubmix.com/v1'
   - api_key 字面值：`sk-vduyVKfBuiI5p4P5B030A80938924aFe87Af360473612f68`
   - is_active=1
   - 使用 INSERT IGNORE（name 有 UNIQUE 约束）
2. INSERT `llm_model_provider`（8 行，priority=5，见 spec §4.2 表格）
   - 依赖 llm_provider.id 查询：`(SELECT id FROM llm_provider WHERE name='aihubmix')`
   - 依赖 llm_model.id 查询：`(SELECT id FROM llm_model WHERE model_key=...)`
   - INSERT IGNORE（uk_model_provider 有 UNIQUE (model_id, provider_id)）
3. INSERT `pricing_rule`（5 行，spec §4.3）
   - `claude-sonnet-4-6` flat 21.60/108.00
   - `claude-sonnet-4-6-think` flat 21.60/108.00
   - `deepseek-v3.2` flat 2.16/3.24
   - `gemini-3.1-pro-preview` tiered_token 0/0（价格走子表）
   - `gpt-5.4` tiered_token 0/0
4. INSERT `pricing_rule_tier`（8 行，spec §4.3 表）
   - gemini input/output 各 2 档，gpt input/output 各 2 档
   - 使用 `(SELECT id FROM pricing_rule WHERE provider='aihubmix' AND model=...)` 拼接

**涉及文件**：
- 新建：`migrations/20260416_100000_seed_aihubmix_provider.sql`（独立文件，无任何现有文件改动）

**验收条件**：
- [ ] SQL 文件语法合法（在 dev 库 dry-run，`SOURCE` 命令成功）
- [ ] dev 库执行后，`SELECT COUNT(*) FROM llm_model_provider WHERE provider_id=(SELECT id FROM llm_provider WHERE name='aihubmix')` = 8
- [ ] `SELECT COUNT(*) FROM pricing_rule WHERE provider='aihubmix'` = 5
- [ ] `SELECT COUNT(*) FROM pricing_rule_tier WHERE rule_id IN (SELECT id FROM pricing_rule WHERE provider='aihubmix')` = 8
- [ ] 所有 4 模型基础变体 + 4 模型 thinking 变体都能 SELECT 到 priority=5 的 aihubmix 路由

**Commit message**：`feat(aihubmix): seed provider/model routes and pricing rules`

---

## Task 3 — DMXAPIClient 扩展（reasoning_effort + -think 后缀）

**文件**（修改）：`numind-server/internal/pkg/llm/dmxapi_client.go`

**改动点 1（spec §5.1 改动 1）**：`-think` 后缀适配（约 line 268）
```go
// 旧
if strings.HasSuffix(strings.ToLower(model), "-thinking") {
    bodyMap["temperature"] = 1
}
// 新
lowerModel := strings.ToLower(model)
if strings.HasSuffix(lowerModel, "-thinking") || strings.HasSuffix(lowerModel, "-think") {
    bodyMap["temperature"] = 1
}
```

**改动点 2（spec §5.1 改动 2）**：`switch thinkingFormat` 新增 case（约 line 272）
```go
case "reasoning_effort":
    bodyMap["reasoning_effort"] = "high"
```

**改动点 3（spec §5.1 400 fallback 扩展）**：约 line 309 的字符串匹配
```go
// 旧
strings.Contains(string(respBody), "enable_thinking") ||
    strings.Contains(string(respBody), "thinking") ||
    strings.Contains(string(respBody), "unknown_parameter")
// 新：再加一条 reasoning_effort 匹配
strings.Contains(string(respBody), "enable_thinking") ||
    strings.Contains(string(respBody), "thinking") ||
    strings.Contains(string(respBody), "reasoning_effort") ||
    strings.Contains(string(respBody), "unknown_parameter")
```

**新增单元测试**（同目录新文件 `dmxapi_client_reasoning_test.go`）：
- Test_ReasoningEffort_InjectedWhenFormatIsReasoningEffort：mock HTTP server 验证请求 body 含 `"reasoning_effort":"high"`
- Test_ReasoningEffort_NotInjectedByDefault：thinkingFormat="" 时不含 reasoning_effort 字段
- Test_ThinkSuffix_TriggersTemperature1：model 含 `-think` 后缀时 temperature=1
- Test_400Fallback_OnReasoningEffortError：mock 返回 400 + "reasoning_effort unknown" → 自动重试去 thinking

**涉及文件**：
- 修改：`internal/pkg/llm/dmxapi_client.go`
- 新建：`internal/pkg/llm/dmxapi_client_reasoning_test.go`

**验收条件**：
- [ ] `go test ./internal/pkg/llm/...` 全部通过（含新 4 个 test case）
- [ ] `task lint` 通过
- [ ] 4 处改动与 spec §5.1 一致（reviewer 逐行对照）

**Commit message**：`feat(llm): support reasoning_effort and -think suffix for AiHubMix`

---

## Task 4 — LLMRouter 签名扩展

**文件**（修改）：
- `numind-server/internal/numind/biz/llmrouter/types.go`
- `numind-server/internal/numind/biz/llmrouter/router.go`

**改动点 1（spec §5.2）**：types.go 加常量
```go
// ThinkingReasoningEffort AiHubMix 统一推理协议：通过 reasoning_effort 参数激活
ThinkingReasoningEffort = "reasoning_effort"
```

**改动点 2（spec §5.3 改动 1）**：router.go 的 `inferThinkingFormat` 签名扩展
```go
func inferThinkingFormat(providerName, providerModelID string) string {
    id := strings.ToLower(providerModelID)
    if strings.ToLower(providerName) == "aihubmix" {
        if strings.HasSuffix(id, "-think") {
            return ThinkingNone
        }
        return ThinkingReasoningEffort
    }
    // 保留现有 Claude/GPT/DeepSeek/Doubao/Gemini/Qwen 的按 id 推断逻辑
    ...
}
```

**改动点 3（spec §5.3 改动 2）**：调用点更新
```go
tf = inferThinkingFormat(mp.Provider.Name, mp.ProviderModelID)
```

**新增单元测试**（同目录新文件 `router_aihubmix_test.go`）：
- Test_InferThinkingFormat_Aihubmix_WithThinkSuffix_ReturnsNone
- Test_InferThinkingFormat_Aihubmix_WithoutThinkSuffix_ReturnsReasoningEffort
- Test_InferThinkingFormat_Dmxapi_Gemini_StillNative：确保 DMXAPI 的 gemini 行为未变
- Test_InferThinkingFormat_Dmxapi_Claude_StillThinkingNone：确保 DMXAPI 的 claude 行为未变

**涉及文件**：
- 修改：`internal/numind/biz/llmrouter/types.go`
- 修改：`internal/numind/biz/llmrouter/router.go`
- 新建：`internal/numind/biz/llmrouter/router_aihubmix_test.go`

**验收条件**：
- [ ] `go test ./internal/numind/biz/llmrouter/...` 全部通过（含新 4 个 test case + 所有现有 test 仍通过）
- [ ] `task lint` 通过
- [ ] `inferThinkingFormat` 调用点全局搜索：只有 router.go 一处（grep 验证无遗漏）

**Commit message**：`feat(llmrouter): dispatch ThinkingFormat by provider dimension`

---

## Task 5 — Config YAML × 4

**文件**（修改）：
- `numind-server/config_local.yaml`
- `numind-server/config_dev.yaml`
- `numind-server/config_qa.yaml`
- `numind-server/config_prod.yaml`（用户已豁免 CLAUDE.md §3 规则）

**改动**：每个文件新增
```yaml
aihubmix:
  base_url: "https://aihubmix.com/v1"
  api_key: "sk-vduyVKfBuiI5p4P5B030A80938924aFe87Af360473612f68"
```

**注意（与 S1 proposal §3.2 的对齐说明）**：
- S1 proposal §3.2 原写"seed SQL 从配置读取 api_key 而非写死"。**该假设在 S3 预研后被推翻**：`internal/pkg/aiservice/seed.go:SyncProviderCredentials` 目前是 stub（ai-service-manager 功能未实现），config → DB 同步路径不存在。
- **最终方案**：seed SQL（T2）直接 INSERT 字面值 api_key 到 `llm_provider.api_key`；config 中的 `aihubmix.api_key` 是**冗余占位**，未被 runtime 读取。保留原因：
  - (a) 未来 ai-service-manager 的 `SyncProviderCredentials` 实现后会读取 config 刷新 DB
  - (b) 运维文档一致性（所有 provider 凭据都在 config 可见）
- Viper 不解析 `${AIHUBMIX_API_KEY}` 环境变量引用（用户选字面值，参见 manifest decisions）
- 该不一致属于**已知技术债**，随 ai-service-manager 完成 `SyncProviderCredentials` 后自动解决

**涉及文件**：
- 修改：4 个 config 文件

**验收条件**：
- [ ] 4 个文件都有 `aihubmix.api_key` 字段
- [ ] YAML 语法合法（`yamllint` 或 Go 启动时 viper load 不报错）
- [ ] 未修改其他配置项（diff 只新增 3 行）

**Commit message**：`chore(config): add aihubmix provider to 4 env configs`

---

## Task 6 — S5 验证策略（必填，NDF 规则 10）

### 验证方式选择

**选定**：**gstack `/qa`（浏览器 QA）+ 手动故障注入（1 次）**

**不选**：
- Playwright E2E：当前项目 E2E 套件主要覆盖 SOP 执行，新增 aihubmix 路由的 E2E 需建 fixture，工时 >1 天，超过本功能总工时
- 纯后端 TDD：无法验证 chatbot 前端 ThinkingBlock 渲染

### 理由

1. **改动集中在后端 + DB seed**，前端零改动 → 不需要前端回归测试
2. 核心验证点是"4 个模型实际调通 + thinking 渲染正常"，这是单次浏览器操作可覆盖的
3. failover 验证需临时改 api_key 为无效值，手动注入最可控

### 关键用户路径清单

**环境**：本地（`cd numind-server && task dev`；`cd numind-web-v3 && npm run dev`）。

**路径 1 — Chatbot × 4 模型**（核心）
1. 访问 `http://localhost:5173/chatbot`
2. 用 `$E2E_USERNAME` / `$E2E_PASSWORD` 登录
3. 新建会话 → ModelSelector 选 **Claude Sonnet 4.6 Thinking**
4. 发送："用一段话介绍你自己，要包含思考过程。"
5. 验证：
   - ✅ ThinkingBlock 渲染 thinking 内容（非空）
   - ✅ 正文流式输出，最终有回答
   - ✅ 浏览器 Network 标签：请求发往后端 `/v1/chatbot/...`，响应 200
   - ✅ 后端 log 含 "LLMRouter: dispatching request" + `provider=aihubmix` + `model=claude-sonnet-4-6-think`
6. 重复步骤 3-5，模型分别换为：
   - **Gemini 3.1 Pro Thinking** → 检查日志 `model=gemini-3.1-pro-preview` + `thinking_format=reasoning_effort`
   - **DeepSeek V3.2 Thinking** → 检查日志 `model=deepseek-v3.2` + `thinking_format=reasoning_effort`
   - **GPT 5.4 Thinking** → 检查日志 `model=gpt-5.4` + `thinking_format=reasoning_effort`

**路径 2 — failover 验证**
1. 临时修改 dev DB：`UPDATE llm_provider SET api_key='sk-invalid' WHERE name='aihubmix'`
2. 重启 Router 缓存或等 5min（缓存 TTL=5min）
3. chatbot 同上选 Claude，发送消息
4. 验证：
   - ✅ 浏览器最终收到回复（不报错）
   - ✅ 后端 log 含 "LLMRouter: route failed, trying next" + provider=aihubmix
   - ✅ 紧接着一条 "LLMRouter: dispatching request" + provider=dmxapi 成功
5. 恢复 api_key：`UPDATE llm_provider SET api_key='sk-vduyVKfBuiI5p4P5B030A80938924aFe87Af360473612f68' WHERE name='aihubmix'`

**路径 3 — Langfuse trace 验证**
1. 本地 Langfuse（`docker compose -f docker-compose.langfuse.yml up -d`）
2. 重跑路径 1 Claude 一次
3. 在 Langfuse UI 找到刚刚的 trace
4. 验证：
   - ✅ 存在名为 `llm-chat` 的 generation
   - ✅ generation.model = `claude-sonnet-4-6-think`
   - ✅ generation.usage.promptTokens > 0 且 completionTokens > 0

**路径 4 — Billing 验证**
1. 跑完路径 1 全部 4 模型后
2. 查 dev DB：
   ```sql
   SELECT provider, model, prompt_tokens, completion_tokens, cost_cents, created_at
   FROM usage_record
   WHERE user_id=<E2E_USERNAME 对应 ID> AND provider='aihubmix'
   ORDER BY created_at DESC LIMIT 10;
   ```
3. 验证：
   - ✅ 4 条记录（各模型一条）
   - ✅ cost_cents > 0
   - ✅ claude 行：cost_cents ≈ (prompt_tokens / 1M × 21.60 + completion_tokens / 1M × 108.00) × 100（允许 ±5% 误差）
   - ✅ gemini 行：若 prompt_tokens ≤ 200000，input 单价 14.40 ¥/M
   - ✅ gpt 行：若 prompt_tokens ≤ 272000，input 单价 18.00 ¥/M
   - ✅ deepseek 行：cost_cents ≈ (prompt_tokens / 1M × 2.16 + completion_tokens / 1M × 3.24) × 100

### 回归保护诚实声明

选择 gstack `/qa` 意味着**本功能未来修改时没有自动回归保护**。

**风险评估**：
- 本功能涉及计费（pricing_rule 新增数据）→ 高风险业务逻辑
- 但**计费逻辑本身未修改**，只是新数据。计费 recorder 在 ai-service-manager 的持续回归测试中覆盖
- failover 机制（Router.StreamChat）本身有既有单元测试（未改动）

**结论**：新增的单元测试（T3、T4）+ gstack /qa 一次性覆盖足够。未来回归由 ai-service-manager 恢复后的统一 integration test 保障。

**验收条件**：
- [ ] 4 模型路径 1 全通过（ThinkingBlock 渲染 + 日志 + 响应）
- [ ] failover 路径 2 通过（模拟故障切换成功）
- [ ] Langfuse trace 路径 3 通过（generation 记录完整）
- [ ] Billing 路径 4 通过（4 条 usage_record 且 cost 正确）

---

## S5 Gate 汇总（重跑 S4 检查 + T6 上述 4 路径）

- [ ] `task lint`（numind-server/）退出码 0
- [ ] `task test`（numind-server/）退出码 0
- [ ] T6 的 4 条路径验收通过
- [ ] Langfuse trace + billing 记录正确

---

## 规模与工时估算

| Task | 文件 | LOC 估算 | 工时 |
|------|------|---------|------|
| T1 | 0 | 0 | 10 min |
| T2 | 1 新建 | ~120 | 45 min |
| T3 | 1 改 + 1 新建测试 | ~15 改 + 80 测试 | 1h |
| T4 | 2 改 + 1 新建测试 | ~10 改 + 80 测试 | 45 min |
| T5 | 4 改 | 12 新增 | 10 min |
| T6 | 执行验证 | 0 代码 | 1h |
| **合计** | | | **~4h** |

与 S1 proposal 估算（0.5 天 = 4-6h）一致。

---

## 风险记录

1. **DMXAPI 某些模型实际 priority 与预期不符**：T1 已预检，若发现需在 T2 前调整 aihubmix 取值（当前定 5，留有下探空间）
2. **AiHubMix 账户余额消耗**（S5 路径 1 会实际调用 4 次，每次 200-500 token；路径 2 再多 1 次 DMXAPI 兜底）：用户已充值，预估成本 < $0.1
3. **Claude `-think` 变体的 `reasoning_effort` 行为未验证**：S2 D2 决策跳过 reasoning_effort，S5 路径 1 Claude 测试会实证
4. **tiered_token 单价查询逻辑未验证**：T6 billing 路径 4 是首次验证该路径准确性，若发现 bug 需 hotfix
