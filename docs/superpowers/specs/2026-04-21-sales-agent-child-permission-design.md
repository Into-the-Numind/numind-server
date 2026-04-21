# 销售智能体 — 父账号子账号权限管控 — 技术设计

**Feature ID**: `sales-agent-child-permission`
**Date**: 2026-04-21
**Stage**: S2
**NDF version**: 1.1
**Upstream PRD**: `numind-server/proposals/sales-agent-child-permission-proposal.md` §4

---

## §1 问题与目标

SOP 和通用智能体已有父账号对子账号的"运行权限"管控（`user_template_permission` / `user_chatbot_permission`）。销售智能体（SalesRAG）是唯一漏网功能 — `CheckSalesPermission` 硬编码返回 `true`，所有登录用户都能使用。

本设计把销售智能体纳入同一套权限体系，**复用已存在的基础设施**：`user_feature_permission` 表 + `FeatureKeySalesAgent = "sales_agent"` 常量 + `HasFeaturePermission` store/biz 函数 + `FeaturePermission` middleware + 前端客户管理弹窗"销售智能体"开关。

**核心架构决策**：**不新增任何基础设施**，只做"挂 gate + 修硬编码 + 加测试"。

---

## §2 架构图

```
HTTP Request
  ↓
authGroup (authGroup.Use(AuthToken))
  │
  ├─ GET /sales-rag/check-permission       ← authGroup 直辖，**不挂 gate**（D1）
  │    └─ controller 调用 ctrl.b.Customers().CheckFeaturePermission()  ← biz 层（C2 明确）
  │
  └─ salesGroup := authGroup.Group("/sales-rag")
       salesGroup.Use(middleware.FeaturePermission(model.FeatureKeySalesAgent))   ← 新增 C1
       │
       ├─ POST /sales-rag/ingest                        → gate
       ├─ GET  /sales-rag/documents                     → gate
       ├─ ... (共 27 个端点，§4 列表)
       └─ POST /sales-rag/ocr                           → gate
```

### 执行顺序（Gin middleware chain）

1. `authGroup.Use(AuthToken)` → 未登录 401
2. `salesGroup.Use(FeaturePermission(FeatureKeySalesAgent))` → 父账号直过 / 子账号有 grant 直过 / 子账号无 grant 403
3. Controller handler 执行 → 内部的 `creditBiz.CanPerformAIOperation` 积分预检
4. Biz 层调用 → LLM / 向量检索

**D5 保证**：权限拒绝发生在 credit 预检之前，无权拒绝不消耗 credit 估算。

---

## §3 数据模型

**无任何 schema 变更。** 下列结构已全部存在。

### `user_feature_permission` 表（现存）

```sql
CREATE TABLE user_feature_permission (
    id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    parent_user_id BIGINT UNSIGNED NOT NULL,
    sub_user_id BIGINT UNSIGNED NOT NULL,
    feature_key VARCHAR(64) NOT NULL,
    created_at DATETIME,
    updated_at DATETIME,
    deleted_at DATETIME,
    INDEX idx_parent_sub (parent_user_id, sub_user_id),
    UNIQUE KEY idx_sub_feature_unique (sub_user_id, feature_key),
    INDEX idx_sub_feature (sub_user_id, feature_key)
);
```

### `FeatureKeySalesAgent` 常量（现存）

```go
// numind-server/internal/pkg/model/user_feature_permission.go:7
const FeatureKeySalesAgent = "sales_agent"
```

### 基础设施函数（现存，不改）

| 层 | 函数 | 位置 |
|---|------|------|
| store | `HasFeaturePermission(ctx, userID, featureKey) (bool, error)` | `store/customer.go:340-365` |
| store | `GrantFeatures(ctx, parentID, subID, keys) error` | `store/customer.go:368` |
| store | `RevokeFeatures(ctx, parentID, subID, keys) error` | `store/customer.go:398` |
| biz | `customerBiz.CheckFeaturePermission(ctx, userID, featureKey)` | `biz/customer/customer.go:341` |
| biz | `customerBiz.GrantFeatures` / `RevokeFeatures` | `biz/customer/customer.go:344-365` |
| middleware | `FeaturePermission(featureKey) gin.HandlerFunc` | `middleware/middleware.go:212-238` |

