# 非 Agent 改动回植 Prod — 部署记录 + Runbook（2026-06-02）

> **一句话**：把 develop 上**与 agent mode 无关**的一小撮已完成功能，回植到「no-agent 发布线」上线 prod；agent mode 继续**全程挡在 prod 外**（它还有 5 条上线红线未关闭，见 §2）。
>
> **本文档双重用途**：① 决策与背景的**回溯记录**；② 正式执行时可对着走的 **runbook**。执行结果回填到 §8。
>
> **决策**：用户 2026-06-02 在评估 A/B/C 三方案后明确选 **方案 A（回植 no-agent 发布线）**。
>
> **关联文档**：
> - `docs/agent-mode/agent-mode-prod-readiness-test-plan.md`（agent 上线就绪计划 / 5 条红线）
> - `docs/agent-mode/go-live-checklist.md`、`docs/agent-mode/prod-migration-runbook.md`
> - `docs/deployment/ai-service-manager-migration-runbook.md`（runbook 风格参考）

---

## §0 关键事实速查

| 项 | 值 |
|----|----|
| 分叉点（develop ↔ prod 线 共同祖先）| `4c0f8e40`（2026-05-19）`fix(cicd): admin health check timeout` |
| **用户 API prod 当前版本** | `v2.1.33` = `admin-v1.4.6` = `17ff3ee2`（2026-05-25）|
| **管理 API prod 当前版本** | `admin-v1.4.8` = `c41bbb01`（2026-06-02，no-agent 线最新提交）|
| 用户前端 prod | `numind-web-v3` `v1.0.28` |
| 管理前端 prod | `numind-admin-web` `v1.4.8` |
| no-agent 发布线分支命名约定 | `release-no-agent-v<版本>`（例：`release-no-agent-v2.1.32`）|
| 本次新版本号（建议）| 用户 API `v2.1.34`；管理 API 无功能变更，可不动或平移 tag |
| ⚠️ 验证盲区 | 编写时环境**未注入 `PROD_SSH_*` 凭据**，线上实际运行镜像版本**未经 SSH 核验**，以上基于 git tag 推断。正式部署前需配置 prod SSH 让 AI 核验容器镜像 tag。|

---

## §1 背景：为什么 prod ≠ develop（部署模型）

prod **不是**从 develop 直接发的。自 **2026-05-19**（`4c0f8e40`）起，prod 从 develop 分叉出一条**独立的「no-agent 发布线」**，只把"安全的非 agent 修复"用 cherry-pick / 手工回植的方式补上去，再打 tag 发布。agent mode 一直没进 prod。

```
              4c0f8e40 (2026-05-19) ← 分叉点（共同祖先）
             /         \
        develop         no-agent 发布线（只回植非 agent 安全修复）
   (agent mode +         … → v2.1.32 (5be43b41)
    所有其它，877+         → v2.1.33 / admin-v1.4.6 (17ff3ee2, 05-25) ← 用户 API prod
    commits 未上 prod)    → admin-v1.4.8 (c41bbb01, 06-02)          ← 管理 API prod
```

**两个独立 prod 部署点**（同一个 numind-server 仓库、不同 commit、不同容器）：

| 部署点 | 入口 | 端口 | 当前 prod tag | 基线 commit |
|--------|------|------|----------------|-------------|
| 用户 API | `cmd/numind` | 9095 | `v2.1.33` | `17ff3ee2` |
| 管理 API | `cmd/numind-admin` | 9099 | `admin-v1.4.8` | `c41bbb01` |

> 注意：管理 API（`c41bbb01`）比用户 API（`17ff3ee2`）多一个 commit —— per-action 计费回植（管理端专属逻辑）。故本次回植应**基于 `c41bbb01`** 起分支，避免回退该修复。

**前端两仓库**亦各自从 tag 发，且都与各自 develop 分叉（同样的"prod tag 含回植 hotfix、develop 含 agent"模式）。

---

## §2 为什么 agent **必须**继续挡在 prod 外（决策的硬约束）

依据 `docs/agent-mode/agent-mode-prod-readiness-test-plan.md`（2026-06-01，代码勘察 + 第一手验证），agent mode 当前有 **5 条 go-live 红线**：

