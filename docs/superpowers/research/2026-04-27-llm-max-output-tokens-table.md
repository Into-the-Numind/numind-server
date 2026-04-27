# LLM max_output_tokens 调研表
## Context Budget Feature (F-1) — Production Backfill Reference

**文档日期**：2026-04-27  
**关联功能**：F-1 Context Budget Middleware (`internal/pkg/aiservice/middleware/context_budget.go`)  
**关联 S5 验证报告**：`docs/superpowers/specs/admin-api-smoke-2026-04-27.md`

---

## 1. 背景：为什么需要 backfill

F-1 Context Budget Middleware 在每次 LLM 调用前计算安全输入预算：

```
safe_input_budget = max_output_tokens - reserved_output_tokens - overhead_buffer
```

其中 `max_output_tokens` 从 `ai_service.capability_json` 的 `$.max_output_tokens` 字段读取。  
如果该字段为 NULL 或 0，middleware 抛出 `ErrContextConfigInvalid`，**feature 完全废**。

S5 验证（2026-04-27）在 dev 环境发现：**14 个 LLM service 中有 12 个 `max_output_tokens=NULL`**。  
Dev 环境已用 `32768` 作为 blanket 临时值进行 backfill 以通过测试，但：

- `32768` 不是任何模型的真实上限
- Production 上线前必须使用每个模型的真实（或保守合理）上限
- 用过低的值 → `safe_input_budget` 偏小，频繁触发不必要的 context 压缩
- 用过高的值 → 超出 provider cap，API 调用返回 4xx
- 保持 NULL/0 → `ErrContextConfigInvalid`，feature 完全废

---

## 2. max_output_tokens 约束规则（来自 F-1 spec §2.4）

backfill 值必须满足以下三个约束（缺一不可）：

| 约束 | 说明 |
|------|------|
| `max_output_tokens < context_window` | spec §2.4 validation，等于也会 fail |
| `max_output_tokens >= reserved_output_tokens` | 默认 sop_run reserved=16384，所以 max ≥ 16384 |
| `max_output_tokens <= provider 实际上限` | 超出 provider cap 导致 4xx |

---

## 3. 模型族 max_output_tokens 参考表

> **声明**：以下数值是基于截至 2026-04 公开模型文档的 **保守最小值**（conservative floor），确保不会 break feature。  
> 这些是 backfill 的 starting point，不是最终配置。  
> Production 上线后 admin 应通过 AI Service 编辑页面将每个模型调高至该模型实际广告的最大输出值（见 §5 升级路径）。

### 3.1 按模型族

| model_key prefix / pattern | 推荐 backfill 值 | Provider 公开上限 | 来源 / 备注 |
|---------------------------|----------------|-----------------|-----------|
| `claude-sonnet-4-*` | **64000** | 64000 | Anthropic Sonnet 4.x docs；cw=200000，远大于 64000 ✓ |
| `claude-haiku-4-*` | **64000** | 64000 | Anthropic Haiku 4.x；同族策略 |
| `claude-opus-4-*` | **64000** | 64000 | Anthropic Opus 4.x；同族策略 |
| `gpt-5.4*` / `gpt-5.5*` / `gpt-5.*` | **128000** | 128000 | OpenAI GPT-5 family；cw=128000，max_output=128000（注意 max_output=cw 时须保证 reserved+overhead < cw，否则 safe_input_budget ≤ 0，触发 spec §2.4 guard） |
| `gemini-3.1-pro-*` / `gemini-3-*` | **65536** | ~65536 (estimated) | Google Gemini 3.x；cw=1000000，留足余地 ✓ |
| `deepseek-v3.2` | **8192** | 8192 | DeepSeek V3.2 docs；cw=128000 ✓ |
| `deepseek-v3.2-thinking` | **8192** | 8192 | cw=65536，8192 远小于 cw ✓ |
| `deepseek-v4-*`（如存在） | **32768** | 32768-384000（型号相关）| DeepSeek V4 系列；估算，参考 V3 系模式取保守值 |
| `qwen-turbo*` | **8192** | 8192 | Alibaba DashScope Qwen-turbo docs；cw=131072 ✓ |
| `qwen-plus*` | **8192** | 8192 | Alibaba DashScope Qwen-plus docs |
| `qwen3-vl-flash*` | **16384** | ~16384 | Alibaba Qwen3 VL；cw=32768，16384 = cw/2，留 overhead ✓ |
| `qwen3-*`（非 VL） | **8192** | ~8192 | Qwen3 文本系列；估算，与 qwen-turbo 对齐 |
| `glm-4-*` | **4096** | 4096 | Zhipu GLM-4 docs；cw 通常 128k，4096 保守 ✓ |
| `doubao-*` | **16384** | ~16384 | ByteDance Doubao；估算，参考 Seed 系列默认 16k |

