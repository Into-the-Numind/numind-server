# Credits System 全面审计报告（post legacy-system-deprecation T1-T4）

**审计日期**: 2026-05-18  
**审计触发**: 用户在 legacy-deprecation feature 完成后要求"全面检查会员积分相关逻辑，确认链路不再有 legacy 残留，新体系完全跑通且无漏洞"  
**审计设计**: 5 维度（A 残留扫描 / B 流程正确性 / C 漏洞 / D 跨仓 contract / E 实测），4 个 subagent 并行 + 主会话验证

## 执行摘要

| 严重度 | 数量 | 类型 |
|---|---|---|
| **🔴 P0** | **2** | 真实安全/资金漏洞，需 hotfix |
| 🟡 P1 | 2 | 中等问题（文档不实 + 隐性 IOU） |
| 🟢 P2 | 13 | 死代码/注释/小漂移 |
| ℹ️ info | 12 | 故意保留的历史记录 |

**结论**: legacy 体系**已从代码层全量移除**（A 维度 0 P0 in 主代码，只 e2e 测试残留）。新 credits 体系**5 个核心流程全 PASS**（B1-B5 无 P0）。但**审计揪出 2 个独立于本次 refactor 的既存漏洞**——即下面 P0-1 和 P0-2，建议立刻 hotfix。

---

## 🔴 P0 — 必须立刻修复

### P0-1: 授权绕过 — 任意用户可给任意用户开会员

**位置**: `internal/numind/controller/v1/credit/grant_membership.go:78,99`

**漏洞**:  
`POST /v1/users/children/:child_id/grant-membership` 路由（router.go:275）挂在裸 `authGroup` 上，没加 `ParentUserOnly()`。Controller 直接调 `c.membershipSvc.GrantTrial` / `GrantOrRenewSubscription`，**绕过** biz 层的 parent-child 校验。`MembershipService` 内部只挡 `parentID == userID`（自购）。

**攻击面**:
- 任何已认证用户都能给任意 user_id 开 3 天 200 积分试用 → **烧掉受害者的 lifetime trial 配额**（`uniq_trial_user_id` 后受害者再不能拿试用）
- 任何已认证用户都能给非自己的 user_id 开 monthly subscription → 在 `membership_event.granter_user_id` 中记账，**B2B 月末账单算到攻击者头上**，金额 = months × ¥99

**根因**: T2 把 `creditBiz.GrantMembership`（biz/credit/grant_membership.go:89-251，含 line 120 的 parent 校验）变成了死代码（comment 自承"本方法仅为单测覆盖"），实际生产路径是 controller → membershipSvc 直连。

**修复方案**:  
方案 A（推荐）：让 controller 走回 `creditBiz.GrantMembership` 而非直连 `membershipSvc`  
方案 B：在 controller dispatch 前加 `c.ds.Customers().GetSubUser(ctx, parent.ID, uint(childID))` 校验

**严重度判断**:
- 影响：safety + 资金（攻击者直接污染受害者账户 + 攻击者承担非自愿账单）
- 触达：任何 authenticated 用户 + 简单参数遍历
- 当前 prod 暴露：是

### P0-2: Reservation 泄漏 — 服务器崩溃 = 永久丢失用户积分

**位置**: 缺失整个 cron / 后台 sweeper

**漏洞**:  
`Reserve` 在 transaction 内立即把 credit_cycle/trial_grant/user_booster_balance 扣掉（cycle.go:258, 289, 322），并写入 `credit_reservation` 行 `status='reserved'`。正常路径靠 `defer FinalizeReservation` 触发 `Reconcile`/`Refund` 把状态更新成 `final` 或退款。

**问题**: 服务器在 Reserve 完成后、Reconcile/Refund 完成前**崩溃**（kill -9 / OOM / deploy 中断 / context timeout 杀掉 defer），那条 reservation 永远停在 `status='reserved'`，**用户的积分被扣但永远不会退回**。

**审计结果**:
- `grep CleanZombieReservations` → 0 hits
- `grep "expired_by_cron"` → 仅注释/测试引用，无生产调用方
- `server.go:50` cron 列表只有 order 过期清理，无 reservation sweep

