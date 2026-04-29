# Context Budget Compression — S6 Prod Rollout 执行计划

> 目的：把 `develop` 分支上累积的 245 个 commit（核心是 `context-budget-compression` feature + 一批 hotfix）安全地推到 prod。
>
> 状态：planning（2026-04-29 制定）
> 配套 manifest 条目：`context-budget-compression`
> 上次 prod release：`v2.1.12`（2026-04-28 部署）

---

## §0 前置事实

```
prod 现状：v2.1.12（R2 计费机制，无 R3 schema）
release 分支：跟 v2.1.12 同步
develop 分支：领先 release 245 个 commit
  - context-budget-compression（本次主 feature，S5-done）
  - prod-payment / prod-wechat / customer-list 等 hotfix
  - credit-multiplier-per-model（最新 feat）
prod 数据库：缺 4 张新表 + credit_reservation 缺 6 个新字段
prod admin UI：你已手动把 10 个活跃 LLM 服务的 max_output_tokens 设好
prod 已知遗留：admin UI is_active 改不进 DB 的 bug（backlog 推迟）
```

---

## §1 总览（阶段 + 时间线）

```
Phase 1: Dev 加强测试 + SQL 验证          [约 1-2 天]
  ├── 1.1 多场景 dev 测试 (chatbot/SOP/SalesRAG)
  ├── 1.2 边界场景 (压缩触发/流中断/并发)
  ├── 1.3 dev 跑 backfill SQL dry-run
  └── 1.4 dev migration rollback 演练
                ↓ Gate 1: dev 99% OK
Phase 2: QA 部署 + 验证                    [约 1-2 天]
  ├── 2.1 合 develop → release，处理冲突
  ├── 2.2 QA 自动部署
  ├── 2.3 QA migration + backfill
  ├── 2.4 QA 全套 S5 验证
  └── 2.5 QA 测 245 commit 里的 hotfix 核心路径
                ↓ Gate 2: QA 全 PASS
Phase 3: Prod 部署预案                     [约半天]
  ├── 3.1 写 prod-deploy runbook
  ├── 3.2 准备 prod migration / backfill 最终版 SQL
  └── 3.3 准备回滚预案
                ↓ Gate 3: 预案 + SQL 都准备好
Phase 4: Prod 部署 + Canary                [部署 0.5-1h + 24h 观察]
  ├── 4.1 选窗口（低流量 / 你方便监控）
  ├── 4.2 prod DB 备份
  ├── 4.3 跑 prod migration
  ├── 4.4 跑 prod max_output_tokens backfill
  ├── 4.5 打 v2.1.13 tag → CI 部署
  ├── 4.6 canary 真实调用验证
  └── 4.7 24h 观察期
                ↓ Gate 4: canary 通过 + 24h 无异常
Phase 5: 上线后稳定 + 调参                 [上线 2 周后]
  └── 5.1 跑 calibration runbook (第 1 轮)
```

---

## §2 Phase 1 — Dev 加强测试 + SQL 验证

**目标**：把 dev 上的测试覆盖从"几个 happy path"提到"99% 场景都验证过"，且 backfill SQL 真在 dev 上跑过一次。

**入场条件**：
- ✅ S5 sign-off 已完成
- ✅ Dev 已部署最新 develop 代码
- ✅ Dev 已有 1 次成功的真实 chatbot 端到端调用

**任务清单**：

### 1.1 三个业务路径都真实跑通

S5 sign-off 时只跑了 chatbot。SOP 和 SalesRAG 也是同一套 Gateway middleware，但**走不同的 producer**——必须各跑一次。

- [ ] **chatbot**：再跑一次（已验证，确认仍 OK）
- [ ] **SOP**：在 dev 上跑一次完整 SOP 模板
   - 验证：`credit_reservation` 写入正确（estimation_source='context_budget'）
   - 验证：`context_budget_event.operation='sop_run'`
   - 验证：`usage_record.metadata` 包含 budget IDs
- [ ] **SalesRAG**：在 dev 上跑一次 SalesRAG 查询
   - 验证：与上同 + `operation='salesrag_query'` 或 `salesrag_stream`

### 1.2 边界场景验证

- [ ] **超预算压缩**：构造 prompt 让 `estimated_before > safe_input_budget`，验证：
   - `context_budget_event.compression_actions` 非空
   - `status='compressed'` 或类似
   - LLM 仍正常返回（压缩后的）
- [ ] **流式中断**：客户端中途断开 SSE，验证：
   - `credit_reservation` 状态被 `Refund` 或正确 finalize（不能卡 `reserved` orphan）
   - `context_budget_event` 有对应错误码
- [ ] **并发同 user**：同一个 user 短时间内并发触发 5 个 chatbot 调用：
   - 5 个 reservation 都正确独立 reconcile
   - 没 active version 切换冲突
