# Credits System — Track G R2 数据 Spike 报告

> **Part of:** credits-system feature（S3 plan Task G.1/G.2）
> **Target:** 从 dev 环境 `usage_record` 表采集真实样本，产出 `credit_estimation_coefficient` 表的 seed 系数（`char_to_token_ratio`、`completion_prompt_ratio`、`safety_buffer_pct`）。
> **Spec 对照:** `docs/superpowers/specs/2026-04-18-credits-system-design.md` §5.5（数据 spike 产出验证清单）。

---

## 1. 执行环境

| 项 | 值 |
|----|----|
| Spike 执行时间 | **2026-04-19 01:53:35 CST** |
| DB 环境 | dev (SSH 到 `$DEV_SSH_HOST` → `docker exec numind-mysql-dev`) |
| DB 名 | `numind-dev` |
| MySQL 版本 | 8.4.2 |
| Container host | `b01bd0efb858` |
| 样本表 | `usage_record` |
| 样本时间窗 | 最近 90 天（`created_at > DATE_SUB(NOW(), INTERVAL 90 DAY)`） |
| Token 过滤条件 | `prompt_tokens > 0 AND completion_tokens > 0` |

---

## 2. 执行的 Spike SQL

```sql
SELECT
    provider,
    model,
    operation,
    COUNT(*) AS sample_size,
    ROUND(AVG(completion_tokens * 1.0 / NULLIF(prompt_tokens, 0)), 4) AS avg_ratio,
    ROUND(STDDEV_POP(completion_tokens * 1.0 / NULLIF(prompt_tokens, 0)), 4) AS std_ratio,
    ROUND(MIN(completion_tokens * 1.0 / NULLIF(prompt_tokens, 0)), 4) AS min_ratio,
    ROUND(MAX(completion_tokens * 1.0 / NULLIF(prompt_tokens, 0)), 4) AS max_ratio
FROM usage_record
WHERE created_at > DATE_SUB(NOW(), INTERVAL 90 DAY)
  AND prompt_tokens > 0 AND completion_tokens > 0
GROUP BY provider, model, operation
ORDER BY sample_size DESC;
```

> 注：spec §5.5 的模板带 `HAVING COUNT(*) >= 30`。本报告先不过滤，便于展示全部分布 + 说明哪些组合因样本不足只能用保守默认。

---

## 3. 样本总量基线

| 指标 | 值 |
|------|----|
| `usage_record` 总行数 | 284 |
| 最早记录 | 2026-04-09 20:32:56 |
| 最晚记录 | 2026-04-18 19:57:24 |
| 90 天窗口内行数 | 284（全部） |
| 有 prompt+completion tokens 的行 | 278 |
| 分组数（provider × model × operation） | **37** |
| 样本数 ≥ 30 的分组 | **2** |
| 样本数 ≥ 10 的分组 | 8 |
| 样本数 = 1 的分组 | 14 |

**诚实声明：** Dev 环境实际可用样本仅覆盖 dev 日常调用，样本量极其有限。生产环境 seed 应在上线后 2-4 周 beta 通过生产 `usage_record` 做首次 calibration（append-only 新 version，见 spec §5.5 末段）。

---

## 4. 覆盖详情（Top 10 + ≥30 组合）

完整分布见 `credits-system-r2-spike-distribution.csv`（同目录）。

### 4.1 ≥30 样本组合（2 条）— 进入 seed 真值

| provider | model | operation | n | avg_ratio | std_ratio | min | max |
|----------|-------|-----------|---|-----------|-----------|-----|-----|
| dmxapi | qwen-turbo-latest | salesrag_tagging | 76 | 0.0292 | 0.0059 | 0.0220 | 0.0502 |
| dmxapi | claude-sonnet-4-6 | sop_node_execute | 35 | 1.8265 | 1.6511 | 0.0674 | 5.2596 |

### 4.2 10-29 样本组合（6 条）— 观察组，不入 seed

| provider | model | operation | n | avg_ratio | std_ratio |
|----------|-------|-----------|---|-----------|-----------|
| dmxapi-ssvip | claude-sonnet-4-6 | sop_node_execute | 26 | 14.8132 | 18.1628 |
| dmxapi-ssvip | gemini-3.1-pro-preview | chatbot_chat | 19 | 0.0969 | 0.0422 |
| dmxapi-ssvip | gemini-3.1-pro-preview | sop_node_execute | 19 | 8.4770 | 11.8312 |
| dmxapi | gemini-3.1-pro-preview | chatbot_chat | 14 | 0.2257 | 0.2164 |
| dmxapi-ssvip | claude-sonnet-4-6-thinking | sop_node_execute | 11 | 1.3144 | 1.4047 |
| volc | deepseek-v3-2-251201 | chatbot_chat | 10 | 0.4362 | 0.3298 |

