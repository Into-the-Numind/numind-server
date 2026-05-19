# H3 dev 验证 PASS

**Date:** 2026-05-19

**Feature:** `settings-credits-display-broken`

**Migrated from:** `build-manifest.yaml` decisions[]

---

admin/admin123456 登录 $DEV_API_URL，GET /v1/credits/balance 返回 cycle_remaining=1978, booster_total=6000, booster_usable=6000, membership_state='pro', sub_expires_at='2026-06-07' — 后端 BalanceDTO 字段就位。Playwright headless Chromium 访问 $DEV_SITE_URL/settings 截图：(1) 会员积分 1978 + 6月底 过期，显示正常 (2) 加量包『6000 积分』单数字（不再是 0/N 分式）(3) BoosterPurchaseCard data-state='credits' / 不含 is-disabled / 显示『立即购买』绿色 CTA — Pro 会员可购买。Console 0 错误。三个用户报告的 bug 全部 fixed。
