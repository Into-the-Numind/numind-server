# V1→V2 plan extension

**Date:** 2026-05-15

**Feature:** `membership-credits-redesign`

**Migrated from:** `build-manifest.yaml` decisions[]

---

V1 方案 5 task 经 3 独立 sonnet reviewer REJECT 9 P0（缺 source_type / 颗粒度粗 / b2b_billing 前置漏 / 校准目标错位 / FE 未删 / parent_grant 描述误导 / user 1 不能盲处理 / 删 ubb 冲突 spec §2.4 / DROP 不可逆 rollback 缺失）。V2 拆 12 task：T1 加 credit_transaction.source_type 前置 / T2-T6 拆 4 个写入路径每个原子 / T7-T8 数据校准 / T9 b2b_billing 前置 / T10 DROP COLUMN credits_deducted / T11 archive+DROP credit_package + balance 字段 / T12 加 FK 收尾。详细 plan：plans/2026-05-15-membership-credits-redesign-cleanup-plan.md。
