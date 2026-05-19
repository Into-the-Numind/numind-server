# S2 spec review-fixes

**Date:** 2026-04-30

**Feature:** `membership-credits-redesign`

**Migrated from:** `build-manifest.yaml` decisions[]

---

派 4 个串行 fix subagent（避免并行 Edit 同文件 race），每章节修复一组问题。共修 47 条（18 P0 + 21 P1 + 8 P2，剩 6 P2 为低优纯优雅度）。spec 增至 6559 行（+432 行新增内容含错误码 ErrSubscriptionExpired/ErrIdempotencyKeyConflict 定义、Invariant I-13 / I5b、§7 cutover_date 必填启动校验、§10.1 maintenance 503 豁免支付回调、§9.1 anchor_add_months 7/31→8/31 测试 case 等）。最终验证：cycle_index in SQL = 0 / months_or_quantity = 0 / trial_pro_overlap = 0 / ErrMembershipSelfPurchaseDisabled = 0 / has_active_pro = 0 / code_url = 0 / 章节锚点 10 个完整 / 无 TODO TBD 残留
