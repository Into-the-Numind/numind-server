# S1 office-hours 自审

**Date:** 2026-05-15

**Feature:** `pricing-charge-cost-split`

**Migrated from:** `build-manifest.yaml` decisions[]

---

挑战 3 个替代方案 — 方案 X 直接改 CalculateCost 读 sell 字段（否决：污染 UsageRecord.CostCents 财务口径）/ 方案 Y 管理端禁止 sell != cost（否决：违背 ai-service-admin-complete 定价灵活性本意）/ 方案 Z 本方案分两方法（采纳：语义清晰可测）。时机判断：当前 prod cost == sell 是字节级一致的零风险部署窗口，等运营调价后再改需要数据回填，风险大。范围控制：拒绝顺便重构 UserTypeMultiplier / safetyBufferPct / cache 层，单一职责 feature。
