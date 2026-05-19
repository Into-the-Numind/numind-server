# S4 Phase 5 Task 13 deferred

**Date:** 2026-05-14

**Feature:** `sop-chatbot-visibility-scope`

**Migrated from:** `build-manifest.yaml` decisions[]

---

plan 假设有 DeleteSubUser 流程, 实际 grep 后代码库零此路径 (无 DELETE /v1/customers/sub-users/:user_id 路由, 无 customer.DeleteSubUser biz). spec §5 '现有删除路径' 描述错误. store.CleanupBySubUser 已实现备用 API, 未来加 DeleteSubUser 功能时 1 行接入即可. 用户确认跳过, 不增加无效代码
