# AiHubMix reasoning_tokens 计费对账 Spike

> **Feature**: aihubmix-protocol-audit Task 8
> **决策 Q6=A + Q9=A**：2 次对照 curl 实验，提前到 S4 早期执行规避 dashboard 结算时延风险
> **日期**: 2026-04-21

---

## §1 背景

T2 协议审计（`aihubmix-protocol-reference.md`）揭示 GPT 5.4 / Gemini / DeepSeek via AiHubMix 的 `usage` 都暴露 `reasoning_tokens`，但 **AiHubMix 官方定价表只有 input_price + output_price 两列，不存在 reasoning_price**。

三种可能：
- **Option A**：reasoning_tokens 在 AiHubMix 侧**独立计价**（可能高于 completion_tokens 价）
- **Option B**：reasoning_tokens 已**并入 completion_tokens** 计价（与 OpenAI 原生计费策略一致）
- **Option C**：数据噪声导致无法判定

本 spike 通过 2 次对照实验（low vs high reasoning_effort）在消除其他变量的前提下放大 reasoning_tokens 差值，以倒推 AiHubMix 实际计价策略。

---

## §2 实验方法

同一 prompt，同一 `max_completion_tokens=3000`，仅变化 `reasoning_effort`：

### 实验 1 — `reasoning_effort=low`

```bash
curl -sS -X POST https://aihubmix.com/v1/chat/completions \
  -H "Authorization: Bearer sk-vdu..." \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "messages": [{"role":"user","content":"Explain the trade-offs between binary heap vs Fibonacci heap for Dijkstra. 3 concrete points, concise."}],
    "max_completion_tokens": 3000,
    "reasoning_effort": "low"
  }'
```

### 实验 2 — `reasoning_effort=high`

同上，仅把 `reasoning_effort` 改为 `"high"`。

---

## §3 原始响应（raw JSON）

### 实验 1 LOW

```json
{
    "id": "chatcmpl-DWjJLBFYRpFYAD6PF56lrsG7jpIVP",
    "model": "gpt-5.4-2026-03-05",
    "usage": {
        "prompt_tokens": 27,
        "completion_tokens": 274,
        "total_tokens": 301,
        "completion_tokens_details": {
            "reasoning_tokens": 0
        }
    },
    "choices": [{"finish_reason": "stop"}]
}
```

- **prompt_tokens**: 27
- **completion_tokens**: 274
- **reasoning_tokens**: **0**
- **实际 output tokens** = completion - reasoning = **274**
- **content 长度**: 1143 chars
- **finish_reason**: stop

### 实验 2 HIGH

```json
{
    "id": "chatcmpl-DWjJQ4A94M6vX8wj323D2ZwQ2nUvo",
    "model": "gpt-5.4-2026-03-05",
    "usage": {
        "prompt_tokens": 27,
        "completion_tokens": 267,
        "total_tokens": 294,
        "completion_tokens_details": {
            "reasoning_tokens": 45
        }
    },
    "choices": [{"finish_reason": "stop"}]
}
```

- **prompt_tokens**: 27
- **completion_tokens**: 267
- **reasoning_tokens**: **45**
- **实际 output tokens** = completion - reasoning = **222**
- **content 长度**: 925 chars
- **finish_reason**: stop

---

## §4 观察与决策判据

### 关键观察

1. **GPT 5.4 在 `reasoning_effort=low` 下产出 0 reasoning_tokens**。低档 effort 对 GPT 5.4 不是"少量思考"，而是"完全不思考"。这与 T2 协议审计观察一致（`low` 在 GPT 上行为接近 `minimal`）
2. **HIGH 档只产出 45 reasoning_tokens**——这次 prompt 相对简单（三点比较），高档 reasoning 也不会大幅扩展。若需放大 reasoning 占比，prompt 应选复杂问题（例如多步推理数学题）
3. `completion_tokens` 在两次实验中接近（274 vs 267）——说明 `max_completion_tokens=3000` 对实际 output 无约束，两次 output 长度自然

### 数据不足，需要 dashboard 核对

本次 curl 产生的 reasoning_tokens 差值（0 vs 45）有限，仅通过 token count 无法准确倒推计价。必须结合 AiHubMix dashboard 实测扣费金额。

### Dashboard 核对步骤（待执行）

**SOP（需手工操作 https://aihubmix.com/dashboard）**：
1. 登录 AiHubMix 控制台
2. 进入"API 使用记录"或"账单明细"
3. 按 request id 过滤：
   - 实验 1：`chatcmpl-DWjJLBFYRpFYAD6PF56lrsG7jpIVP`
   - 实验 2：`chatcmpl-DWjJQ4A94M6vX8wj323D2ZwQ2nUvo`
4. 记录每 request 的扣费金额（¥）

### 三种判定分支

给定 GPT 5.4 on AiHubMix 公开价（截止 2026-04-21，假设 $a input / $b output per Mtok）：

| Dashboard 实测扣费 | 理论计算 | 结论 |
|-------------------|---------|------|
| `E1 ≈ 27·a + 274·b`，`E2 ≈ 27·a + 267·b` | 两者都等于 "prompt·a + completion·b"，reasoning_tokens **不独立** | **Option B**（并入 completion） |
| `E2 > E1` 明显（差额 ≫ (267-274)·b） | 超额部分 = 45·r，`r` 为 reasoning 独立价 | **Option A**（独立，记录 r） |
| `E2 ≈ E1 + 45·b` | reasoning 按 completion 同价计，但独立列 | **Option A with same rate**（需分列但价相同） |

---

## §5 结论（待 dashboard 补数据）

**当前状态**：实验 curl 已完成，2 个 request id 记录在案。等待 dashboard 扣费数据核对。

**初步推测**：基于 AiHubMix 定价表只有 input/output 两列的事实，**Option B（并入 completion）最可能**。若证实：
- `pricing_rule` **不需要**新增 `reasoning_price_per_mtok` 列
- `ChatResponse.Usage.ReasoningTokens` 字段仅作为 Langfuse 观测信号使用，不进入计费计算公式

**若证实 Option A**：独立 hotfix 登记加 `pricing_rule.reasoning_price_per_mtok` 列 + migration，本期 feature 不做。

---

## §6 行动建议

- [ ] **待用户/运维操作**：手动查 AiHubMix dashboard 对 2 个 request id 的扣费，记录到 §4 表格
- [ ] **短期**：根据 dashboard 数据在 §5 补最终结论（Option A/B/C）
- [ ] **如 Option B**：关闭本 spike，记录 `docs/aihubmix-protocol-reference.md` §5 补充"reasoning_tokens 并入 completion_tokens 计价"
- [ ] **如 Option A**：创建独立 hotfix feature 登记加 pricing_rule 列 + migration，本期 aihubmix-protocol-audit 不 block

---

## §7 附：Claude/Gemini/DeepSeek 同方法对比（时间允许时补跑）

本次只跑 GPT 5.4，因为它是 OpenAI 加密推理最典型的 reasoning_tokens 暴露模型。Claude via AiHubMix 不暴露 reasoning_tokens（T2 已证实），spike 无意义。Gemini/DeepSeek 同样暴露 nested `completion_tokens_details.reasoning_tokens`，若 dashboard 数据对 GPT 5.4 能得出结论，Gemini/DeepSeek 预期遵循相同策略（AiHubMix 统一计费框架）；若需逐 provider 验证，可照搬 §2 方法。
