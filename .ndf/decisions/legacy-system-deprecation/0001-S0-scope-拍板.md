# S0 scope 拍板

**Date:** 2026-05-18

**Feature:** `legacy-system-deprecation`

**Migrated from:** `build-manifest.yaml` decisions[]

---

(1) user 表字段策略：清数据 + 最终 DROP user_tier/tier_expires/monthly_sop_runs/monthly_reset_at/billing_mode 列 (2) API 字段：直接删 remaining_runs / monthly_limit，同步前端 (3) 测试：删 legacy 专用，双制测试改 credits-only (4) PR：分 3-4 个 sub-task 递次提交
