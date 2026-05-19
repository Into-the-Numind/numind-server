# Triage

**Date:** 2026-05-14

**Feature:** `credits-deduct-cycle-wiring`

**Migrated from:** `build-manifest.yaml` decisions[]

---

3 个独立 reviewer subagent 共识 — 注释 STALE-LIE，GetQuotaBreakdown 读 credit_package 但 grant 只写 subscription/credit_cycle。所有 SOP/chatbot/salesrag 的 pre-check 都返回 SubRemain=0，total = booster_remain（最多 600），估算>600 立即 ErrInsufficientCredits。Reserve/Reconcile 也写老表，新表 credit_cycle 永远不动。修复方向：完整切换 Reserve/Reconcile 到 MembershipService.DeductCredits（用户拍板：选项 B 不选短期止血选项 A）
