# S1 二次 audit 漏发现

**Date:** 2026-05-18

**Feature:** `legacy-system-deprecation`

**Migrated from:** `build-manifest.yaml` decisions[]

---

比 S0 列的 19 处多发现：admin_migration controller 整套 / admin_user/user.go:273-293 tier 编辑 / store/customer.go IncrementSopRunCount / admin-web UsersView 等级列 + CreditUsersView banner + MigrationsView 整页 / 2 个 migration SQL ENUM 收敛。Sub-task 拆分维持 4 task，但每个范围扩大。
