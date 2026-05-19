# S4 merge to develop

**Date:** 2026-05-14

**Feature:** `membership-credits-redesign`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户恢复推进。feat/membership-credits-redesign 分支落后 develop 75 commit、领先 20 commit；merge origin/develop 进分支共 6 个冲突全部解决——store.go 取并集（Membership + SopVisibilityGrant + ChatbotVisibilityGrant）；server.go 取分支版（移除 develop 加的 creditBiz.RunCronTasks，符合 I-2 去 cron 化）；payment.go 取分支版（booster-only CreateOrder + quantity 参数，废弃 develop 的 months 兼 quantity 兼容路径）；order.go 取分支版（createOrderRequest quantity 强制必填）；dry-run.sql 取 develop 版（§C SQL 语法已用 CTE+ROW_NUMBER 修复）；manifest 取并集 + 修正 4-30 'all merged' 误判。develop 原本缺失的 67 文件全部新增（internal/numind/biz/membership 8 文件 + internal/numind/store/membership 9 文件 + internal/pkg/model/membership 7 文件 + idempotency.go/maintenance.go 中间件 + 5 张新表 migration + 完整测试套件）。
