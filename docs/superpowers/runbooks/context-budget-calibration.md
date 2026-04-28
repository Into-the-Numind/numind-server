# Context Budget — Token 估算调参 Runbook

> 本 runbook 用于 `context-budget-compression` feature 上线后的**周期性调参**。
> 配套 manifest 条目：`context-budget-compression`（`build-manifest.yaml`）
> 配套 spec：`docs/superpowers/specs/2026-04-25-context-budget-compression-design.md`（§4.3 精度目标 + §6.4 校准两层结构）
>
> **使用方式**：用户随时一句话触发（"跑一下 context-budget 调参"），AI 按本文档执行。**每个步骤都需要用户确认才能改动 prod**。

---

## §1 用途 (Why)

Token 估算系数（`profile_json.classes` 和 `calibration_multiplier`）需要根据 prod 真实流量周期性校准，把估算偏差从初始的 ±30%（保守兜底）逐步收敛到 spec §4.3 目标：

- P50（中位数）相对偏差 ≤ 5%
- P90 ≤ 10%
- P99 ≤ 20%

**调参不修代码、不改 schema**——只通过 admin API 创建 `token_estimation_profile` 新版本，旧版自动失活。在飞 reservation 因 active-version invariant 不受影响。

---

## §2 触发时机 (When)

| 阶段 | 频率 | 说明 |
|---|---|---|
| 上线后 0-14 天 | **不调** | 数据积累期，让兜底 `safety_multiplier=1.30` 工作 |
| 上线后 14 天 | 第 1 次调 | 第一次跑统计，调最离谱的 (provider, model) 桶 |
| 上线后 14-60 天 | 月度 | 每月跑一次，迭代收敛 |
| 上线后 60 天+ | 季度 | 数据稳定，低频维护 |
| 触发性事件 | 立即调 | 接入新模型 / 厂商升级 tokenizer / 客户投诉扣费偏差 |

**判断"稳定"的标准**：连续两轮调参后，所有活跃 (provider, model) 桶的 P50 都在 ±5% 以内。

---

## §3 前置条件 (Prerequisites)

调参前必须确认：

1. **R3 已在 prod 部署**（schema 含 `context_budget_event` + `token_estimation_profile` 表）
2. `context_budget_event` 表上一调参周期（默认 30 天）至少有 **1000 行**`status='ok'` 的数据，且每个待调 (provider, model) 桶至少 **30 行**
3. 没有正在进行的 prod 变更窗口（避免 active version 切换跟其他 schema 改动冲突）
4. SSH 凭据可用（`$PROD_SSH_HOST` / `$PROD_SSH_USER` / `$PROD_SSH_PASS`）
5. Admin token 可用（用 `$E2E_USERNAME` + `$E2E_PASSWORD` 登录 prod admin）

---

## §4 调参流程 (Procedure)

### Step 1 — 拉取偏差数据（AI 自动）

SSH 进 prod DB，跑下面 SQL 拿到上一周期偏差分布：

```sql
-- 按 (provider, model, operation) 聚合上 30 天偏差
SELECT
    provider,
    model,
    operation,
    COUNT(*) AS sample_count,
    ROUND(AVG(calibration_ratio), 4) AS mean_ratio,
    ROUND(SUBSTRING_INDEX(SUBSTRING_INDEX(GROUP_CONCAT(calibration_ratio ORDER BY calibration_ratio), ',', 50/100 * COUNT(*) + 1), ',', -1), 4) AS p50,
    ROUND(SUBSTRING_INDEX(SUBSTRING_INDEX(GROUP_CONCAT(calibration_ratio ORDER BY calibration_ratio), ',', 90/100 * COUNT(*) + 1), ',', -1), 4) AS p90,
    ROUND(SUBSTRING_INDEX(SUBSTRING_INDEX(GROUP_CONCAT(calibration_ratio ORDER BY calibration_ratio), ',', 99/100 * COUNT(*) + 1), ',', -1), 4) AS p99
FROM context_budget_event
WHERE created_at >= NOW() - INTERVAL 30 DAY
  AND status = 'ok'
  AND calibration_ratio IS NOT NULL
  AND calibration_ratio > 0
GROUP BY provider, model, operation
HAVING sample_count >= 30
ORDER BY ABS(p50 - 1.0) DESC;
```

**注意**：MySQL 8 的 `PERCENTILE_CONT` 在某些版本不可用，上面用 `GROUP_CONCAT + SUBSTRING_INDEX` 兜底。如果数据量超过 `group_concat_max_len`（默认 1024），换成 CTE + ROW_NUMBER 写法。

### Step 2 — 当前生效系数对照（AI 自动）

```sql
SELECT
    id, provider, model, model_family, service_type,
    safety_multiplier, calibration_multiplier,
    calibration_sample_count, calibration_p50_abs_error, calibration_p90_abs_error,
    version, change_reason
FROM token_estimation_profile
WHERE is_active = 1
ORDER BY provider, model;
```