- [ ] **error 恢复**：选一个 dev 上 active 的 LLM service，临时把 admin UI 里 `max_output_tokens` 改成非法值（比如 < `reserved_output_tokens`），验证：
   - 调用立即返回 `ErrContextConfigInvalid`
   - **不**真正打 LLM provider（Langfuse 不应该有 generation 行）
   - 改回正常值，调用恢复

### 1.3 Dev 上跑 backfill SQL dry-run

Team C 的 SQL 写好了从未在任何环境实际跑过。

- [ ] SSH 进 dev DB，跑 `01-dry-run.sql`，确认能输出"NEEDS BACKFILL"列表
- [ ] **预期**：dev 上目前 max_output_tokens 全是 32768 占位值，所以 dry-run 应该显示**0 行需要 backfill**（因为 SQL 用 `IS NULL OR = 0` 过滤）
- [ ] 如果想完整测试 SQL 效果，可以**先把 dev 上某个 LLM 的 max_output_tokens 临时设成 NULL**，再跑 dry-run + apply，验证 LIKE 模式能命中。完了之后恢复。
- [ ] 跑完 `03-verify.sql`，检查输出格式
- [ ] **测一次 rollback**：跑 `04-rollback.sql`，验证能正确把刚 apply 的字段还原

### 1.4 Dev migration rollback 演练

如果 prod 部署出问题，最坏情况要回滚 schema。**先在 dev 演练一次**才知道回滚 SQL 真能跑。

- [ ] dev 上备份 4 张新表 + credit_reservation 表（mysqldump）
- [ ] 跑 rollback migration（应该在 `migrations/` 目录，文件名应该是 `*_rollback.sql`）
- [ ] 验证：4 张表被删，`credit_reservation` 6 个字段被删
- [ ] 重新跑 forward migration，确认能恢复
- [ ] 比对数据是否完整（如果 rollback 设计良好，原数据应该不丢）

**Gate 1 — Phase 2 准入**：
- ✅ 1.1-1.4 全部完成
- ✅ 没发现新 P0/P1 bug
- ✅ 如果发现 bug，已修复并 merge 到 develop

---

## §3 Phase 2 — QA 部署 + 验证

**目标**：在 QA 环境（49.233.219.254:9093）跑一遍 release 分支构建出来的产物，验证镜像本身没问题。

**入场条件**：
- ✅ Gate 1 通过
- ✅ develop 测试全绿，没未提交改动

**任务清单**：

### 2.1 合 develop → release

```bash
git checkout release
git merge develop --no-ff -m "Release v2.1.13: context-budget-compression + hotfix bundle"
```

**风险**：245 commit 合一次，可能有冲突（虽然 develop 是单线，但 release 上可能有过 hotfix 直接合）。

- [ ] 解决冲突
- [ ] 检查 merge 后 `task lint` + `go test ./...` 仍绿

### 2.2 QA 自动部署

```bash
git push origin release
```
- [ ] 等 CI 跑完（约 4-5 分钟）
- [ ] 看 dockerhub 推上来的新 tag
- [ ] SSH 进 QA 服务器，确认容器跑起来 healthy

### 2.3 QA migration + backfill

⚠️ **顺序敏感**：必须先 migration 再启动新代码，否则启动时可能因找不到表/字段挂掉。

如果 CI 自动部署没自动跑 migration，需要手动跑：

```bash
# 备份
ssh qa "docker exec numind-mysql-qa mysqldump -uroot -p... numind-qa > /backup/pre-r3-$(date +%Y%m%d).sql"

# Migration
ssh qa "docker exec -i numind-mysql-qa mysql -uroot -p... numind-qa < migrations/20260425_172000_context_budget_compression.sql"

# Backfill (max_output_tokens)
ssh qa "docker exec -i numind-mysql-qa mysql -uroot -p... numind-qa < scripts/2026-04-27-context-budget-max-output-backfill/02-apply.sql"

# Verify
ssh qa "docker exec -i numind-mysql-qa mysql -uroot -p... numind-qa < scripts/2026-04-27-context-budget-max-output-backfill/03-verify.sql"
```

- [ ] 重启 QA 服务器（让代码读到新 schema）
- [ ] 确认 startup log 无错

### 2.4 QA 跑全套 S5 验证

复用 `docs/superpowers/verification/admin-api-smoke-2026-04-27.md` + `context-budget-compression-s5.md` 的验证清单，**逐项重跑**：

- [ ] Admin API smoke（9 endpoints，33+ 检查）
- [ ] 真实 chatbot/SOP/SalesRAG 调用，验证全链路
- [ ] Langfuse trace metadata 验证（11 budget keys）
- [ ] Playwright admin spec 4/4 PASS（BASE_URL 改成 QA 的 admin URL）
- [ ] gstack /qa 用户输入计数器（可选，之前 dev 跳过了）

