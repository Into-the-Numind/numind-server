# 需求卡片 — multimodal-billing-fix（S0）

> feature `multimodal-billing-fix` · Standard · 2026-06-17 · 仅 numind-server
> 起因：客户（dev 自测）上报 — 一次 chatbot 上传图片，被扣 64000 积分。

## 1. 问题（客户原话）

「我使用了一次 chatbot，并且上传了图片。然后从积分消耗记录表里看到，直接扣了 64000 的积分，这是一个巨大的 bug。」

## 2. 根因（已完成 forensic，三处 bug 叠加）

一次 claude-opus-4-6 识图请求，真实用量 2178 输入 + 504 输出 token，真实成本 ≈ **0.12 元 ≈ 12 积分**。但实际：预扣 796、对账扣到 64000。链路：

1. **定价按 service_type 路由（错误设计）**：网关 `classifyServiceType` 把**任何带图片的请求**自动归类为 `service_type=llm_vision`。但 `pricing.resolvePricingRule` 把 service_type 当**定价主键的一部分**——`(llm_vision, dmxapi, claude-opus-4-6)` 与 `(llm_chat, …)` 是两个 key，缺一个就硬报错，**不会回退到同一模型的另一个 service_type 价**。`llm_vision` 只配了 ali/qwen-vl + volc/doubao，**dmxapi/claude 无 vision 价** → CalculateCost 报错。
2. **对账缺价兜底 = 灾难放大（单位错误）**：`finalizeReservationIfNeeded` 在 holder 未 Set（缺价）时 fallback 到 `fi.EstimatedCredits = ReservedOutputTokens`（`context_budget.go:864`）——把**输出 token 数（64000）直接当成积分数**扣，连单价都没乘。`ReservedOutputTokens = MaxOutputTokens/2`，claude 128K → 64000。
3. **预扣估算严重虚高**：bill-only 路径 `synthBillOnlyResult` 硬编码 `reserved = MaxOutputTokens/2 = 64000` 当输出估算，按 claude 价算 = 796 积分。真实输出 504 token（12 积分），**预扣比实际高 66 倍**。

> 关键证据：`llm_chat` claude-4.7（有价）对账 30 ✓；`llm_vision` claude-4.6（无价）对账 64000 ✗。已确认**没有任何模型配了双模态价**（按 (provider,model) 定价零歧义、安全）。

## 3. 影响范围

- **dev**：user 1 被扣爆（已恢复 4758 积分，audit txn 8472）。
- **prod**：**未被咬**（已核查，无 ≥5000 对账；prod 识图全走 ali/qwen-vl，有 vision 价）。三个识图相关 feature 均未上 prod。
- **潜在 prod 风险**：agent 模式/SOP 走 bill-only，若发图给 dmxapi/claude 会踩同样雷 → 根 #2/#3 必须治本。
- **附带（本 feature 顺修或记 follow-up）**：prod vision usage provider 名 `ali` vs 定价规则 `ali-dashscope` 不匹配 → prod 识图一直按 0 计费（少收，不致命）。

## 4. 目标（三个改 + 数据已修，纳入同一 feature）

| # | 改什么 | 解决 |
|---|--------|------|
| ✅ 已做 | dev 退回 4758 积分（audit txn 8472） | 客户损失 |
| ① | 定价按 `(provider, model)` 解析，service_type 不再是定价主键（缺模态时回退到模型唯一价） | 根 #1 |
| ② | bill-only 预扣改用历史均值估算器 + 合理冷启动默认，预扣贴近实际 | 根 #3 |
| ③ | 对账缺价兜底：退款/收 0 + 告警，**绝不把 token 数当积分**、绝不超过预扣 | 根 #2（安全网）|

## 5. 验收标准

- AC1：chatbot 选 claude-opus-4-6/4-7 发图 → 按真实 token × 模型价计费（~10 量级积分），不再 64000、不再 796 虚高。
- AC2：对账缺价（人为构造无定价模型）→ 收 0/退款 + 日志告警，绝不放大。
- AC3：bill-only 预扣（chatbot vision + agent run）与实际对账值同量级（不再 66 倍虚高）。
- AC4：专用视觉模型（qwen-vl/doubao-vision）定价仍正确命中、不回归。
- AC5：复现测试永久留库（claude 图片被收 64000 场景红→绿）。

## 6. Triage

> 推荐档位：**Standard**
> 理由（5 条）：(1) 无 DB schema 变更（定价是数据/逻辑，非建表）✓ 但 (5) **直接碰计费/支付高风险逻辑** ✗ → 强制 Standard。涉及文件 pricing.go + context_budget.go 多处 + 测试，>3 文件。
> Bug-from-Customer（Rule 11）：第一个 commit 必须是失败的复现测试。

**用户已确认：Standard 档，三个改纳入同一 feature。**
