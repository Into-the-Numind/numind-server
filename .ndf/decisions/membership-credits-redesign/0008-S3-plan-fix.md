# S3 plan fix

**Date:** 2026-04-30

**Feature:** `membership-credits-redesign`

**Migrated from:** `build-manifest.yaml` decisions[]

---

派 2 个串行 fix subagent。Agent A 修 9 条实质问题（含新增 Task 4.5 集中落地 8 个 errno、Task 12 扩展含 GET /v1/orders/:id/status + GET /v1/users/children 两个轻量端点、CreditsRemain 7 处全局替换为 CreditsRemaining、Task 11 fulfillOrder 删 monthly/trial 分支只保留 booster、Task 14 apply.sql 删除 status 字段、Task 13 测试变量补定义、Task 2 GORM 复合索引 priority tag 修复）。Agent B 修格式（Tasks 5-8 H2→H3，Tasks 9-13 ①②③④⑤→checkbox，Tasks 14-16 子标题 14.1/14.2 → 内联 + checkbox，Tasks 17-23 H2→H3）。最终 24 个 task（含 Task 4.5）/ 109 个 checkbox 步骤 / 0 格式残留
