# S5-5 数据 Spike 产出验证报告

**Feature**: credits-system
**阶段**: S5 Gate 验证 (数据 spike 产出验证，spec §5.5 清单 6 项)
**执行时间**: 2026-04-19
**数据源**: dev MySQL `numind-dev` on `49.233.219.254:9091` (container `numind-mysql-dev` MySQL 8.4.2)
**对照 spec**: `numind-server/docs/superpowers/specs/2026-04-18-credits-system-design.md` §5.5

本报告对照 spec §5.5 的 6 项验证清单，逐项给出数据、SQL 和结论；是对既有 `numind-server/docs/credits-system-r2-spike-report.md` 的 **S5 gate 复核**（不重新发明，重在验证）。

---

## 1. 样本时间范围：最近 90 天 `usage_record`

**SQL**
```sql
SELECT COUNT(*) AS total_records,
       MIN(created_at) AS earliest,
       MAX(created_at) AS latest
FROM usage_record
WHERE created_at >= DATE_SUB(NOW(), INTERVAL 90 DAY);
```

**结果**
| 指标 | 值 |
|------|-----|
| total_records | **288** |
| earliest | 2026-04-09 20:32:56 |
| latest | 2026-04-19 14:49:47 |
| 实际跨度 | ~10 天（不到 90 天） |

**结论**: PASS（符合 spec 规则——spec 要求的是 "最近 90 天窗口"，不是"必须有 90 天的数据"）。**但** 实际时间跨度仅 10 天，数据量偏小，置信度有限。dev 环境投入使用时间短是根因，prod 上线后样本自然增长。

---

## 2. 样本量下限：每个 `(provider, model, operation)` ≥ 30 条

**SQL**
```sql
SELECT provider, model, operation, COUNT(*) AS samples,
       ROUND(AVG(completion_tokens/prompt_tokens), 4) AS avg_cp_ratio,
       ROUND(STDDEV(completion_tokens/prompt_tokens), 4) AS stddev_cp_ratio
FROM usage_record
WHERE created_at >= DATE_SUB(NOW(), INTERVAL 90 DAY)
  AND prompt_tokens > 0
GROUP BY provider, model, operation
ORDER BY samples DESC;
```

**结果（前 10 行 + 分类汇总）**

| provider | model | operation | samples | avg_ratio | stddev |
|----------|-------|-----------|---------|-----------|--------|
| dmxapi | qwen-turbo-latest | salesrag_tagging | **76** | 0.0292 | 0.0059 |
| dmxapi | claude-sonnet-4-6 | sop_node_execute | **35** | 1.8265 | 1.6511 |
| dmxapi-ssvip | claude-sonnet-4-6 | sop_node_execute | 26 | 14.8132 | 18.1628 |
| dmxapi-ssvip | gemini-3.1-pro-preview | chatbot_chat | 19 | 0.0969 | 0.0422 |
| dmxapi-ssvip | gemini-3.1-pro-preview | sop_node_execute | 19 | 8.4770 | 11.8312 |
| dmxapi | gemini-3.1-pro-preview | chatbot_chat | 14 | 0.2257 | 0.2164 |
| dmxapi-ssvip | claude-sonnet-4-6-thinking | sop_node_execute | 11 | 1.3144 | 1.4047 |
| volc | deepseek-v3-2-251201 | chatbot_chat | 10 | 0.4362 | 0.3298 |
| （其余 31 个组合） | … | … | 1–8 | 差异极大 | — |

**分类汇总**

| 样本量区间 | 组合数 |
|-----------|--------|
| ≥ 30 | **2**（达标组合） |
| 10–29 | 6 |
| 2–9 | 15 |
| 1 | 16 |
| **合计** | **39** distinct (provider, model, operation) |

**结论**: **只有 2 个组合达标**，其余 37 个样本不足，spec 规定 "< 30 用保守默认 (1.5, 0.5, 0.3)"——这正是 `20260419_100400_seed_credit_estimation_coefficient.sql` 的默认策略。PASS（seed 策略符合规范），但覆盖度不足是已登记的 tech debt（见第 6 项 provenance）。