---

## §4 端点清单（全集 27 个 salesGroup 端点）

> **Reviewer Q2 修正**：S1 PRD "11+" 是低估，实际 27 个，全部在 `router.go:101-137` 注册到 `salesGroup`，全部被 C1 的 `.Use()` 覆盖。

| 分类 | 端点 | 方法 | Handler |
|------|------|------|---------|
| **文档 (6)** | `/sales-rag/ingest` | POST | Ingest |
|  | `/sales-rag/documents` | GET | ListDocuments |
|  | `/sales-rag/documents/:id` | GET | GetDocument |
|  | `/sales-rag/documents/:id/chunks` | GET | ListChunks |
|  | `/sales-rag/documents/:id` | PUT | UpdateDocument |
|  | `/sales-rag/documents/:id` | DELETE | DeleteDocument |
| **观点 (1)** | `/sales-rag/opinion-tracks` | GET | ListOpinionTracks |
| **会话 (8)** | `/sales-rag/sessions` | POST | CreateSession |
|  | `/sales-rag/sessions` | GET | ListSessions |
|  | `/sales-rag/sessions/:id` | GET | GetSession |
|  | `/sales-rag/sessions/:id` | PUT | UpdateSession |
|  | `/sales-rag/sessions/:id` | DELETE | DeleteSession |
|  | `/sales-rag/sessions/:id/pin` | PUT | PinSession |
|  | `/sales-rag/sessions/:id/pin` | DELETE | UnpinSession |
|  | `/sales-rag/sessions/:id/rename` | PUT | RenameSession |
| **消息 (4)** | `/sales-rag/sessions/:id/chat` | POST | ChatWithSession |
|  | `/sales-rag/sessions/:id/messages` | GET | ListMessages |
|  | `/sales-rag/sessions/:id/messages/:mid/feedback` | POST | SubmitFeedback |
|  | `/sales-rag/sessions/:id/messages/:mid/feedback` | GET | GetFeedback |
| **档案 (4)** | `/sales-rag/sessions/:id/customer-profile` | PUT | UpdateCustomerProfile |
|  | `/sales-rag/sessions/:id/customer-profile` | GET | GetCustomerProfile |
|  | `/sales-rag/analyze-profile` | POST | AnalyzeProfile |
|  | `/sales-rag/analyze-profile-text` | POST | AnalyzeProfileText |
| **风格 (3)** | `/sales-rag/analyze-chat-style` | POST | AnalyzeChatStyle |
|  | `/sales-rag/analyze-chat-style` | GET | GetLanguageStyle |
|  | `/sales-rag/analyze-chat-style` | PUT | SaveLanguageStyle |
| **OCR (1)** | `/sales-rag/ocr` | POST | OCR |

### authGroup 直辖（不被 gate）

| 端点 | 方法 | Handler | 理由 |
|------|------|---------|------|
| `/sales-rag/check-permission` | GET | CheckSalesPermission | 前端用它决定是否显示销售智能体入口；被 gate 会死锁（§5 D1） |

---

## §5 越权防线（6 条 + 2 条 accepted exposure）

### D1: `check-permission` 端点不被 gate 拦截

**规则**：`/v1/sales-rag/check-permission` 必须注册在 `authGroup`（现状），**不可**被移到 `salesGroup` 下。否则未授权子账号会直接 403，前端永远拿不到"未授权"信号，无法显示"请联系管理员"UI 分支。

**测试覆盖**：C5 httptest — 子账号无权限时 `GET /sales-rag/check-permission` 应 200 且 body `has_permission: false`（不是 403）。

### D2: 所有运行端点统一被 gate 覆盖

**规则**：§4 表格 27 个 `salesGroup` 端点必须**全部**被 `salesGroup.Use(...)` 覆盖。Gin 的 `Use()` 语义对 group 下所有路由生效（包括 `.Use()` 之前和之后注册的），所以只需一次 `.Use()`。

**测试覆盖**：C5 httptest 抽样 3 个不同类别端点（`/documents` GET / `/sessions/:id/chat` POST / `/ocr` POST）验证子账号无权限 → 403。