**修复方案**:  
增加后台 sweeper：扫 `credit_reservation WHERE status='reserved' AND created_at < NOW() - INTERVAL 1 HOUR`，复用现有 `Refund(ctx, id, "expired_by_cron")` 调用。

**严重度判断**:
- 影响：资金（用户付的积分永久丢失）
- 触达：每次服务器崩溃 / 强制 redeploy / k8s OOM kill
- 当前 prod 暴露：是。本日就发生过 1 次 prod 容器替换中断（hotfix v2.1.22 时 ~3min），如果当时有 in-flight reservation，已经永久丢了。

---

## 🟡 P1 — 中等问题

### P1-1: Lock-ordering 文档与实际不符

**位置**: `internal/numind/biz/membership/cycle.go:140`

文档注释声称按字母序 `credit_cycle < subscription < trial_grant < user_booster_balance`，但 `DeductCreditsTx` 实际跳过 subscription、只锁 cycle → trial → booster。当前不会死锁（无环），但若未来某 mutator 反向取锁（如 trial → cycle）→ 死锁。

**修复**: 更新注释 OR 抽象 `LockOrder` 常量集中维护。

### P1-2: Reconcile 隐性 IOU

**位置**: `credit_service.go:583-615`

当 Reserve 估算偏低 + 用户在 Reserve 与 Reconcile 之间花光余额 + Reconcile 发现 actual > reserved 时，需要 top-up 差额。若 top-up 失败（余额不足），代码记录 `credit_transaction.operation="reconcile_debt:<op>"` 留账，但**不抑制后续 Reserve**——用户继续欠款做新操作。

**修复方案**:
- A: cap reservation at predicted max (no overdraft)
- B: deduct accumulated debt from next Reserve

---

## 🟢 P2 — 清理项（13 项）

**已废弃但代码残留**:
1. `biz/sop/sop.go:816, 1560` / `biz/salesrag/salesrag.go:356` — `SkipDeduction` 分支永远 false（dead code）
2. `biz/credit/grant_membership.go:89-251` — `creditBiz.GrantMembership` biz 方法是 dead code（被 controller 直连绕过）
3. `biz/credit/credit_service.go:877-887` — Test-fallback `GetBalance` 漏 TrialRemain（仅测试触发）

**Backend 残留**:
4. `controller/v1/credit/credit.go:89` — 仍返回 `"billing_mode": "credits"`（credits 唯一后无意义）
5. `controller/v1/credit/credit.go:96-99` — 仍返回向后兼容字段 `balance`/`sub_total`/`sub_remain`/`booster_remain`
6. `controller/v1/credit/types.go:23` — `SkipDeduction bool // legacy_tier=true` 注释 stale
7. `biz/credit/contracts.go:12` — 历史注释（故意保留，info 级）

**Contract 漂移**:
8. `numind-admin-web/src/api/users.ts:8` — `tier_expires: string` 字段 backend 已不返回
9. `numind-admin-web/src/api/users.ts:12` — `monthly_sop_runs: number` 字段 backend 已不返回

**幂等/账本细节**:
10. `payment.go:104-186` — Order create 的 Idempotency-Key header 没存进 order 表，重复提交产生孤儿 pending order
11. `credit_service.go:484-502` — `refund_lost` event 无对冲 `credit_transaction`，违反 SUM=net flow 守恒
12. `cycle.go:408` — Refund 在原 trial 过期时**静默改池**到 booster/cycle（trial 的 0.5x user_type_multiplier 优惠丢失）

**Timezone**:
13. `credit_service.go:606,621,713` / `cycle.go:271,302,336,397` — `time.Now()` 裸调（local TZ）vs 其他地方 UTC。当前在 Asia/Shanghai 无 DST 不出问题，但跨时区/UTC 部署会漂 8h。

**E2E 测试残留（非 prod）**:
14. `numind-web-v3/e2e/helpers/credits-admin.ts:26,37-38,157-162` — type/mock 还有 `legacy_tier`
15. `numind-web-v3/e2e/credits-system.spec.ts:321` — test case 还测 `switchBillingMode('legacy_tier')`
16. `numind-web-v3/src/views/sop/composables/useDraftLifecycle.ts:27` — stale 注释引用 `IncrementSopRunCount`

