# T1 deploy + 主动验证 PASS

**Date:** 2026-05-15

**Feature:** `membership-credits-redesign`

**Migrated from:** `build-manifest.yaml` decisions[]

---

merge 226df5a → push develop → CI 25916337787 success → GORM AutoMigrate 自动加 source_type/source_id 列 + idx_ct_source 复合索引。SSH dev 跑 backfill UPDATE 196 行（booster 107 + subscription 89 + 2 debt rows NULL by design）。gstack browse 自动登录 dev → chatbot 发消息 → AI 回 'Ready.' → SQL 校验：新 credit_transaction id=201 (budget_reserve -72) + id=202 (refund +69) 都 source_type='cycle' source_id=6 package_id=0。完美闭环：新 path 写 ledger + source_type 正确填入。用户选 C 模式跳过 24h 监控期直接进 T2。