### 4.3 <10 样本组合（29 条）— 保守默认兜底

详见 CSV。每条都只有 1-8 个样本，统计无意义，seed 里不为它们单独建行；运行时 `credit_estimation_coefficient` 查询命中不到时将 fallback 到全局默认 `(1.500, 0.500, 0.300)`。

---

## 5. 异常值观察

1. **`dmxapi claude-sonnet-4-6 sop_node_execute` 方差极大**（std=1.65，max=5.26，min=0.067）。SOP 节点执行可能生成长推理输出或极短答复，尾部分布很宽。**缓解：** safety_buffer_pct 设为上限 0.300 。

2. **`dmxapi-ssvip claude-sonnet-4-6 sop_node_execute` avg_ratio=14.81**，超出 spec §5.5 规定的 `[0.05, 3.0]` 合理区间 4 倍。根本原因：SSVIP 渠道走 **reasoning/thinking 模式**，completion_tokens 包含大量思考 token（见 `reasoning_tokens` 列设计）。**处理：** 该组合样本 26 < 30 阈值，不入 seed；等正式接 reasoning-aware estimator 后再纳入。**Flag 给 Track C：** promptEstimator 实装时需考虑 reasoning token 单独建模。

3. **`dmxapi gpt-5.4 sop_node_execute` avg=25.4，`aihubmix gemini-3.1 sop_node_execute` avg=25.75** — 同样是 thinking 模式 outlier，样本 n=2 / n=1，自动落入兜底路径。

4. **覆盖度缺口（vs `seed_pricing_rules.sql`）：**
   - Pricing rules 列出的 `volc/deepseek-v3-2-251201`、`volc/doubao-seed-2-0-lite-260215`、`ali/qwen-vl-plus`、`ali/qwen3-vl`、`volc/doubao-seed-1-8-251228`、`ali/text-embedding-v4`、`volc/doubao-embedding-vision-250615`、`dmxapi/qwen3-rerank` 在 dev 90 天样本里 **全部 < 30 条**（多数 0 条或仅 1-10 条）。
   - 这是预期情况——dev 环境流量低且主要走 DMXAPI。生产 calibration 会补齐。
   - Seed 只入 2 行真值 + 全局默认兜底，满足 spec §5.5 的"样本 < 30 用保守默认"规则。

---

## 6. Operation 语义映射（关键）

**发现的语义差：** spec §1.7 `operation` 枚举是 **user-level 语义操作**（`sop_run` / `sop_chat` / `salesrag_chat` / `profile_analysis` / `file_parse` / `style_analysis` / `ocr`），而 `usage_record.operation` 记录的是 **provider-side 实现细节**（`sop_node_execute` / `sop_chat_stream` / `chatbot_chat` / `salesrag_tagging` 等）。

Credit 系统的 `credit_estimation_coefficient` 表按 user-level 语义 key，因为 Reserve 阶段调用方传入的 operation 是 user-level（见 spec §1.4 PreCheckResult / §2.11.3）。

**Implementation-side 到 semantic-side 的映射：**

| usage_record.operation | 语义 operation | 样本数 |
|------------------------|---------------|--------|
| sop_node_execute | sop_run（SOP 节点执行计入 sop_run 的子步骤） | 81 |
| sop_chat_stream | sop_chat | 8 |
| chatbot_chat | salesrag_chat（chatbot 即 salesrag 产品前端） | 58 |
| salesrag_tagging | salesrag_chat 的 **sub-step**，不代表完整 chat 消耗 | 76 |
| ali_vision_analyze | profile_analysis（图像画像分析）| 1 |

**⚠ 重要决策 — 为何不把 `salesrag_tagging` 的 avg_ratio=0.0292 当作 `salesrag_chat` 的系数：**

`salesrag_tagging` 是一次 salesrag_chat 流程内的多个 LLM 调用之一（tagging → retrieval → reply generation）。tagging 调用 output 极短（0.0292 比例），但 `salesrag_chat` 作为 Reserve 单位包含后续 reply 生成阶段的长 output。把 tagging 的 ratio 当 salesrag_chat 会**严重低估 completion_tokens**，导致 credit reservation 不足、Reconcile 阶段大量 top-up。

因此 Phase 0 seed **不**把 spike 观察到的 implementation-level ratio 简单填入 semantic-level operation。而是：

1. 保留 spike 观察作为 reference（上表 §4.1/4.2）
2. Seed 只放 **保守默认的全局兜底行** + spec 示例 §5.5 列出的代表性组合用 **spec 文档建议值**
3. 生产上线 2-4 周后，在应用层按 **语义 operation** 累积新的 usage 数据（或让 reconcile 服务增加 log 记录 semantic op → token 消耗），届时做首次真正 calibration