### D3: biz 层无 controller 之外的**用户级**入口

**规则**：`SalesRAG()` biz 层所有"用户可触发"的调用必须经过 controller（从而经过 middleware gate）。

**验证结果**（reviewer 独立 grep 确认）：
- 27 处 `.SalesRAG().` 调用 100% 来自 `controller/v1/salesrag/sales_rag.go`
- 第 28 处来自 `biz/biz.go:226` — **opinion track seeder**，系统初始化时跑一次，`userID = 0`，不经 HTTP 请求，不受 gate 影响。不构成越权漏洞（系统操作）。

**spec 接受**：第 28 处 seeder 是 accepted side-entry（系统级，不影响权限模型）。

### D4: 父账号自动通过

**规则**：`parent_user_id IS NULL` 的用户无需 `user_feature_permission` 记录即可通过 gate。

**代码保证**：`store/customer.go:342-365` `HasFeaturePermission` 先查 `user.ParentUserID`，若为 `nil` 直接 `return true, nil`，根本不查表。已被 content_monitor 功能线上验证。

### D5: Gate 在 credit 预检之前

**规则**：无权访问的请求不消耗 credit 估算。

**代码保证**：Gin middleware chain 顺序 — `AuthToken` → `FeaturePermission` → handler → `creditBiz.CanPerformAIOperation`。Handler 内部的 credit 逻辑在 middleware 401/403 之后才执行。

### D6: `check-permission` 与 gate 使用同一条查询路径

**规则**：C2 的 `CheckSalesPermission` controller 和 middleware 的 `FeaturePermission` 必须返回一致的判断（否则出现"UI 显示有权但调用被拒"或反之）。

**实现约束（reviewer Q6 修正）**：
- C2 controller **必须**调用 `ctrl.b.Customers().CheckFeaturePermission(ctx, user.ID, model.FeatureKeySalesAgent)`（biz 层）
- **禁止**仿照 middleware.go:222 直接 `store.S.Customers().HasFeaturePermission(...)`（该路径本身违反 `controller → biz → store` 单向规则，不可扩大违规面）
- content_monitor 先例（`biz/monitor/monitor.go:146`）走的是 biz 路径，对齐之

**一致性保证**：biz 层 `CheckFeaturePermission` 是对 store `HasFeaturePermission` 的薄包装（`biz/customer/customer.go:341` 单行 return），与 middleware 直调 store 在**语义**上同源（同一 SQL），在**路径**上不同但等价。

### Accepted Exposure 1: `/v1/ali/bailian/*` 百炼文件上传

**现象**（reviewer Q7 发现）：`/v1/ali/bailian/lease` / `/v1/ali/bailian/confirm` 挂在 `authGroup` 而非 `salesGroup`，未授权子账号可上传文件到百炼。

**评估**：
- 文件上传本身不消耗 sales-agent credit、不创建 sales session、不触发 LLM 调用
- 上传后的文件只能通过 `/sales-rag/ingest`（被 gate）或 `/sales-rag/sessions/:id/chat`（被 gate）引用
- 未授权子账号无法把孤儿文件带入 sales 上下文

**决策**：**不处理**，不视为越权漏洞。若未来把百炼文件上传纳入 SalesRAG 专属资源配额，再重新评估。本 spec 记录此 exposure 以免未来被当作"漏洞回归"。

### Accepted Exposure 2: SSE 宽松模式（业务决策 A）

**现象**：子账号正在进行的 SSE 对话，父账号撤权后当前流不中断（下一次请求才被 403）。

**评估**：
- 与 chatbot `HasChatbotPermission` 已确立的语义一致
- 最多一次对话的延迟生效
- 严格模式（SSE 循环内周期查权限）侵入性高，收益低

**决策**：**宽松模式**（S1 业务决策 A）。验收标准 AS-2 已按此语义编写。

---

## §6 组件设计（5 个）

### C1: router.go 挂 middleware

**文件**：`numind-server/internal/numind/router.go`
**位置**：`salesGroup := authGroup.Group("/sales-rag")` 定义之后、第一个 `salesGroup.POST(...)` 之前
**改动**：+1 行