### 2.5 测 245 commit 里 hotfix 核心路径

不能假设其他 hotfix 自带验证就够了。在 QA 上至少跑一遍：

- [ ] 微信支付下单（prod-wechat-pubkey-config-missing 影响）
- [ ] 支付宝下单（prod-alipay-config-missing 影响）
- [ ] 客户列表（customer-list-remove-limit-cap 影响）
- [ ] B2B2C 父账户帮子账户开通会员（B2B 流程，是否被 245 commit 间接影响）
- [ ] credit_multiplier 调整（feat/credit-multiplier-per-model 影响——这是新 feat，要单独验证）

**Gate 2 — Phase 3 准入**：
- ✅ 2.1-2.5 全部完成
- ✅ QA 上 24h 内没出现 P0 异常告警（context_budget_event 错误率、reservation 卡住率）
- ✅ 你主观觉得"99% 没问题"

---

## §4 Phase 3 — Prod 部署预案

**目标**：把 prod 部署变成"按 runbook 一步步执行，无临场决策"。

**入场条件**：Gate 2 通过

**任务清单**：

### 3.1 写 prod-deploy runbook

新建 `docs/superpowers/runbooks/context-budget-prod-deploy.md`，内容：

- 部署窗口选择（低流量时段）
- 预备 SSH 终端 + DB 终端 + 一份 prod 状态快照 SQL
- 完整命令列表，每步预期输出
- 回滚决策树（什么情况下回滚 / 什么情况下 forward fix）
- canary 验证脚本

### 3.2 准备 prod 最终版 SQL

- [ ] **prod migration**：检查 `migrations/20260425_172000_context_budget_compression.sql` 是否需要根据 prod 实际 schema（比如 user_id 类型差异）调整
- [ ] **prod backfill**：你已经手动改过 admin UI 的 max_output_tokens。重新跑 `01-dry-run.sql` 在 prod 上看实际剩多少 NULL（理论上只剩 4 个 deprecated + 1 个 embed）。决定：
  - 选项 A：跑 backfill 一并把 deprecated 也补上（避免 admin UI is_active bug 导致 deprecated 被路由）
  - 选项 B：跳过 backfill（你已手动改完了，没必要）
  - 推荐 A——SQL 是 idempotent 的（IS NULL OR =0 过滤），多跑一次只影响那 4 个 NULL 的 deprecated 模型，零副作用

### 3.3 回滚预案

- [ ] 写明：什么情况触发回滚
   - schema migration 失败
   - 启动失败（启动日志报 panic）
   - canary 第一个调用 5xx 或 reservation 卡住
- [ ] 回滚动作清单：
   - 重新部署 v2.1.12 镜像（docker run 指定旧 tag）
   - 跑 rollback migration SQL（删 4 张新表 + 删 6 个新字段）
   - 验证 R2 路径仍可用（旧 chatbot 调用走老 coefficient 流）

**Gate 3 — Phase 4 准入**：
- ✅ runbook 写完
- ✅ migration / backfill SQL 准备就绪
- ✅ 回滚预案准备就绪
- ✅ 你选好了部署时段

---

## §5 Phase 4 — Prod 部署 + Canary

**目标**：把代码 + schema 推到 prod，验证全链路工作。

**入场条件**：Gate 3 通过

**任务清单**：

### 4.1 选部署窗口

一人公司，建议：
- 工作日上午（精力最足，便于监控）
- 避开你已知的高流量时段（如有）
- 留 2-3 小时连续可用时间（部署 + canary + 观察初期）

### 4.2 prod DB 备份

```bash
ssh prod "docker exec numind-mysql-prod mysqldump -uroot -p... \
  --single-transaction --routines --triggers numind-prod \
  > /backup/pre-r3-$(date +%Y%m%d-%H%M).sql"
```
- [ ] 备份完成
- [ ] 备份文件大小 sanity check（不为 0、不远小于上次）

### 4.3 跑 prod migration

```bash
ssh prod "docker exec -i numind-mysql-prod mysql -uroot -p... numind-prod \
  < migrations/20260425_172000_context_budget_compression.sql"
```
- [ ] 跑完无 error
- [ ] 验证 4 张新表存在 + credit_reservation 有 6 个新字段

### 4.4 跑 prod max_output_tokens backfill

按 Phase 3.2 的决策（推荐选项 A）跑 `02-apply.sql`，再跑 `03-verify.sql` 确认。

### 4.5 打 tag → CI 部署

```bash
git checkout release
git tag v2.1.13 -m "Release: context-budget-compression + hotfix bundle"
git push origin v2.1.13
```
- [ ] 等 CI 跑完
- [ ] SSH 进 prod 确认容器 healthy
- [ ] 看启动 log 无 panic

### 4.6 Canary 真实调用验证