### Step 3 — 决策准则 (Calibration Heuristics)

按下面的规则给每个 (provider, model) 桶**分类**，输出建议表：

| P50 偏离 1.0 | 样本量 | 动作 | 怎么调 |
|---|---|---|---|
| ≤ 5% | 任意 | **不调** | 已达 spec 目标 |
| 5-15% | ≥ 30 | **轻度调** | 把 `calibration_multiplier` 改为当前值 × 当前 P50 |
| 15-30% | ≥ 100 | **中度调** | 同上，但额外检查 P90/P99 看是否有 long tail |
| > 30% | ≥ 100 | **大幅调** | 同上，且警告"可能字符类系数也偏，下轮考虑调 profile_json.classes" |
| 任意 | < 30 | **不调** | 样本不足，等积累 |

**安全约束**：
- 单次调参最多动 **5 个桶**（避免一次性大改导致预扣异常）
- `calibration_multiplier` 单次调整幅度不超过 **±50%**（比如当前 1.0，新值不能超过 [0.5, 1.5] 区间）
- 不要把 `safety_multiplier` 调到 < 1.0（永远要保留安全余量）
- 不要直接改 `profile_json.classes` 字符类系数——先用 `calibration_multiplier` 微调；只有连续 2-3 轮 multiplier 都收敛在同一方向，才考虑调 classes（这是高级模式，下面 §6 说明）

### Step 4 — 出建议报告，等用户拍板（AI 报告 → 用户确认）

格式：

```
=== Context Budget 调参建议 (2026-MM-DD) ===

数据期：2026-MM-DD ~ 2026-MM-DD（30 天）
总样本：X 条 events，Y 个有效 (provider, model, operation) 桶

【建议调】3 桶
1. ali-dashscope / qwen-turbo / sop_run
   - 样本量: 1234
   - 当前 calibration_multiplier: 1.00
   - P50: 1.18 (高估 18%)，P90: 1.32，P99: 1.61
   - 建议新值: 1.18
   - 原因: 中文场景估算偏低，actual_prompt 比 estimated_before 多 18%

2. ...

【不调】8 桶（P50 偏差 ≤5%，已达标）
- volc-ark / glm-4-7-251222 / sop_run (P50=1.02)
- ...

【样本不足】2 桶（<30）
- aihubmix / claude-sonnet-4-7 / chatbot_chat (n=12)
- ...

请确认要调哪几个？(全部确认 / 选择性确认 / 不调任何 / 调整建议值)
```

**严格要求**：AI 必须等用户明确回复后才能进入 Step 5。**禁止"按建议自动执行"**。

### Step 5 — 通过 Admin API 创建新 profile 版本（用户确认后）

每个待调桶执行一次 `POST /v1/admin/context-budget/token-profiles`：

```bash
curl -sS -X POST "https://youshu.asia/v1/admin/context-budget/token-profiles" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "ali-dashscope",
    "model": "qwen-turbo",
    "model_family": "qwen",
    "service_type": "llm_chat",
    "profile_json": <复制旧版的 profile_json，不动>,
    "safety_multiplier": <复制旧版的，不动>,
    "calibration_multiplier": <新值>,
    "change_reason": "2026-MM-DD calibration: P50=1.18 over 1234 samples, multiplier 1.00→1.18"
  }'
```

**重要**：
- `profile_json` **完整复制旧版本**（包括 classes、overheads、method 等所有字段），只是放在新版里
- `safety_multiplier` 不动（除非有充分理由调整）
- `calibration_multiplier` = 新值
- `change_reason` 写明"调参原因 + 数据期 + 样本量 + 调整幅度"
- 新 version 创建后旧 version 自动 `is_active=0`

### Step 6 — 验证 + 记录 (AI 自动)

```sql
-- 验证新版本生效
SELECT id, provider, model, version, is_active, calibration_multiplier, change_reason, created_at
FROM token_estimation_profile
WHERE provider = '<provider>' AND model = '<model>'
ORDER BY version DESC LIMIT 3;
```

预期：最新版本 `is_active=1`，前一版本 `is_active=0`。

把这次调参动作记到 `build-manifest.yaml` 的 `context-budget-compression` 条目 `decisions` 数组：

```yaml
- "2026-MM-DD (calibration round N): 调整 X 个 (provider, model) 桶。详见 docs/superpowers/runbooks/calibration-history/2026-MM-DD.md。下次建议 30 天后跑。"
```

并新建一份 `docs/superpowers/runbooks/calibration-history/YYYY-MM-DD.md` 记录详情（数据期、调了哪些桶、为啥调、新旧值对比）。

### Step 7 — 部分回滚机制（如果调坏）

