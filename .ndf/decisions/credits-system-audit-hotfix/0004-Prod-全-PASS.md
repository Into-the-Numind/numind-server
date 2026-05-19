# Prod 全 PASS

**Date:** 2026-05-18

**Feature:** `credits-system-audit-hotfix`

**Migrated from:** `build-manifest.yaml` decisions[]

---

server v2.1.27 + web-v3 v1.0.24 + admin-web v1.4.4 三 CI 端到端无 GFW 失败一气上线。Prod sweeper started log @21:48:13 确认运行。GORM AutoMigrate 自动给 payment_order 加上 idempotency_key UNIQUE 列。
