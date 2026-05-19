# H3 dev 验证 — smoke test 命中预测

**Date:** 2026-05-16

**Feature:** `credits-gateway-completion-estimate`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用 admin token POST /v1/sop/runs?template_id=4 创建 run 2331，然后 POST /v1/sop/runs/2331/nodes/10/execute?model_key=deepseek-v4-pro 触发 gateway path。Stream 正常返回。检查 credit_reservation 表对比：id=1664 (pre-deploy 01:26): estimated_completion_tokens=16384, reserved_credits=12。id=1665 (post-deploy 15:44, 同 provider/model=dmxapi/deepseek-v4-pro): estimated_completion_tokens=2136, reserved_credits=2。2136 = ceil(1780 × 1.2) 精确等于预测值（1780 = usage_record 30d 历史均值, 1.2 = SafetyMultiplier）。reserved_credits 同 prompt 大小下降 6x。Helper 全链路 wiring 正确：CompletionEstimator → effectiveCompletionTokens → budgetMetadata + precheckIn 双站点 → 真实 DB 写入。Prod tag 待用户决定。