如果新版本上线后发现某个桶估算变差（calibration_ratio 反而离 1.0 更远）：

```bash
# 重新激活上一版本
curl -sS -X POST "https://youshu.asia/v1/admin/context-budget/token-profiles/{old_version_id}/activate" \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

**注意**：当前路由可能没暴露"重新激活"端点，需要直接用 SQL 改 `is_active`：
```sql
UPDATE token_estimation_profile SET is_active=0 WHERE id=<new_bad_id>;
UPDATE token_estimation_profile SET is_active=1 WHERE id=<old_good_id>;
```
SQL 层有 unique constraint 保证只能一个 active，需要在事务里完成。

---

## §5 报告归档约定

- **每次调参产出一份归档报告**：`docs/superpowers/runbooks/calibration-history/YYYY-MM-DD.md`
- 包含：数据期、SQL 查询结果（截图或 markdown 表）、建议表、用户决策记录、最终执行的 API 调用、Step 6 验证截图
- 归档报告**进 git**（不是临时文件），未来回看演变路径

---

## §6 高级模式：调字符类系数 (Advanced)

**警告**：只在连续 2-3 轮 `calibration_multiplier` 调参后**仍**偏离 1.0 同一方向时使用。需要用 prompt 文本做字符分类，对 AI 操作复杂度更高。

简化版：
1. 拉一批 event + 关联 usage_record / chatbot_message 拿原始 prompt 文本
2. 对每条 prompt 做字符分类统计（zh / en / code / json / symbol / mixed 各占多少字符）
3. 用线性回归或简单加权平均，反算每个 class 的真实 `token_per_char`
4. 新建 profile，把 `profile_json.classes.<class>.token_per_char` 改成新值
5. 同时把 `calibration_multiplier` 重置为 1.0

详细方法和 SQL 模板待第一次进入高级模式时补充本节。

---

## §7 Schema 速查 (Reference)

### context_budget_event 关键字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `provider`, `model`, `operation` | varchar | 调参分桶维度 |
| `estimated_before` | int | 调用前估算的 prompt tokens |
| `actual_prompt_tokens` | int | LLM 返回的真实 prompt tokens |
| `calibration_ratio` | decimal(10,4) | actual / estimated（spec §6.4 比值，调参核心信号） |
| `status` | varchar | 'ok' / 'failed' / 'compressed' 等，分析时只看 'ok' |
| `created_at` | datetime | 时间窗口分桶用 |

### token_estimation_profile 关键字段

| 字段 | 调参时怎么用 |
|---|---|
| `safety_multiplier` | **不调**（默认 1.15-1.30，安全兜底） |
| `calibration_multiplier` | **MVP 调参主要改这个**（默认 1.0） |
| `profile_json.classes.<class>.token_per_char` | **高级模式才调** |
| `version` | 自动递增 |
| `is_active` | 同 (provider, model, service_type) 唯一 active 版本 |
| `change_reason` | 调参时必填，记录原因 + 数据期 + 样本量 |

### Admin API endpoints

- `GET /v1/admin/context-budget/token-profiles?provider=X&model=Y&is_active=1` 当前生效版本
- `GET /v1/admin/context-budget/token-profiles/history` 版本历史
- `POST /v1/admin/context-budget/token-profiles` 创建新版本（自动失活旧版）
- `PUT /v1/admin/context-budget/token-profiles/:id` 更新当前版本（不推荐——版本化设计要求新增不更新）
- `GET /v1/admin/context-budget/events` 浏览偏差数据

---

## §8 触发提示词模板

未来 session 调参，用户可以用以下任一句开头：

```
跑一下 context-budget 月度调参
```

```
按 docs/superpowers/runbooks/context-budget-calibration.md 跑这个月的 context-budget 调参
```

```
跑 context-budget 调参，只看 deepseek-v3.2 这个模型，其他不动
```

AI 收到任一句应当：
1. 读本 runbook
2. 读 manifest 里 `context-budget-compression` 条目最新状态
3. 按 §4 步骤执行（每个改动前必须用户确认）
4. 完成后生成归档报告

---

## §9 边界情况

- **样本量不足**（任一桶 < 30）：跳过该桶，下轮再说
- **新模型上线**：第一次调参前样本不足，建议手动设保守 `calibration_multiplier=1.15`
- **流量异常**（某用户批量异常 prompt）：剔除后再统计，避免单一来源污染
- **profile_json 内容缺失/损坏**：用 spec §3.2 的 default profile 当兜底初始化（method='default', safety_multiplier=1.30, calibration_multiplier=1.0），不要继续用错误数据
- **事务锁失败**（同时多个 admin 创建新 profile）：重试一次，仍失败报错给用户

---

*最后更新：2026-04-28*
*下次复审：第一次实战调参后（约 2026-05-15，假设 2026-05-01 上 prod）*
