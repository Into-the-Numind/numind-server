# H1 Triage

**Date:** 2026-05-19

**Feature:** `drop-billing-account-dead-table`

**Migrated from:** `build-manifest.yaml` decisions[]

---

grep 三仓零业务引用确认（仅 model/store/helper/charset 配置 4 处死代码 + 1 处 add_billing_tables.sql migration）。dev/qa/prod 三库 billing_account COUNT(*) = 0 / nonzero_rows = NULL，无数据丢失风险。Hotfix track 合理，跳过 brainstorming/spec/plan 重型流程。
