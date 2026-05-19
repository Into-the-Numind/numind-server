# Prod 实测 5/5 PASS

**Date:** 2026-05-18

**Feature:** `credits-system-audit-hotfix`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户要求自信度 99%+。创建 audit_parent(id=435) + audit_child(id=436) dev 测试用户。(1) GET /v1/credits/balance 响应只含 5 干净字段无 legacy ✓ (2) POST grant-membership 给 id=1（非 child）返 403 ✓ (3) 同 endpoint 给 id=436（合法 child）返 200 + trial_grant 写 200 积分 ✓ (4) POST /v1/orders 同 Idempotency-Key 双提交返回相同 id=29 + order_no ✓ (5) 注入 status=reserved created_at-2h 的 zombie reservation id=1667，5min sweeper tick 自动 refund，status='refunded' finalize_reason='expired_by_cron' ✓。完整通过后清理测试数据。