```go
salesGroup := authGroup.Group("/sales-rag")
salesGroup.Use(importMw.FeaturePermission(model.FeatureKeySalesAgent))    // ← 新增
{
    salesGroup.POST("/ingest", salesRAGc.Ingest)
    // ... 其余 26 个路由不变
}
```

**对齐先例**：`router.go:332` `monitorGroup.Use(importMw.FeaturePermission(model.FeatureKeyContentMonitor))`

### C2: sales_rag.go 修 CheckSalesPermission 硬编码

**文件**：`numind-server/internal/numind/controller/v1/salesrag/sales_rag.go`
**位置**：行 1019-1031
**改动**：±10 行

**当前代码**（硬编码）：
```go
func (ctrl *SalesRAGController) CheckSalesPermission(c *gin.Context) {
    user := middleware.GetCurrentUser(c)
    if user == nil {
        core.WriteResponse(c, errno.ErrTokenInvalid, nil)
        return
    }
    core.WriteResponse(c, nil, gin.H{
        "has_permission": true,    // ← 硬编码
    })
}
```

**新实现**（走 biz 层）：
```go
func (ctrl *SalesRAGController) CheckSalesPermission(c *gin.Context) {
    user := middleware.GetCurrentUser(c)
    if user == nil {
        core.WriteResponse(c, errno.ErrTokenInvalid, nil)
        return
    }
    hasPermission, err := ctrl.b.Customers().CheckFeaturePermission(c.Request.Context(), user.ID, model.FeatureKeySalesAgent)
    if err != nil {
        log.Errorw("CheckSalesPermission: check feature permission failed", "user_id", user.ID, "err", err)
        core.WriteResponse(c, errno.ErrInternalServer, nil)
        return
    }
    core.WriteResponse(c, nil, gin.H{
        "has_permission": hasPermission,
    })
}
```

**约束**（D6）：严禁走 `store.S.Customers().HasFeaturePermission` 绕过 biz 层。

### C3: CheckSalesPermission controller 单测

**文件（新建）**：`numind-server/internal/numind/controller/v1/salesrag/sales_rag_test.go`
**范围**：仅测 `CheckSalesPermission` 的分支行为（父账号 true / 子账号有权 true / 子账号无权 false / biz 层 err → 500）

**测试用例**：
| # | 场景 | 预期 |
|---|-----|------|
| T1 | `parent_user_id IS NULL` 用户 | 200 + `{has_permission: true}` |
| T2 | 子账号 + 有 `sales_agent` grant | 200 + `{has_permission: true}` |
| T3 | 子账号 + 无 `sales_agent` grant | 200 + `{has_permission: false}` |
| T4 | biz 层返回 err（mock DB 故障） | 500 + `ErrInternalServer` |

**实现方式**：用 mock biz 层（`biz.IBiz` 接口，mockgen 已在项目里）。

### C4: E2E Playwright 回归测试

**文件（新建）**：`numind-web-v3/e2e/sales-agent-permission.spec.ts`
**范围**：父账号开关 ↔ 子账号访问循环

**测试场景**：
| # | 步骤 | 预期 |
|---|-----|------|
| E1 | 父账号登录 → 打开客户管理弹窗 → 开"销售智能体"开关 → 保存 | API `POST /customers/sub-users/:id/features` 200 |
| E2 | 子账号登录 → 访问 `/sales` 路由 | 能进入销售智能体页（`checkSalesPermission` 返回 true） |
| E3 | 父账号关"销售智能体"开关 → 保存 | API `DELETE` 200 |
| E4 | 子账号刷新 `/sales` 页面 | 看到"未开通，请联系管理员"UI（`HomeView.vue` / `SalesView.vue` 已有此分支） |
| E5 | 子账号直接调用 `POST /sales-rag/sessions/:id/chat` | 后端 403（gate 生效） |

**凭据**：使用 `E2E_USERNAME` / `E2E_PASSWORD` 环境变量（`.claude/rules/testing.md`）。如果 dev 无现成父子账号对，E2E 需自建 fixture 或使用 mock response 模式（参考 `e2e/parent-self-grant.spec.ts` 既有 mock pattern）。