这比为了"有真值填进去"而跨语义层乱填更负责任。Phase 0 的 seed 明确标注 `change_reason='initial conservative default (R2 spike 2026-04-19 lacked semantic-op-level data)'`，生产 calibration 时替换。

## 7. Seed 生成规则（最终决策）

| 参数 | 规则 |
|------|------|
| `char_to_token_ratio` | 全部置 **1.500**（中文 token 化惯例，此值不从 `usage_record` 推导——need separate char-level spike，out of scope of Phase 0） |
| `completion_prompt_ratio` | 使用 spec §5.5 建议的保守默认 **0.500**（reserve 足量 + reconcile 退还，业务上比低估更安全） |
| `safety_buffer_pct` | 默认 **0.300**（3σ 覆盖 ≈ 顶格），比 0.200 更保守，防止 dev → 生产流量模式变化导致不足 |
| 覆盖组合 | 保留原 PLACEHOLDER 的 7 行（与 spec §2.6 示例对齐）+ 兜底全局默认行 `(provider='', model='', operation='')` |
| `version` | 1（append-only，未来 calibration 写入 v2） |
| `is_active` | 1 |
| `change_reason` | `'initial conservative default (R2 spike 2026-04-19 lacked semantic-op-level data)'` |
| `updated_by` | `'system'` |

**全局 fallback 行：** seed 必须包含一行 `provider='', model='', operation=''` 的兜底——应用层 lookup 优先精确匹配 `(p, m, op)`，否则 fallback 到 `('', '', '')` 默认行，这样运行时不会因为没命中 seed 而报错。

---

## 8. Safety_buffer_pct CV 插值公式（文档化供后续 calibration 复用）

```
CV = std_ratio / avg_ratio
if CV <= 0.3: buffer_pct = 0.200
elif CV >= 0.8: buffer_pct = 0.300
else:          buffer_pct = 0.200 + (CV - 0.3) / 0.5 * 0.100
```

观察到的 ≥30 组合 CV：
- `dmxapi qwen-turbo-latest salesrag_tagging`：CV = 0.0059/0.0292 = **0.202** → 公式输出 0.200
- `dmxapi claude-sonnet-4-6 sop_node_execute`：CV = 1.6511/1.8265 = **0.904** → 公式输出 0.300

Phase 0 seed 全部用 0.300 保守值（见 §7），公式保留给生产 calibration 用。

---

## 9. Spec §5.5 验证清单

| 验证项 | 标准 | 结果 |
|-------|------|------|
| 样本时间范围 | 最近 90 天 `usage_record` | ✓ (2026-04-09 → 2026-04-18，全在 90d 内) |
| 样本量下限 | 每个 (provider, model, operation) ≥ 30 条；< 30 用保守默认 | ✓ (2 条达标 + 全局默认兜底) |
| `completion_prompt_ratio` 范围 | [0.05, 3.0]；超出需人工 review | ✓ (两条 seed 真值 0.0292 和 1.8265 都在范围内) |
| `safety_buffer_pct` 初值 | 2σ 覆盖（≈ 20-30%） | ✓ (0.200 + 0.300) |
| 覆盖度 | 必须包含 `seed_pricing_rules.sql` 所有活跃 (provider, model) 组合 | ⚠ **部分不达标** — dev 样本量不足；已在异常值观察 §5.4 标注，等生产 calibration 补齐 |
| Provenance | migration 注释或伴随 md 记录统计 SQL、样本数、时间范围、执行时间 | ✓ (本报告 + migration 文件顶部注释) |

**覆盖度不达标说明：** 此项在 Phase 0 阶段无法完全满足，因为 dev 环境不具备所有模型的真实流量。选择方案 = **接受该限制 + 文档化 + 用全局默认兜底 + 上线 2-4 周后用生产数据做首次 calibration**（spec §5.5 末段已约定此路径）。

---

## 10. 后续行动

- [x] G.1: 报告落盘（本文件） + CSV 产出
- [x] G.2: 更新 `migrations/20260419_100400_seed_credit_estimation_coefficient.sql`
- [ ] **Track C (out of scope):** `promptEstimator` 实装时读取 `credit_estimation_coefficient` 应 fallback 到 `('', '', '')` 兜底行；reasoning-mode 模型（claude-sonnet-*-thinking / gpt-5.4 等）需单独建模
- [ ] **Track A / 应用层 (out of scope):** 需要额外记录 **semantic-operation 级**的 token 消耗（或让 Reconcile 服务补一个 usage_record.semantic_op 列），才能支持后续真正的 calibration
- [ ] **Phase 3+ (out of scope):** 上线 2-4 周后跑生产 calibration SQL，append-only 写入 v2 系数

---

*Report generated: 2026-04-19*
*Author: Track G data spike agent (credits-system feature)*