按 Phase 1.1 的三个业务路径，在 prod 各跑一次（用 admin 账号，自己当 canary 用户）：

- [ ] chatbot：reservation 写入 + reconcile + usage_record + Langfuse trace 全链路 OK
- [ ] SOP：同上
- [ ] SalesRAG：同上
- [ ] 验证 SQL：
   ```sql
   SELECT estimation_source, status, COUNT(*) FROM credit_reservation
   WHERE created_at >= NOW() - INTERVAL 1 HOUR GROUP BY estimation_source, status;
   ```
   预期：有 `estimation_source='context_budget' status='reconciled'` 的行

### 4.7 24h 观察期

- [ ] 第 1 / 4 / 12 / 24 小时各跑一次：
   - `SELECT status, COUNT(*) FROM credit_reservation WHERE created_at >= NOW() - INTERVAL X HOUR GROUP BY status`（看有没有 orphan reserved）
   - `SELECT status, COUNT(*) FROM context_budget_event WHERE created_at >= NOW() - INTERVAL X HOUR GROUP BY status`（看 error 率）
   - 抽 1-2 个 trace_id 在 Langfuse UI 验证 metadata 完整
- [ ] 异常处理：
   - orphan reserved > 1%：定位原因（可能 finalize 路径有遗漏）
   - context_budget_event error 率 > 5%：可能 max_output_tokens 还有缺失，立即 SQL 补
   - 如果 P0 风险，按 §3.3 回滚

**Gate 4 — Phase 5 准入**：
- ✅ Canary 全 PASS
- ✅ 24h 观察期无 P0 异常
- ✅ Manifest 更新 `stage: S6-done`

---

## §6 Phase 5 — 稳定 + 第一次调参（2 周后）

**目标**：让真实流量积累 14 天，第一次调 token 估算系数。

**入场条件**：Gate 4 通过 + prod 流量积累 14 天

**任务清单**：

### 5.1 跑 calibration runbook 第 1 轮

按 `docs/superpowers/runbooks/context-budget-calibration.md` 执行：
- 拉数据 → 出建议表 → 你拍板调哪几个桶 → admin API 创建新 profile 版本 → 验证 → 归档报告

预期：第 1 轮调参后，多数桶 P50 应该从 1.30+（保守高估）收敛到 1.10 左右；P90 从 1.50+ 收敛到 1.25 左右。

### 5.2 后续节奏

- 上线后 30 天：第 2 轮调参（应该收敛到 P50 1.05、P90 1.15 左右）
- 上线后 60 天：第 3 轮（应达 spec §4.3 标准 P50≤5%、P90≤10%）
- 60 天后：季度调参，进入维护期

**最终 Gate**：连续两轮调参后所有桶 P50 ≤ 5% → manifest `stage: S7-done`

---

## §7 关键风险 + 缓解

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| Migration 失败（SQL 错） | 低 | P0 | Phase 1.4 dev 演练 + Phase 2.3 QA 实跑 |
| 启动失败（schema 不一致） | 低 | P0 | Phase 4.3 先 migration 再启动；备用 v2.1.12 镜像 |
| canary 时发现 calibration_ratio 大量 NULL | 中 | P1 | 第一次 reservation 时 token_profile_id 还没匹配上是预期的；24h 观察后再判断 |
| 245 commit 里某个 hotfix 在 prod 表现异常 | 中 | P1 | Phase 2.5 已在 QA 验证；如发现立即灰度回滚 v2.1.12 |
| prod admin UI is_active 改不进 DB（已知 backlog） | 高（已知） | P2 | 接受。需要 deactive 时直接 SQL 改。上 prod 后第一时间评估是否优先解决 |
| Calibration 数据不足（流量太低） | 中 | P2 | 14 天后如果总样本 < 1000，延后调参，每周再看 |

---

## §8 时间预算

最快路径（不算等待时间）：

```
Phase 1: 1 天工作量（密集）
Phase 2: 0.5-1 天 + QA 24h 观察
Phase 3: 0.5 天
Phase 4: 1 天部署 + 24h 观察
Phase 5: 上线 14 天后开始
```

**最早 prod 上线日：2026-05-02**（4 天后），假设你今天开始干。
**保守 prod 上线日：2026-05-05-07**（一周左右），留出 buffer。

---

## §9 触发恢复

未来 session 想继续这个计划时，提示词：

```
按 docs/superpowers/plans/2026-04-29-context-budget-s6-prod-rollout-plan.md 继续 S6 部署
```

或具体某个阶段：

```
执行 S6 部署 Phase 1 dev 加强测试
```

新 session 的 AI 会：
1. 读本计划文件
2. 读 manifest 确认当前 stage
3. 找到上次中断点
4. 按当前 Phase 的任务清单继续

---

*最后更新：2026-04-29*
*状态：planning（等用户确认是否启动）*