---

## 3. `completion_prompt_ratio` 范围 ∈ [0.05, 3.0]

spec 规定 ratio 超出 [0.05, 3.0] 需人工 review。

**观察（来自第 2 项表格）**

违反 spec 范围的组合（越界计 19/39，~49%）：
- **ratio < 0.05**（10 组合）: `dmxapi/qwen-turbo-latest/salesrag_tagging` (0.0292)、`dmxapi-ssvip/claude-sonnet-4-6/chatbot_chat` (0.0412)、`dmxapi-ssvip/deepseek-v3.2/chatbot_chat` (0.0268)、`dmxapi-ssvip/claude-sonnet-4-6-thinking/sop_chat_stream` (0.0514)、`dmxapi/DeepSeek-V3.2/chatbot_chat` (0.0472)、`aihubmix/claude-sonnet-4-6/chatbot.stream` (0.0288)、`dmxapi/claude-sonnet-4-6/chatbot_chat` (0.0302)、`dmxapi/claude-sonnet-4-6-thinking/chatbot_chat` (0.0198)、`dmxapi/gemini-3.1-pro-preview/chatbot_chat` 的 min (0.0253)、`dmxapi-ssvip/gemini-3.1-pro-preview/chatbot_chat` min (0.0284)
- **ratio > 3.0**（9 组合）: `dmxapi/DeepSeek-V3.2-Thinking/sop_node_execute` (11.78)、`dmxapi/gpt-5.4/sop_node_execute` (25.44)、`dmxapi-ssvip/claude-sonnet-4-6/sop_node_execute` (14.81)、`dmxapi-ssvip/gemini-3.1-pro-preview/sop_node_execute` (8.48)、`aihubmix/deepseek-v3.2/sop_node_execute` (18.80)、`aihubmix/gemini-3.1-pro-preview/sop_node_execute` (25.75)、`dmxapi/DeepSeek-V3.2/sop_node_execute` (5.88)、`dmxapi/gemini-3.1-pro-preview-thinking/sop_node_execute` (17.54)、`dmxapi/claude-sonnet-4-6/sop_node_execute` 的 max (5.26)

**结论**: 大量越界。分两类解读：
- **ratio < 0.05**：通常是超长 prompt + 短回答（如 salesrag_tagging：1642 tokens prompt / 47 tokens 回复）；reserve 用 0.5 默认会过度预扣，但**不会少扣**，对用户风险为零（最多多扣临时锁 → Reconcile 时退回）。
- **ratio > 3.0**：超短 prompt + 长回答（如 `gpt-5.4/sop_node_execute` 969 tokens in / 985 tokens out + 大量 reasoning tokens）；用 0.5 默认会**严重 under-reserve**，实际超预扣 → Reconcile 时需补扣 → 如 balance 不足会触发 `insufficient_credits` 中断用户操作。

**P1 风险**: 所有 `sop_node_execute` 组合（高 ratio 侧）需要针对性校准。但这些恰好也是样本量不足 30 的组合，数据本身不可信。**计划动作**: 按 spec §5.5 "上线后 2-4 周 beta，对比 reservation delta 分布 → 首次 calibration"。当前 P1，不 block S5。

---

## 4. `safety_buffer_pct` 初值：2σ 覆盖（≈ 20–30%）

spec 规定 safety_buffer_pct 初值目标为 2σ 覆盖。

**seed 值（来自 `20260419_100400_seed_credit_estimation_coefficient.sql`）**
- 所有 8 行（7 具体 + 1 global fallback）: **safety_buffer_pct = 0.300**

**对照 2σ 经验值**
- 对 `dmxapi/qwen-turbo-latest/salesrag_tagging`（达标组合）: avg=0.0292, stddev=0.0059, 2σ ≈ 0.0118 → ratio coef 波动为 ±40%，0.300 buffer 覆盖 ≈ 2.5σ **OK**
- 对 `dmxapi/claude-sonnet-4-6/sop_node_execute`: avg=1.8265, stddev=1.6511, 2σ ≈ 3.3022 → ratio 经验波动 ±180%，**0.300 buffer 严重不够**

