# S6 prod tag v2.1.17 + 用户实测发现 3 处 follow-up

**Date:** 2026-05-14

**Feature:** `credits-deduct-cycle-wiring`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户打 v2.1.17 prod tag，包含本 feature + membership-balance-read-path。用户 chatbot 实测发现 3 处漏修，均当场快速 fix：(1) commit 480f196 — creditsImpl.reserveBudgetRow (chatbot/gateway 路径) T8 只改了 Reserve 没改 reserveBudgetRow，同模式 dual-path patch (2) commit baa3159 — 新 path 的 CreditReservationItem 没填 PackageExpiresAt 列，MySQL strict mode 把 Go time.Time 零值翻译成 '0000-00-00' 报 Error 1292 (22007)。修：DeductItem 加 ExpiresAt 字段，DeductCreditsTx 各 pool 填 trial.ExpiresAt / cycle.CycleEnd / booster 2099-12-31 哨兵 (3) commit 8da9bf4 — Reconcile delta>0 top-up 仍调 c.biz.DeductCreditsTx 写老 credit_package，credits-mode 用户实际消耗超估算时差额被写 debt 台账不真扣，公司账目损失。修：加 c.membershipSvc != nil 分支走 MembershipService.DeductCreditsTx。