| ID | 红线 | 影响 |
|----|------|------|
| **BLK-1** | 权限管线在非测试二进制**全局短路**（即 `remove-permission-backdoor` 后门，仍未修）| 所有工具权限 / 租户黑名单 / 危险命令拦截全失效 |
| **BLK-2** | agent run **不扣真实积分** | 三池计费对 agent 完全不生效 |
| **BLK-3** | bashvalidator 不拦 `rm -rf /`、`curl\|sh`、fork bomb；run_python 零命令校验 | **prod 一旦开沙箱即 RCE** |
| **BLK-4** | 流式主路径 `ask_user_question` 断裂、答题后卡死 | 澄清提问主路径不可用 |
| **BLK-5** | 流式路径 `currentRun` 全程 null | 状态徽标 / 取消 / 预算预警全失效 |

外加元风险：**文档、规格、现有单测全"绿"，但 prod 形态真实行为相反**（虚假信心）。

> **结论**：把 agent 挡在 prod 外不是图方便，是当前唯一安全选择，且与团队自己的就绪计划（先在 **dev** 测、关闭红线后再走分阶段灰度上线）完全一致。

---

## §3 本次要部署的非 Agent 功能点

> 判定方法：以「prod tag → develop 的**文件级 diff**」为准（回植线已含的修复在 diff 中不出现，故 diff 即"还没上 prod 的部分"）。

### 3A. numind-server 用户 API（基线 `17ff3ee2`，目标 `v2.1.34`）

| # | 功能点 | 新端点 / 迁移 | 关键文件 | 来源 commit | 纠缠 |
|---|--------|----------------|----------|-------------|------|
| 1 | **积分消耗流水**（含「任务名」富集）| `GET /v1/credits/consumption-log`；**无 schema 迁移** | `biz/credit/consumption_log.go`(新)、`contracts.go`、`types.go`、`credit_service.go`、`store/credit.go`、`controller/v1/credit/consumption_log.go`(新)、`pkg/aiservice/middleware/reservation_ref.go`(新) + `sop/chatbot/salesrag` 各一行注入 + `middleware/context_budget.go` 一行 | merge `d22dea61`（consumption-log）+ `253e86be`（task-names：`0576fb11`/`9ab8db2f`/`de36adb1`）| ⚠️ `credit_service.go`、`store/customer.go` 见 §5 |
| 2 | **父账户自助对账页** | `GET /v1/users/me/billing-report`；**无迁移** | `biz/b2b_billing/b2b_billing.go`、`controller/v1/parent_billing/billing_report.go`(新) | merge `9f2a1311` | 低（依赖字段已在 prod）|
| 3 | **客户授权模板数修正**（父账户行计数错误）| 无 | `biz/customer/customer.go`、`store/customer.go` | merge `fe7db5a6`（`e8e611b3`/`114ffc2f`/`18728a54`）| ⚠️ `store/customer.go` 重新引用 `agent_run` = **头号陷阱**（§5.1）|
| 4 | **订阅 grant-source 记账修正** | 无 | `biz/membership/subscription.go`（设置 `Source`/`GranterUserID`）| —（字段 prod 已存在）| 低 |
| 5 | **历史账单记录清理**（数据迁移，**手动**）| `migrations/20260602_120000_clean_migrated_billing_records.sql` | — | merge `c2db8607`（`4e621ad0`）| 手动跑、可回滚、金额不变；**非自动应用** |
| + | （随上述一起进的全局改动）`errno.Decode` 改用 `errors.As` | 无 | `internal/pkg/errno/errno.go` | （agent marketplace 任务引入，但全局生效）| 影响**全系统**错误码解析，须回归非 agent 错误路径（§5.4）|

### 3B. numind-server 管理 API（基线 `c41bbb01`）

✅ **本轮无非 agent 改动需发**。per-action 计费、eventType 标签、sidebar 路径 404 均已通过回植进 `admin-v1.4.8`。develop 相对它的差异**只剩 agent**（AgentMonitoring、合规规则）+ 测试文件。

### 3C. numind-web-v3 用户前端（基线 `v1.0.28`，需新 tag）

| # | 功能点 | 关键文件 | 入口纠缠 |
|---|--------|----------|----------|
| 1 | **积分消耗记录弹窗**（配合 3A.1）| `components/credit/CreditConsumptionLogModal.vue`、`stores/consumptionLog.ts`、`utils/consumptionType.ts`、`api/credits.ts` | 入口在 `SettingsView`（干净）|
| 2 | **父账户对账页**（配合 3A.2，`/customers/billing`）| `views/CustomersBillingView.vue`、`api/parent.ts` | 路由在 `router/index.ts`（与 agent 路由混，§5.3）|
| 3 | **父账户权限自管理修正** | `CustomersView.vue`、`GrantMembershipModal.vue`、`customersPermissionDiff.ts`、`customersSelfManagement.ts` | 低 |
| 4 | chatbot → 「AI 助手」改名（纯文案）| `ChatbotList.vue`、`ChatbotChat.vue` | 与 agent 上线术语联动，谨慎 |