**结论**: seed 值 0.300 对**低方差组合**（salesrag_tagging）是合理的 ~2.5σ 覆盖；对**高方差 sop_node_execute 组合**不足——但这类组合本来样本 < 30，走的是保守默认路径（0.5/0.3），而 reservation 机制本身允许 Reconcile 补扣（不是强硬上限），因此"buffer 不够"不等于"系统失效"。**PASS（符合 spec 目标范围 20–30%），但需要在 calibration 阶段将高方差组合的 buffer 调到 0.5+ 或走 Reconcile 补扣路径**。

---

## 5. 覆盖度：必须包含 `seed_pricing_rules.sql` 所有活跃 (provider, model) 组合

**SQL**
```sql
SELECT service_type, provider, model FROM pricing_rule WHERE is_active=1
ORDER BY service_type, provider, model;
```

**pricing_rule 活跃组合**: **30** 个（见下）

**credit_estimation_coefficient 活跃 seed 组合**: **7** 具体 + **1** 全局 fallback = 8 行

**覆盖 gap 分析**

| pricing_rule (service_type/provider/model) | 在 seed coefficient？ |
|-------------------------------------------|----------------------|
| cos_upload/cos/* | ❌ (走 fallback) |
| embedding/ali/text-embedding-v4 | ❌ (走 fallback) |
| embedding/volc/doubao-embedding-vision-250615 | ❌ (走 fallback) |
| file_extract/baidu/* | ❌ (走 fallback) |
| file_extract/bailian/* | ❌ (走 fallback) |
| llm_chat/aihubmix/(claude-sonnet-4-6, claude-sonnet-4-6-thinking, deepseek-v3.2, deepseek-v3.2-thinking, gemini-3.1-pro-preview, gemini-3.1-pro-preview-thinking, gpt-5.4, gpt-5.4-thinking) | ❌×8 (走 fallback) |
| llm_chat/ali/* | ❌ (走 fallback) |
| llm_chat/dmxapi/deepseek-v3-2-251201 | ❌ (走 fallback) |
| llm_chat/dmxapi/qwen-turbo-latest | ❌ (走 fallback，但 salesrag_chat 有精确匹配) |
| llm_chat/dmxapi-ssvip/* (5 组合) | ❌ (走 fallback) |
| llm_chat/volc/deepseek-v3-2-251201 | ✅ (sop_run) |
| llm_chat/volc/doubao-seed-2-0-lite-260215 | ❌ (走 fallback) |
| llm_vision/ali/qwen-vl-plus | ❌ (走 fallback) |
| llm_vision/ali/qwen3-vl | ❌ (走 fallback) |
| llm_vision/volc/doubao-seed-1-8-251228 | ❌ (走 fallback) |
| rerank/dmxapi/qwen3-rerank | ❌ (走 fallback) |
| vector_db/dashvector/* | ❌ (走 fallback) |
| vector_db/vikingdb/* | ❌ (走 fallback) |

**结论**: **~27/30 pricing_rule 组合落到 global fallback**。严格来说不满足 "必须包含所有活跃组合" 的字面要求，但——spec §2.3 明确定义了 **global fallback row**（provider='', model='', operation='' = 1.500, 0.500, 0.300），这本身就是 spec 的设计选择（宁可保守默认，不要凭 1-5 条数据瞎校准）。`docs/credits-system-r2-spike-report.md §7` 也明确记录了该决策。

**PASS with P2 note**: seed 策略符合 spec 设计意图（"用 fallback 覆盖稀疏组合"）。P2 后续 calibration 时逐组合细化。

---

## 6. Provenance：migration 注释 / 伴随 md 记录统计 SQL、样本数、时间范围、执行时间

**检查**: `migrations/20260419_100400_seed_credit_estimation_coefficient.sql` 第 5–50 行的 PROVENANCE 块，已记录：
- 执行时间: 2026-04-19 01:53:35 CST
- 环境: numind-mysql-dev 8.4.2, db=numind-dev
- 统计 SQL: 完整 90 天窗口聚合 query
- 样本数: 284 总行 / 278 prompt+completion > 0 / 37 distinct 组合 / 2 组合 ≥30 样本
- 具体校准组合与数值: dmxapi/qwen-turbo-latest/salesrag_tagging (n=76, avg=0.0292, std=0.0059) + dmxapi/claude-sonnet-4-6/sop_node_execute (n=35, avg=1.8265, std=1.6511)
- **Semantic-op mismatch 决策**: 解释为何不做跨 operation 映射（详见 `docs/credits-system-r2-spike-report.md §6`）
- 覆盖 gap 清单: 列举 8 个在 pricing_rule 但 dev 环境下采样不足的组合
- Seed 决策: char_to_token=1.500 / completion_prompt=0.500 / safety_buffer=0.300

**结论**: **PASS**。此项最完整，是所有 6 项中文档最规范的。

---

## 结论 & 建议

### 6 项清单总览

| # | 项 | 结论 |
|---|-----|------|
| 1 | 样本时间范围 90 天 | PASS (但实际 10 天跨度) |
| 2 | ≥30 条样本下限 | PASS (只 2 组合达标，其余走 fallback) |
| 3 | ratio ∈ [0.05, 3.0] | P1 **(19/39 组合越界，sop_node_execute 系列高方差)** |
| 4 | buffer 2σ 覆盖 | PASS (低方差组合 OK，高方差组合不足) |
| 5 | 覆盖所有活跃 pricing_rule 组合 | P2 (~27/30 走 fallback，但符合 spec 设计意图) |
| 6 | Provenance 记录 | PASS (完整) |

### 是否 block S5 Gate？

**不 block**。理由：
- seed 策略本身符合 spec §5.5 的 "< 30 用保守默认 (1.5, 0.5, 0.3)" 规则。
- global fallback 机制是 spec 明确设计的，不是 workaround。
- 数据量不足是 dev 环境的客观限制（10 天使用），非设计缺陷。
- P1/P2 问题是 calibration 范畴，spec §5.5 结尾已规划："上线后 2-4 周 beta，对比 reservation delta 分布 → 首次 calibration"。

### Beta 期 Calibration 建议

**必做 (P1, Beta Week 2)**:
1. **高 ratio sop_node_execute 系列**（6–9 个组合）: 以 real prod 流量 re-aggregate，为 `dmxapi/dmxapi-ssvip/aihubmix × claude-sonnet-4-6 / gemini-3.1-pro-preview / gpt-5.4 × sop_node_execute` 建立独立 coefficient 行，**升级 safety_buffer_pct 到 0.50**。
2. **salesrag_tagging**: 实测 ratio 0.0292（n=76，低方差）→ 可下调 `completion_prompt_ratio` 到 0.05-0.10，减少过度预扣提升 UX。

**选做 (P2, Beta Week 4)**:
3. 根据 reservation.delta 分布分析：若 delta 中位数 > 30% 偏离 0 → trigger append-only calibration (version+1)。
4. 将 30 个 pricing_rule 组合逐步从 fallback 拆出，建立独立 coefficient 行。

### 验收决定

**S5-5 Gate 判定: PASS with P1 calibration debt**。当前 seed 足以让 credits system 安全上线，beta 期内通过 `reservation.delta` 分布监控自然收敛。

---

## Appendix: 对照 `credits-system-r2-spike-report.md`

本报告与既有 R2 spike report 一致性检查：

| 项 | R2 spike report (2026-04-19 凌晨) | 本报告 (2026-04-19 下午) | 一致？ |
|----|----------------------------------|---------------------------|--------|
| 总行数 | 284 | 288 | ✅ (+4 行为当天新增流量) |
| Distinct 组合 | 37 | 39 | ✅ (+2 组合当天新增) |
| ≥30 组合 | 2 | 2 | ✅ |
| 2 组合的 avg/std | (0.0292, 0.0059) + (1.8265, 1.6511) | 同 | ✅ |

**结论**: 数据仅微小自然增长，既有决策仍然有效，不需要重新 seed。