**mock 策略**：本 E2E 在 `numind-web-v3/` 仓库内，后端是 dev 真实服务。父子账号 fixture 由 test setup 创建；测试结束 cleanup。若 dev 环境不允许写入，退化为 mock responses（与 parent-self-grant 一致）。

### C5: httptest 路由 gate 集成测试（reviewer Q8 补洞）

**文件（新建）**：`numind-server/internal/numind/router_sales_gate_test.go`
**范围**：验证 `salesGroup.Use(FeaturePermission(FeatureKeySalesAgent))` 这一行对运行端点真实生效（防止"忘加 `.Use()`"回归）

**测试用例**：
| # | 场景 | 预期 |
|---|-----|------|
| H1 | 子账号无 grant + GET `/v1/sales-rag/documents` | 403 `ErrForbidden` |
| H2 | 子账号无 grant + POST `/v1/sales-rag/sessions/:id/chat` | 403 `ErrForbidden` |
| H3 | 子账号无 grant + POST `/v1/sales-rag/ocr` | 403 `ErrForbidden` |
| H4 | 子账号无 grant + GET `/v1/sales-rag/check-permission` | **200**（D1 验证 — check-permission 不被 gate） |
| H5 | 父账号 + GET `/v1/sales-rag/documents` | 通过 gate（不关注 handler 结果，验 gate 放行） |

**实现方式**：
- 使用 `httptest.NewServer` + 项目现有 router setup 代码
- DB 用 in-memory SQLite（参考 `store/customer_test.go` 现有模式）
- 父子账号 fixture 在 test setup 中创建

**意义**：这是整个项目第一次给"路由级 middleware 挂载"加 Go 单元测试。content_monitor 未覆盖，属技术债。本 spec 补上。

---

## §7 S5 验证策略

**选 Playwright E2E**（不是 gstack `/qa`），与 parent-self-grant-membership / child-run-permission 策略一致。

**理由**：
- 权限是高风险业务逻辑（改变线上子账号可见性）
- 需要持久化回归保护（gstack `/qa` 一次性验证，无自动回归）
- 已有 E2E 基础设施（`numind-web-v3/e2e/`）

**S5 具体执行**：
1. 本地启动后端（`cd numind-server && task dev`）
2. 本地启动前端（`cd numind-web-v3 && npm run dev`）
3. 运行 `E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e -- sales-agent-permission.spec.ts`
4. 运行 `go test ./internal/numind/...`（含 C3 单测 + C5 httptest）
5. 运行 `task lint`（numind-server）+ `npm run lint && npm run type-check`（numind-web-v3）

---

## §8 回滚策略

**回滚路径**（1-3 分钟可完成）：
1. `git revert <feature-commit>` — 撤销 C1 `.Use()` 行 + C2 硬编码恢复
2. `task build` → 重启后端服务（约 30s）
3. 无需 DB migration（不涉及 schema 变更）
4. 已有的 `user_feature_permission` grant 记录保留（回滚后被授权子账号回到"全部可用"状态，不是"被吊销"）

**回滚触发条件**：
- 上线后 error rate 激增
- 客服反馈子账号大面积受阻且产品公告未到位
- middleware chain 其他 bug 被本次改动触发

**无需 feature flag**：改动极小（2 文件约 11 行），直接 git revert 足够，不引入 flag 复杂度。

---

## §9 上线前 Go/No-Go 检查（reviewer Q9 硬要求）

S4 完成后、S6 merge 之前，**必须**执行：

```sql
-- SSH dev / prod DB 分别跑
SELECT COUNT(*) AS grants FROM user_feature_permission WHERE feature_key = 'sales_agent' AND deleted_at IS NULL;
SELECT COUNT(*) AS sub_users FROM user WHERE parent_user_id IS NOT NULL;
```

**预期结果**（reviewer 推测）：`grants = 0`，`sub_users > 0` → **上线即 100% 子账号被拒**（不是"部分"）。

**Go/No-Go 逻辑**：
- 若 `grants = 0` → 产品公告必须强调"所有销售智能体使用者"（不是"部分"）；S6 merge 前确认产品已通知到所有父账号
- 若 `grants > 0` → 说明有父账号提前手动 grant 过（通过已有 API），冲击面较小；正常上线即可
- 任何情况下 **禁止** 在 migration 里 backfill（S0 D1 决策）