### 3D. numind-admin-web 管理前端（基线 `v1.4.8`）

✅ **本轮无非 agent 改动需发**（B2B eventType 标签、sidebar 路径修正均已回植）。develop 差异只剩 agent（`AgentMonitoring.vue`、`compliance/*`）+ 部署脚本。

---

## §4 已在 prod、无需重发（避免重复部署）

no-agent 发布线已含：`drop-billing-account-dead-table`、`customer-stats-real-data/softdelete`、`customer-page-polish`、`b2b-billing-rules-rewrite`、`b2b-billing-action-month`(per-action 计费)、`salesrag-kb-public`、`authorized-templates-count`、`salesrag-embed-dim-2048`、`sop-salesrag-parent-scope`、`settings-credits-display`、父账户帮开通会员（self-grant）等。

---

## §5 ⚠️ 纠缠点与处理方式（方案 A 的核心风险，"容易出错"就在这）

develop 上非 agent 功能与 agent 是**交织开发**的。回植时**不能整文件复制**，要按下面规则手术式处理：

### 5.1 `store/customer.go` 重新引用 `agent_run`（**头号陷阱**）
- **现象**：no-agent 线当初**特意删掉**了客户活跃数统计查询里的 `agent_run` 子查询（commit `44061506`，注释写明"hotfix 分支不含 agent 表，引用未建的表会让整个 query 报 Table doesn't exist"）。develop 版本又把 `OR EXISTS (SELECT 1 FROM agent_run …)` 加回来了。
- **风险**：照搬 develop 这个文件 → prod **客户统计查询直接报「表不存在」**。
- **处理**：回植 #3（模板数修正）时，**只取 `biz/customer/customer.go` 的计数修正**，`store/customer.go` 的查询**保持 no-agent 线现状（不带 `agent_run` 分支）**；或给该子查询加"表不存在则跳过"的保护（更一劳永逸，见 §7）。

### 5.2 `biz/credit/credit_service.go` 混写 consumption-log + agent 计费
- **现象**：非 agent 的 `ListConsumptionLog`/`ReferenceID` 逻辑与 agent 计费（`ReserveAgentTest`/`ReconcileAgentTest`/`SetAdminTestConsumer`/`AdminTestPool`）写在同一批方法里；`GetBalance` 被改成追加 agent `AdminTestPool`。
- **处理**：回植时**只取 consumption-log 相关**；**不带** `ReserveAgentTest`/`AdminTestPool` 及 `GetBalance` 的 agent 池追加（这些依赖 `credit_admin_test_grant` 表 + `admin_test` CHECK 约束迁移，均为 agent，prod 没有）。需逐方法手工拆。

### 5.3 web-v3 agent UI 无 feature flag、入口文件交织
- **现象**：agent 入口（首页「AI 智能体」卡片、侧边栏「技能市场」、配置页 agent/Skill 标签、4 组 agent 路由）与要发的非 agent 功能**写在同一批 shared 文件里、逐行交织**，且 agent UI **没有开关**。涉及：`router/index.ts`、`HomeView.vue`、`AppSidebar.vue`、`ConfigLayout.vue`。
- **风险**：直接发 develop 前端 → 父账户登录后能看到、点开 agent 菜单（后端无 agent 端点 → 点了报错，体验崩）。
- **处理**：回植 web-v3 时**手工去掉这些文件里的 agent 路由 / 入口 / 标签**，只保留 #1~#4 的非 agent 部分。**改完必须用 Playwright 跑一遍**（登录 → 设置看消耗记录 → /customers/billing → 确认无 agent 菜单残留）。

### 5.4 `errno.go` 全局 Decode 改动
- **现象**：`Decode` 从裸 type-switch 改为 `errors.As`，影响两个 API 所有错误码解析（agent 引入但全局生效）。
- **处理**：跟着回植（否则消耗流水等新代码的错误包装可能解析不对），但**回归测已有非 agent 错误路径**（登录失败、积分不足、参数校验等返回码不变）。

### 5.5 agent migration 动到共享表（**部署纪律**）
- **现象**：多个 agent 迁移不只是建 agent 表，还会改 `credit_transaction` CHECK 约束、`ai_service` capability seed、AI 路由表 —— 这些表 SOP/对话/销售都在用。
- **处理**：prod **只跑** §3A.5 那 1 个手动迁移（`clean_migrated_billing_records`）；**严禁**在 prod 跑任何 agent migration。回植分支里也不应包含 agent migration 文件。