---

## ✅ 正面确认（无问题）

### Legacy 残留（A 维度）
- 主代码（非测试 / 非 markdown / 非 archive / 非 migrations）**0 真引用** legacy 符号
- 仅 `contracts.go:12` 一行历史注释 + `build-manifest.yaml` 描述字段 — 故意保留

### 5 大核心流程（B1-B5）— 全 PASS
| 流程 | 单路径分发 | 走 MembershipService | Tx 边界 | 错误退款 | 幂等 |
|---|---|---|---|---|---|
| B1 GET /v1/credits/balance | ✓ | ✓ | N/A read | ✓ | N/A |
| B2 SOP execute | ✓ | ✓ | ✓ | ✓ | ✓ |
| B3 Chatbot stream | ✓ | ✓ | ✓ | ✓ | ✓ |
| B4 SalesRAG stream | ✓ | ✓ | ✓ | ✓ context.WithoutCancel | ✓ |
| B5 Booster 自购 + 回调 | ✓ | ✓ | ✓ | ✓ | ✓ order_no 幂等 |

### 状态/边界（B6-B9）
- B7 trial 发放：UNIQUE 双保险（pre-tx 查 + tx 内 violation 兜底），无双发漏洞
- B8 MembershipState：trial > pro > free 优先级正确
- B9 Cycle rollover：lazy create + `AnchorAddMonths` 处理跨月日数边界（Jan31→Feb28→Mar31）

### 并发安全（C1）
- `DeductCreditsTx` 三池统一 FOR UPDATE 顺序
- `Increment` 用 ON DUPLICATE KEY UPDATE 原子化
- `ensureCurrentCycle` 用 InsertOrIgnore + SELECT FOR UPDATE

### 资金守恒（C4）
- 每笔 Deduct/Refund/top-up 都写 `credit_transaction` 三元组 (source_type, source_id, amount)
- Reconcile top-up 用 `operation="reconcile_debt:<op>"` 标记
- 守恒律可通过 `SUM(credit_transaction.amount per user) == -SUM(usage_record.cost * multiplier per user)` 离线对账

---

## 优先级行动清单

| # | 项 | 优先级 | 估时 | 类型 |
|---|---|---|---|---|
| 1 | **P0-1 grant_membership 加 parent 校验** | 🔴 紧急 | 1-2h | 新 hotfix |
| 2 | **P0-2 reservation 后台 sweeper** | 🔴 紧急 | 4-6h | 新 hotfix |
| 3 | P1-1 lock-order 注释订正 | 🟡 | 15min | 顺手做 |
| 4 | P1-2 reconcile debt 策略决策 | 🟡 | 需产品决策 + 0.5-1d 实施 | 单独 feature |
| 5 | P2 #1-2 删 dead code（SkipDeduction + creditBiz.GrantMembership） | 🟢 | 30min | 顺手做 |
| 6 | P2 #4-6 backend 死字段+stale 注释清 | 🟢 | 30min | 顺手做 |
| 7 | P2 #8-9 admin-web User interface 字段删 | 🟢 | 15min | 顺手做 |
| 8 | P2 #14-16 e2e 测试 legacy 残留清 | 🟢 | 30min | 顺手做 |
| 9 | P2 #10-12 账本细节修复 | 🟢 | 单独评估 | 单独 ticket |
| 10 | P2 #13 timezone 统一 UTC | 🟢 | 1-2h | 单独 ticket |

**推荐立即行动**：
- 今天：P0-1 + P0-2（合并成一个 hotfix feature 上 prod）
- 顺手：P1-1 + P2 #1-2、#4-9、#14-16（合并成一个 cleanup commit）
- 单独 feature：P1-2 + P2 #10-13

## 审计方法学

- 4 个并行 subagent + 主会话 5 维交叉验证
- 总耗时 ~30 min
- 假阳性率：A+D subagent 1 处误判（EstimateResp.balance 字段实际存在），主会话纠正
- 漏检风险：dim E 因 dev admin login 端口问题未跑实际 curl，仅做代码静态验证；live 行为靠 prod 已部署 v2.1.26 healthy + 既有 b8/c6 检查兜底
