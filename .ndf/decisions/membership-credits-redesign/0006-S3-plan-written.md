# S3 plan written

**Date:** 2026-04-30

**Feature:** `membership-credits-redesign`

**Migrated from:** `build-manifest.yaml` decisions[]

---

派 5 个并行 subagent 写出 23 个 task 跨 7 个 phase 的 plan（约 5251 行）。Phase 1 schema/foundation (Task 1-4) + Phase 2 算法 biz (Task 5-8) + Phase 3 API 端点 (Task 9-13) + Phase 4 迁移+部署+cleanup (Task 14-16) + Phase 5 用户端前端 (Task 17-20) + Phase 6 管理端前端 (Task 21) + Phase 7 cleanup+验证 (Task 22-23)。其中 Task 23 是 NDF Rule 10 强制末尾的 S5 验证策略 task（Playwright E2E + gstack /qa + Go 并发压测三件套，6 条关键路径含 idempotency 矩阵）
