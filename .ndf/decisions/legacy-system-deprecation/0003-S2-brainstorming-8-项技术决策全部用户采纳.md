# S2 brainstorming 8 项技术决策全部用户采纳

**Date:** 2026-05-18

**Feature:** `legacy-system-deprecation`

**Migrated from:** `build-manifest.yaml` decisions[]

---

(1) API 字段 omitempty 移除 (2) admin_migration controller 整删 (3) IncrementSopRunCount 删调用方+删方法体 (4) tier_change_log 改名 legacy_ 前缀停止写入 (5) 老 billing_mode migration SQL 保留不动 (6) doc 文件 inline 改写留锚点 (7) server 先 → 前端两仓 ≥1 天后 (8) Schema DROP 提供完整 rollback SQL + prod backup 双保险。
