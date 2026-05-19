# H2 两阶段 Sonnet review

**Date:** 2026-05-19

**Feature:** `settings-credits-display-broken`

**Migrated from:** `build-manifest.yaml` decisions[]

---

Spec compliance PASS_WITH_CONCERNS (0 P0 / 2 P1 / 4 P2) + Code quality FAIL (1 P0 单测失败 / 2 P1 / 5 P2)。NDF Rule 7 全部就地修：P0 测试用旧字段 mock，更新为 BalanceDTO 新字段 (membership_state='pro'/'trial')；加量包断言从『350』改为『400』（新单数字版）。P1 修：formatMonthEnd/formatDate NaN bug (new Date('invalid') 不抛异常，加 isNaN 检查)；BoosterPurchaseCard JSDoc 矩阵和实际渲染对齐 (free/trial 灰态无 CTA，与代码一致)。P2 修：credits.ts totalRemain getter 改用新字段；SettingsView 注释更新；state.go 注释引用改用 store 名而非行号；admin_credit/balance.go FullBalanceView.BoosterTotal 加同语义注释。trial 用户能否购买加量包的 spec 不一致问题（CLAUDE.md §1 说允许，BoosterPurchaseCard 当前禁用）暂保持现行 prod 行为，单独 spawn task 处理 spec 对齐。
