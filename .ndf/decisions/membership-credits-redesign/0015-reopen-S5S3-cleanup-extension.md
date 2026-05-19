# reopen S5→S3 cleanup extension

**Date:** 2026-05-15

**Feature:** `membership-credits-redesign`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户 prod 直查 16 表后发现 Task 16 cleanup 未真正落地——credit_package 4 个 INSERT 入口（RechargeCredits/RechargeWithOrderTx/GrantMembership/legacy DeductCredits）仍主动写老表，新 MembershipService 不写老 credit_account.balance，新支付路径只写 ubb 不写 credit_package(booster)；结果 write 老表 + read 新表 = 必然残留。归类的 6 个数据问题深调后发现 3 类性质：(1) ubb vs credit_package(booster) = 设计意图（ubb 是 SOT，新支付路径绕过老表），user 1 6000 是 5/14 self_purchase ¥299 = 10 booster 合法、user 30/348 同模式 (2) credit_account.balance vs Σpackages = 过期清理 gap（user 388/392 4/26 trial 200 → 4/29 过期未衰减字段，但 GetBalance 走 expires_at 体验正确） (3) trial_grant 33 vs credit_package(trial) 32 = 设计意图（user 432 5/15 当天新路径开 trial 只写新表） (4) usage_record.credits_deducted 死字段（表保留，仅 1 列）。原 credits-system-data-consistency feature 取消并入本 feature 作为 plan extension。