---

## §6 执行 Runbook

> 前置：配置 `PROD_SSH_*` 凭据并 SSH 核验线上实际镜像版本（§0 验证盲区）。所有"禁止碰 prod 数据库的写"以 CLAUDE.md 硬规则为准；本 runbook 的 prod 操作由 AI SSH 执行，不让非技术用户手动操作服务器。

### 6.1 numind-server 回植（用户 API → v2.1.34）

1. 从 no-agent 线最新提交起分支：`git switch -c release-no-agent-v2.1.34 c41bbb01`（**不走 ndf-start**——这是发布线操作，非 develop feature；遵循既有 no-agent 线实践）。
2. 按 §3A 逐功能回植：
   - 能干净 cherry-pick 的（如 `parent_billing` 控制器、`consumption_log.go` 新文件）直接 `git cherry-pick <commit>` 或 `git checkout <commit> -- <file>`。
   - **纠缠文件**（`store/customer.go`、`credit_service.go`）按 §5.1/§5.2 **手工编辑**，只保留非 agent 部分。
   - 带上 `errno.go`（§5.4）。
   - **不带**任何 agent migration、agent 包、agent 路由。
3. 校验只动了预期文件：`git diff c41bbb01..HEAD --stat` 应只含 §3A 的文件 + `errno.go`，**无 `biz/agent/`、无 agent migration**。
4. 构建 + 测试：`task lint` → `go test ./...`（重点跑 `biz/credit`、`biz/b2b_billing`、`biz/customer`、`aiservice/middleware`）→ `task build`。
5. 打 tag：`git tag v2.1.34`。
6. 部署：`/deploy-prod server`（要求 HEAD 在 `v*` tag）。
7. 管理 API：本轮无变更，**不重新部署**（保持 `admin-v1.4.8`）。如需版本对齐，可在同一 commit 另打 `admin-v1.4.9` 后 `/deploy-prod admin`（可选）。

### 6.2 手动迁移（可选、独立于代码部署）

`clean_migrated_billing_records` 为**手动、非自动应用**、archive-before-delete、可回滚、金额不变。按需在 prod DB 执行（先在 prod 克隆/只读验证），**与上面代码部署解耦**。

### 6.3 numind-web-v3 回植（新 tag）

1. 从 web-v3 prod tag 起分支：`git switch -c release-no-agent-<新版本> v1.0.28`。
2. 回植 §3C 的非 agent 文件；按 §5.3 **手工剔除 `router/index.ts`/`HomeView.vue`/`AppSidebar.vue`/`ConfigLayout.vue` 里的 agent 入口**。
3. `npm run lint && npm run type-check` → Playwright 验：登录 / 设置看消耗记录 / `/customers/billing` / **确认无 agent 菜单残留**。
4. 打 tag → `/deploy-prod`。

### 6.4 部署后验证（prod）

- 用户 API：`/healthz` ok；`GET /v1/credits/consumption-log` 返回 401（未登录）= 端点已上线；`GET /v1/users/me/billing-report` 同理。
- 真实用户登录：设置里能看消耗流水（历史记录回退通用名、**新记录**显示具体任务名）；父账户能看 `/customers/billing` 对账页；客户列表父账户行模板数正确。
- **回归**：客户统计 / 活跃数查询正常（确认 §5.1 未踩雷）；登录失败、积分不足等错误码返回不变（确认 §5.4）。
- **确认无 agent 泄漏**：前端无 agent / 技能市场入口；`/v1/agent/*` 在 prod 应不存在（404）。

### 6.5 回滚

- 代码：prod 健康检查失败 `deploy-remote.sh` 自动 rollback 到旧镜像；手动可 `/deploy-prod` 旧 tag（用户 `v2.1.33`）。
- 迁移：`clean_migrated_billing_records` 有 rollback（archive 表恢复）。

---

## §7 对「未来 agent 上线」的影响评估（回溯用）

**核心判断：方案 A 既不明显帮到、也不明显拖累 agent 上线。**

