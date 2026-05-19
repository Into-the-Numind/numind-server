# T1 完成 — worktree-isolated

**Date:** 2026-05-15

**Feature:** `membership-credits-redesign`

**Migrated from:** `build-manifest.yaml` decisions[]

---

T1 (加 credit_transaction.source_type/source_id 列 + 回填) 用 isolation:worktree 隔离开发。T1 implementer commit 3f9ee4f：migration forward+rollback + GORM model + 3 写入点（deductCreditsTxFull / refundOneItem / 测试），但 2 个 reviewer 找到 1 P0 + 2 P1 + 3 P2。P0：新 path MembershipService.DeductCreditsTx 完全不写 credit_transaction → T7/T8 SOT 假设崩塌。fix subagent commit f56d00b 修 6 个问题 + 扩 T1 scope：cycle.go 新增 8 个 ledger 写入点（3 deduct + 5 refund，全部 inside tx，source_type/source_id 正确），refund subscription→cycle vocabulary mapping，migration START TRANSACTION wrap + IF NOT EXISTS 幂等，2 个测试 DSN 加 per-test name。round-2 sonnet reviewer APPROVE，0 新问题，go test + task lint 全绿。等用户拍板 merge develop + 部署 dev + 主动触发验证 → 进 T2。