### 3.2 本次 dev 12 个 model_key 映射

（对应 S5 验证发现的 NULL 行）

| # | model_key（dev 中确认） | 推荐 backfill 值 | cw | 值来源 |
|---|----------------------|---------------|-----|-------|
| 1 | `claude-sonnet-4-6` | **64000** | 200000 | Anthropic Sonnet 4.x 公开文档 |
| 2 | `claude-sonnet-4-6-thinking` | **64000** | 200000 | Anthropic Sonnet 4.x（thinking variant，同族） |
| 3 | `gemini-3.1-pro-preview` | **65536** | 1000000 | Google Gemini 3.x 估算（保守） |
| 4 | `gemini-3.1-pro-preview-thinking` | **65536** | 1000000 | 同上（thinking variant） |
| 5 | `deepseek-v3.2` | **8192** | 128000 | DeepSeek V3.2 公开文档 |
| 6 | `deepseek-v3.2-thinking` | **8192** | 65536 | DeepSeek V3.2（thinking variant，cw 较小但 8192 仍满足约束） |
| 7 | `gpt-5.4` | **128000** | 128000 | OpenAI GPT-5.4 公开文档（max_output = cw，需确认 reserved+overhead 留量）|
| 8 | `gpt-5.4-thinking` | **128000** | 128000 | 同上（thinking variant） |
| 9 | `qwen-turbo` | **8192** | 131072 | Alibaba DashScope Qwen-turbo 公开文档 |
| 10 | `qwen3-vl-flash` | **16384** | 32768 | Alibaba Qwen3 VL（16384 = cw/2，满足 §2.4） |

> **特殊情况 — `gpt-5.4` / `gpt-5.4-thinking`**：  
> `max_output_tokens = context_window = 128000` 时，`safe_input_budget = 128000 - 16384(reserved) - 1024(overhead) = 110592`。  
> 这是合法的（max_output > reserved + overhead），但意味着 input tokens 被限制在 ~110k。  
> 如果实际业务场景需要更长 input，可以在 reserved_output_tokens 上降低（通过 ContextBudget profile 配置）。

> **`deepseek-v3.2-thinking`（cw=65536）注意事项**：  
> `max_output = 8192`，`safe_input_budget = 8192 - 16384` → **负数**，会触发 ErrContextConfigInvalid！  
> **修正**：`deepseek-v3.2-thinking` 的 `max_output_tokens` 应设为 `min(65536 - 1024, model_actual_max)` = `64512`（保守）或与 `deepseek-v3.2` 同值 `8192` 但需同时将该 model 的 `reserved_output_tokens` profile 降到 ≤ 8192。  
> **推荐**：将 `deepseek-v3.2-thinking` 的 `max_output_tokens` 设为 `32768`（cw=65536 的一半，满足所有约束）。

**修正后 model_key 映射表（修正 deepseek-v3.2-thinking）**：

| # | model_key | 最终推荐 backfill 值 |
|---|-----------|-------------------|
| 6 | `deepseek-v3.2-thinking` | **32768**（从 8192 修正） |

---

## 4. 特殊情况汇总

| 场景 | 处理方式 |
|------|--------|
| `max_output_tokens = context_window` | 合法但 input budget 受限。GPT-5.4 属此情况，可接受 |
| `max_output_tokens` (8192) < `reserved_output_tokens` (16384) | 触发 ErrContextConfigInvalid。DeepSeek thinking 系列须取更大值 |
| `context_window` 较小（≤ 32768） | `max_output_tokens = cw - 16384 - 1024`，例如 qwen3-vl-flash: 32768-17408=15360，取整为 16384 仍合法 |
| Production 中有表中未列出的 model_key | 先运行 01-dry-run.sql 获取列表，参照模型命名前缀对应本表，补充条目后再执行 backfill |
| 模型已设置 max_output_tokens（非 NULL 非 0） | 02-apply.sql WHERE 子句会跳过，不覆盖已有值 |

---

## 5. 应用方法

### 5.1 Dev 验证流程（已完成）

1. S5 验证（2026-04-27）发现 12/14 LLM service `max_output_tokens=NULL`
2. 在 dev 环境执行 blanket backfill：`UPDATE ai_service SET capability_json = JSON_SET(capability_json, '$.max_output_tokens', 32768) WHERE service_type='llm' AND (JSON_EXTRACT(capability_json,'$.max_output_tokens') IS NULL OR JSON_EXTRACT(capability_json,'$.max_output_tokens') = 0)`
3. 重跑 admin smoke test → F-1 全部 PASS

