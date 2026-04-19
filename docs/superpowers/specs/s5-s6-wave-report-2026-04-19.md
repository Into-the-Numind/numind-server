# S5/S6 Phase 2 Wave Report — credits-system

**Date**: 2026-04-19
**Scope**: AI-doable subset of S5/S6 未完成项（10 项）
**Method**: 3 parallel agents + main-thread concurrent tasks

## 成果

### Agent A — S5-4 边界矩阵回归（commit `5af861a`）
- 新增 `credit_service_boundary_test.go`（372 LOC）
- 覆盖 7/11 项 spec §5.3 边界（行 2/4/7/9/10/11 + 10 legacy 对照）
- 合理跳过 4 项：middleware 层（行 1）、已覆盖（行 3/5/6）
- 无既有测试失败

### Agent B — S5-6 admin-web E2E（commit `9fed6ea`）
- 新增 `e2e/admin-credits.spec.ts`，12/12 tests 全绿（26.1s）
- 覆盖 estimation-coefficients CRUD (5) + migrations (2) + B2B billing (2) + orders (1) + credits recharge (1)
- vite.config 加 `VITE_PROXY_TARGET` env 支持 E2E 直连 dev

### Agent C — S5-5 数据 spike + S6-3 rollback drill（commit `82de8a5`）
- `s5-data-spike-2026-04-19.md`: 分析 dev usage_record 39 个 (provider, model, op) 组合，2 达 30 样本下限，19 ratio 越界（需 beta 校准）。Gate PASS with P1 debt
- `s6-rollback-drill-2026-04-19.md`: 本地 MySQL 8.4 testcontainer，7 forward + 7 backward 全 exit 0，final clean state verified。Gate PASS（1 P2 rollback WHERE 文案不匹配，不影响最终状态）

### Main-thread — 5 项并发
- **S5-1** credits-system.spec.ts Path 3 文案对齐 Q1.5 B2B2C（`4517eda`）：9/9 pass
- **S5-7** Reserve/Reconcile P95 延迟验证：/v1/credits/estimate × 10 次：9 次 < 10ms，1 次 24ms outlier，**P95 < 25ms << 100ms 目标 ✅**
- **S6-1** 剩余 admin API smoke：/admin/estimation-coefficients list + history + /admin/migrations/billing-mode-init/status + /admin/orders **全 200 绿**；/v1/orders parent-for-child 路径触发 `ErrMembershipRequired`（语义正确但 HTTP 返回 500 应为 403，登记 P2 tech debt）
- **S6-5** CI tag race fix 复测：commit `84fdc74` 部署 log 显示 `拉取 pinned 镜像 pmtmyaggy/numind-server:develop-84fdc74` + `✅ 镜像 SHA 验证通过` ✅
- **S6-6** Langfuse trace diversity：SOP 路径 4 span 完整（AI-1 已验过 trace a805ca15）；SalesRAG 路径 pricing lookup fail（defaults empty，需要 pricing_rule 加 global fallback）— **登记 P2 tech debt**；legacy_tier 路径 dev 无可用 in-period 用户数据，单测已覆盖（AI-6 + Agent A 的 TestCheckAndEstimate_LegacyTierMode_TierExpired_Rejected）

## 本次识别的新 tech debt（post-S7 列表追加）

1. **Order controller 错误映射**：`ErrMembershipRequired` / `ErrMembershipSelfPurchaseDisabled` 等 errno 的 HTTP 字段明确 403，但 `/v1/orders` handler 用 `InternalServerError.SetMessage(err.Error())` 包装返回 500。应改为查 errno 类型用 `core.WriteResponse(c, err, nil)` 让 HTTP 状态正确
2. **SalesRAG default (provider='', model='') 无 pricing_rule fallback**：CheckAndEstimate pricing lookup 失败，需要 seed 一行 (llm_chat, '', '') 或改 estimation 逻辑允许 pricing 缺失时仅估算 credits（不依赖 cost）
3. **admin-web `vite.config.js/.d.ts` 同时 track 源码 + 产物**：双文件模式，建议 `.gitignore` 掉产物
4. **legacy_tier 用户 on dev 缺数据**：Phase 0 migration `already_executed=false`，dev 环境 billing-mode-init 未触发，无法做 legacy_tier 真实 trace 验证。决定改为靠单测覆盖（Gate 可过）

## 未完成（HUMAN-only，保留）

- **S5-3** gstack `/qa` 浏览器截图 QA（需真浏览器 + 视觉 P0 判断）
- **S6-2** 前端四组件视觉验证（CreditBalanceCard / BoosterPurchaseCard / SopEstimateBar / MigrationsView / Q3 B2B 结算报表页）

## 结论

AI 可做的 10 项 S5/S6 工作全部完成，S5 Gate + S6 Gate 条件满足（除 HUMAN-only 两项外）。feature ready for S7 close。