**此检查必须写进 S3 plan 作为独立 task**，由 AI SSH 执行（不让用户手动操作，见 CLAUDE.md §7）。

---

## §10 PRD 验收标准覆盖检查

| # | AS | 覆盖组件 | 验证方式 |
|---|----|---------|---------|
| AS-1 | 父账号开关 → 子账号可进 | 既有 `GrantFeatures` API + C1 gate | E2E E1+E2 |
| AS-2 | 父账号关开关 → 子账号下次请求 403（撤权即时生效） | 既有 `RevokeFeatures` + C1 gate | E2E E3+E4+E5 |
| AS-3 | 子账号 `/check-permission` → `has_permission: false`，且 check 本身不被 gate | C2 + D1 | C3-T3 + C5-H4 |
| AS-4 | 子账号无权访问 11+（实际 27）运行端点 → 全部 403 | C1 gate | C5-H1/H2/H3 (抽样 3 类) + E2E E5 |
| AS-5 | 父账号无需 grant 即可访问所有端点 | D4（`HasFeaturePermission` 既有逻辑） | C3-T1 + C5-H5 |
| AS-6 | 发布后无 backfill，所有现有子账号默认 deny-all | §9 Go/No-Go SQL 验证 | S6 前 SSH DB 查询 |
| AS-7 | `/check-permission` 与 gate 判断一致 | D6 + C2 biz 路径约束 | C3（check-permission 行为）+ C5-H4（gate 行为）对同一 fixture 给出一致结果 |
| AS-8 | 对积分 / 会员 / billing_mode 无影响 | D5（gate 在 credit 预检之前） | 验证方式：C5 测试仅返回 403，不触碰 credit 相关 mock（隐含覆盖） |

**边界覆盖**：
- E1 并发撤权 SSE → Accepted Exposure 2（§5），不强制验证当前流中断
- E2 子账号变父账号 → D4 代码层面保证，不专门测（既有 `HasFeaturePermission` 测试覆盖）
- E3 未知 featureKey → 非本 spec 范围（grant API 层面）
- E4 check-permission 位置 → D1 + C5-H4

---

## §11 影响面总结

| 改动 | 文件 | 行数 | 类型 |
|-----|------|------|------|
| C1 | `numind-server/internal/numind/router.go` | +1 | 修改 |
| C2 | `numind-server/internal/numind/controller/v1/salesrag/sales_rag.go` | ±10 | 修改 |
| C3 | `numind-server/internal/numind/controller/v1/salesrag/sales_rag_test.go` | 新建 ~120 行 | 新建测试 |
| C5 | `numind-server/internal/numind/router_sales_gate_test.go` | 新建 ~150 行 | 新建测试 |
| C4 | `numind-web-v3/e2e/sales-agent-permission.spec.ts` | 新建 ~100 行 | 新建测试 |

**总计**：2 文件修改（+11 行）+ 3 文件新建（~370 行测试）= **2 仓库 / 5 文件**。

**不改动的确认**：
- ❌ 不改 biz 层（`biz/salesrag/*`）
- ❌ 不改 store 层
- ❌ 不改 DB schema（无 migration）
- ❌ 不改前端源码（`numind-web-v3/src/`）
- ❌ 不改 admin 端（`numind-admin-web`）
- ❌ 不改 config_*.yaml

---

## §12 非目标（YAGNI）

- 不加"子账号申请开通"工作流
- 不做审计日志（`user_feature_permission.created_at` / `deleted_at` 已够用）
- 不加 admin 端管理 UI（复用用户端父账号弹窗）
- 不做 backfill migration（S0 D1）
- 不改 SSE 循环实现严格撤权（Accepted Exposure 2）
- 不修复百炼上传 bypass（Accepted Exposure 1）
- 不给 content_monitor 补 httptest（本 spec 只管 sales_agent；若未来要做，独立任务）

---

## §13 Trace Topology

**N/A** — 本功能是权限 gate，不触发任何 LLM 调用。gate 发生在 credit 预检之前，拒绝的请求根本不会进入 LLM 路径。现有 SalesRAG LLM trace 不受本改动影响。