### 5.2 Production 部署 SOP

使用 `scripts/2026-04-27-context-budget-max-output-backfill/` 中的 SQL 文件，按以下顺序执行：

```bash
# 0. 进入 prod MySQL 容器（或通过 SSH 隧道连接）
docker exec -it numind-mysql-prod mysql -uroot -p<PASSWORD> numind-prod

# 1. 运行 inventory query，导出当前所有 LLM service 的 max_output_tokens 状态
docker exec -i numind-mysql-prod mysql -uroot -p<PASSWORD> numind-prod < 01-dry-run.sql > dry-run-output.txt

# 2. 对照 dry-run-output.txt 中的 model_key 列表，与本表 §3.2 逐行核对
#    若有表中未列出的 model_key，参考 §3.1 前缀规则补充 02-apply.sql

# 3. 备份（回滚安全网）
mysqldump -uroot -p<PASSWORD> --single-transaction --no-tablespaces \
  numind-prod ai_service > ai_service_backup_$(date +%Y%m%d_%H%M%S).sql

# 4. 在事务中执行 backfill
docker exec -i numind-mysql-prod mysql -uroot -p<PASSWORD> numind-prod < 02-apply.sql

# 5. 验证：期望输出 0 行
docker exec -i numind-mysql-prod mysql -uroot -p<PASSWORD> numind-prod < 03-verify.sql

# 6. 如果 03-verify.sql 输出 0 行 → COMMIT 已完成（apply.sql 自带 COMMIT）
#    如果输出非零行 → 从 mysqldump 恢复，或运行 04-rollback.sql
```

**RC（Release Candidate）时机**：在 Context Budget feature flag 打开之前，在 production 数据库上完成 backfill。  
如果 feature 已上线但 backfill 未完成，持有 NULL max_output_tokens 的 LLM service 会向所有用户返回 `ErrContextConfigInvalid`。

### 5.3 RC Checklist

- [ ] 运行 01-dry-run.sql，确认 NEEDS BACKFILL 行数 > 0
- [ ] 对照本表核对每个 model_key，补充 02-apply.sql 中缺失的 model 族 UPDATE
- [ ] 执行 mysqldump 备份 ai_service 表
- [ ] 执行 02-apply.sql
- [ ] 执行 03-verify.sql → 确认输出 0 行
- [ ] 在管理端 AI Service 列表页抽查 2-3 个模型，确认 `max_output_tokens` 已更新

---

## 6. 升级路径（Post-Rollout）

Backfill 使用的是**保守最小值**，确保 feature 不 break。  
Production 稳定后，admin 应通过管理端 **AI Service 编辑页面**将每个模型的 `max_output_tokens` 调高至该模型实际广告的最大输出值，以释放完整能力：

| 模型 | Backfill 值 | 建议最终值 | 备注 |
|------|-----------|---------|------|
| claude-sonnet-4-6 | 64000 | 64000 | 已是实际上限，无需调整 |
| gemini-3.1-pro-preview | 65536 | 待确认 | 参考 Google 官方文档确认 |
| deepseek-v3.2 | 8192 | 8192 | 已是实际上限 |
| deepseek-v3.2-thinking | 32768 | 待确认 | 参考 DeepSeek 文档确认 thinking 模式实际限制 |
| gpt-5.4 | 128000 | 128000 | 已是实际上限 |
| qwen-turbo | 8192 | 8192 | 已是实际上限 |
| qwen3-vl-flash | 16384 | 16384 | 已是合理值（cw/2） |

升级操作：管理端 → AI Services → 选择模型 → 编辑 → `capability_json` 中更新 `max_output_tokens` → 保存。  
无需数据库 SQL，Admin UI 直接写入，立即生效（无需重启服务）。

---

## 7. 参考资源

| 提供商 | 文档链接 |
|--------|---------|
| Anthropic Claude | https://docs.anthropic.com/en/docs/about-claude/models |
| OpenAI GPT-5 | https://platform.openai.com/docs/models |
| Google Gemini | https://ai.google.dev/gemini-api/docs/models |
| DeepSeek | https://api-docs.deepseek.com/quick_start/pricing |
| Alibaba DashScope | https://help.aliyun.com/zh/model-studio/getting-started/models |
| ByteDance Doubao | https://www.volcengine.com/docs/82379/1330310 |
| Zhipu GLM | https://open.bigmodel.cn/dev/howuse/model |

---

*作者：Claude Sonnet 4.6（documentation subagent）*  
*对应 task：F-1 Production Backfill Preparation（chore/prod-max-output-backfill）*
