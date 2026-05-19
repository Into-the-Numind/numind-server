# Audit 兜底 — sweeper 暴露另一个既存 schema drift bug

**Date:** 2026-05-18

**Feature:** `credits-system-audit-hotfix`

**Migrated from:** `build-manifest.yaml` decisions[]

---

id=1598 是 prod 数据迁移过来的 3-天-旧真实 zombie。Sweeper tick 时尝试 refund 失败 Error 1265 'Data truncated for column event_type'。深查发现 membership_event 表的 event_type 是 ENUM('trial_granted','sub_granted','sub_renewed','booster_granted','admin_calibration')，但 Go 模型 GORM tag 声明 VARCHAR(30)。RefundCreditsTx 写 EventTypeRefundLost='refund_lost' 被 ENUM 拒绝。这是 membership-credits-redesign 原始建表 migration 与 model 漂移的既存 bug，因为 refund_lost path 之前从未被触发（无 cron sweeper），ENUM 也从未 trip 过。第二刀：product_type ENUM('trial','monthly','booster') 拒绝 'cycle'，是同类 drift（model 是 VARCHAR(20)）。新 migration 20260518_230202_fix_membership_event_type_enum_to_varchar.sql 双列 ALTER 改 VARCHAR 已应用 dev + prod。Dev 重测：id=1598 在下次 sweeper tick (23:12:21) 成功 refund，credit_transaction 也写入 amount=0 operation='refund_lost' 守恒律满足。