- **A 制造的"分叉"是临时脚手架，上线时丢弃、不累积成债**：agent 真就绪那天，prod 改成**直接从 develop 发布**，no-agent 线退役。A 期间为解纠缠做的手工改（如 `customer.go` 去 agent_run）会被 develop 版本**直接覆盖**（那时 agent_run 表已存在），属一次性。
- **agent 上线的"影响大"是 agent 自身状态决定的**，与 A 无关：要关 5 红线、跑会动共享表的迁移、武装沙箱（激活 RCE 风险）、接通计费、走 Wave 0~5 + 分阶段灰度（见就绪计划 §6）。
- **A 唯一真实成本 = 回植税**：从现在到 agent 上线，每个新非 agent 功能都要回植 + 解一次纠缠。窗口越长、功能越多，税越高。

**降低回植税 + 让 agent 上线更顺的高杠杆动作（不分 A/B/C 都值得做）**：
1. **优先修权限后门 BLK-1**（已登记 `remove-permission-backdoor` hotfix，未实现）——它现在在 dev/develop 上是实打实安全洞，也是 agent 上线头号 gate。
2. **在 develop 上把 agent 改成"可干净隔离"**：例如给 `store/customer.go` 的 `agent_run` 查询加"表不存在则跳过"保护 —— 让以后每次回植都不用再手工切这一刀（§5.1 一劳永逸版）。
3. **agent feature flag 留到上线准备阶段再建**（现在 agent 还在天天变，提前建会烂）。

---

## §8 执行记录（执行时回填）

| 日期 | 操作 | 结果 / tag / commit | 操作人 |
|------|------|---------------------|--------|
| 2026-06-02 | 决策 + 本文档归档 | 选定方案 A；文档落 `docs/deployment/` | user + AI |
| 2026-06-03 | SSH 核验线上基线 | 实测 prod 用户 API=`17ff3ee2`、管理 API=`c41bbb01`、web-v3=`v1.0.28`、admin-web=`v1.4.8`，与 git tag 推断一致 | AI |
| 2026-06-03 | server 回植 | `release-no-agent-v2.1.34`（基线 `c41bbb01` + 7 commit，HEAD `31bf6ece`，tag `v2.1.34`）；4 cherry-pick（消耗流水/任务名/模板数/账单清理）+ 手工（父账户对账+订阅记账+errno 解包）；build+test(46 pass/0 fail)+lint 全绿；agent 零泄漏、`store/customer.go` 未引用 agent_run | AI |
| 2026-06-03 | `/deploy-prod server` | ✅ 成功；镜像 `numind-server:v2.1.34-31bf6ece` healthy；回滚目标 `17ff3ee2`（旧镜像保留）；tag+分支已推 origin | AI |
| 2026-06-03 | prod 验证（用户 API） | `/healthz`=200；`GET /v1/credits/consumption-log`=401、`GET /v1/users/me/billing-report`=401（路由已上线）；回归 `/v1/credits/balance`=401；`/v1/agent/skills`=404（agent 确认在外）| AI |
| 2026-06-03 | web-v3 回植 + 部署 | `release-no-agent-v1.0.29`（基线 `v1.0.28` + 12 cherry-pick，HEAD `a1d2143`，tag `v1.0.29`）：消耗流水弹窗(+任务名/类型列/悬停) + 父账户对账页(`/customers/billing`, parentOnly 守卫) + 父账户权限显示/取消授权修正 + 设置入口 + 权限弹窗标签改名(SOP→AI 工作流/智能体→AI 助手，用户确认保留)；lint+type-check+build 全绿；**agent UI 零泄漏**（router 无 agent 路由、HomeView/AppSidebar/ConfigLayout 未动、无 agent/marketplace/skill 文件）。`/deploy-prod`：✅ 镜像 `numind-web-v3:v1.0.29-a1d2143` healthy，回滚目标 `v1.0.28-cca28a5`；nginx/health=200、SPA index 200、index.html 引用新 bundle hash 匹配本地构建；公网 youshu.asia=200；tag+分支已推 origin | AI |
| 2026-06-03 | 手动迁移 clean_migrated_billing_records | **核验发现已先期应用于 prod**（非本次/非 v2.1.34 部署跑的，应是 06-02 账单清理时一并执行）。只读核验确认状态健康：0 残留 placeholder、48 条 migcleaned 合并记录、archive 表 73 条原始记录（可回滚）、金额公式 0 异常行。**无需再跑（再跑为幂等 no-op）**；未对 prod 做任何写操作 | AI |

---

*创建于 2026-06-02。基线：develop HEAD `7b1911c4`；prod 用户 API `v2.1.33`(`17ff3ee2`)、管理 API `admin-v1.4.8`(`c41bbb01`)。本文档为部署设计 + 决策记录，执行结果以 §8 回填为准。*
