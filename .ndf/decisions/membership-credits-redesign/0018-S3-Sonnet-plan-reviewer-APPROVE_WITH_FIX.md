# S3 Sonnet plan reviewer APPROVE_WITH_FIXES

**Date:** 2026-05-15

**Feature:** `membership-credits-redesign`

**Migrated from:** `build-manifest.yaml` decisions[]

---

3 P0 (T4 漏切 HasActiveSubscription/HasTrialPackage 两 guard reader / 依赖图 T1→T10 错误箭头 / T9 删 getLegacyEvents 破坏历史报表) + 7 P1 + 3 P2，全部修复到 plan extension v2。修复细节：T4 scope 扩 + 切两个 guard 到读 subscription/trial_grant、依赖图删 T1→T10、T9 保留 getLegacyEvents 作 read-only 历史路径、T2 拆 T2a (FE 先) + T2b (BE 后)、T7 SQL join payment_order、T8 rollback partial recovery、T11 完整 rollback SQL、§4.5 主动触发验证表、T12 加 dev-environment-setup.md。
