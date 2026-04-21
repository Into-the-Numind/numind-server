# 销售智能体 — 父账号子账号权限管控 — 提案

## §1 方案概述 [客户可见]

目前 SOP 和通用智能体，父账号已经可以在客户管理弹窗里按子账号逐个开关"运行权限"。但**销售智能体**被遗漏了 — 所有登录用户都能使用，父账号没法控制。

本方案把销售智能体也纳入同样的权限体系：
- 父账号在客户管理弹窗里多一个"销售智能体"开关（**UI 其实已经做好了**，本次只需激活后端逻辑）
- 开关打开 → 子账号可用；开关关闭 → 子账号进不去销售智能体页面，就算知道 URL 也会被后端 403 拒绝
- **默认关闭**。功能上线当天，所有现有子账号都变成"未授权"状态，父账号需要主动开 —— 对齐 SOP / 通用智能体的既有语义
- 父账号自己永远有权限（无需开关）

上线前由产品侧向父账号公告一次，避免客服雪崩。

## §2 报价与周期 [客户可见]

- 预估工作量：**0.5 天**（基础设施 100% 已就位，本次仅激活 + 前端 UI 已存在）
- 报价：对内需求，不单独计价
- 交付时间线：2026-04-21 当天完成 S0 → S6 全流程（含 review + E2E）

## §3 技术可行性 [AI 内部]

### 现有功能复用（基础设施 100% 就位）

| 组件 | 现状 | 本次操作 |
|------|------|---------|
| 表 `user_feature_permission` | 已存在（`migrations/000002_add_user_feature_permission.sql`） | 不动 |
| 常量 `FeatureKeySalesAgent = "sales_agent"` | 已存在（`model/user_feature_permission.go:7`） | 不动 |
| Store `HasFeaturePermission / GrantFeatures / RevokeFeatures / ListUserFeatures` | 已存在（`store/customer.go:340-412`） | 不动 |
| Biz `customerBiz.HasFeaturePermission` 等 | 已存在（`biz/customer/customer.go:341`） | 不动 |
| Middleware `FeaturePermission(featureKey)` | 已存在（`middleware/middleware.go:212-238`） | 不动 |
| API `POST/DELETE /v1/customers/sub-users/:user_id/features` | 已存在（`router.go:250-251`） | 不动 |
| 前端客户弹窗 "销售智能体" 开关 | 已存在（`CustomersView.vue:652,1084,1426,1482`） | 不动 |
| 前端 `grantFeatures` API 函数 | 已存在（`api/customers.ts:139`） | 不动 |

**先例对标**：`content_monitor` 走的就是完全同一套路（`router.go:332` `monitorGroup.Use(FeaturePermission(FeatureKeyContentMonitor))`）。本次只需把 `salesGroup` 也 `.Use()` 一下。

### 本次真实改动

| 文件 | 行数预估 | 说明 |
|------|---------|------|
| `numind-server/internal/numind/router.go` | +1 行 | `salesGroup.Use(middleware.FeaturePermission(model.FeatureKeySalesAgent))` |
| `numind-server/internal/numind/controller/v1/salesrag/sales_rag.go` | ±10 行 | `CheckSalesPermission` 硬编码 `true` 改为真查询 `HasFeaturePermission` |
| `numind-server/internal/numind/biz/salesrag/service/sales_rag.go` 等 | 可能 0 | 如果 biz 层没别的入口直接绕过 controller，则不用改 |
| `numind-web-v3/e2e/sales-agent-permission.spec.ts` | 新建 | E2E 回归：父账号开关切换 → 子账号访问成功/403 |
| `numind-server/internal/numind/controller/v1/salesrag/sales_rag_test.go` | 新建 | `CheckSalesPermission` 的单测（父账号 true / 子账号无权 false / 子账号有权 true） |

### 技术风险

| 风险 | 影响 | 缓解 |
|------|-----|-----|
| R1 `salesGroup` 上的 gate 会拦截 `CheckSalesPermission` 本身，造成死锁 | 子账号访问 `/sales-rag/check-permission` 直接 403，前端拿不到"未授权"信号 | **`check-permission` 端点必须注册在 authGroup（当前就是）而非 salesGroup**。Gate 挂在 salesGroup 只覆盖真正的运行端点。spec 必须明确这一点 |
| R2 biz 层若有其他入口（如 cron / 内部调用）直接走 biz 绕过 controller 的 gate | 权限漏洞 | S2 spec 阶段逐行审 biz 层入口。已知 salesrag biz 层函数只被 controller 调用，但必须确认 |
| R3 上线瞬间所有子账号 403，客服压力 | 短暂客服高峰 | 产品侧上线前公告（业务层责任，非技术方案范围） |
| R4 403 文案不清晰 | 子账号困惑 | 复用 middleware 既有文案"未开通该功能权限，请联系管理员"（已与 content_monitor 一致） |

### 涉及仓库
- [x] numind-server（核心改动）
- [x] numind-web-v3（仅 E2E 测试文件，源码零改动）
- [ ] numind-admin-web

### AI 可观测性
- 涉及 LLM 调用：**否**（本功能是权限 gate，不触发 LLM。gate 发生在 credit 预检之前，在 LLM 调用之前，所以权限拒绝根本不会产生 trace 需求）
- N/A

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事
- **作为父账号（代理商）**，我需要在客户管理弹窗里对每个子账号单独开关"销售智能体"权限，以便差异化向不同销售分发工具
- **作为未授权子账号**，我尝试访问销售智能体时应立即看到"未开通，请联系管理员"提示，而不是看到空白页或报错
- **作为已授权子账号**，我使用销售智能体的所有功能（上传、对话、客户档案分析等）应与当前完全一致
- **作为父账号自己**，我无需任何授权操作即可使用销售智能体（与当前一致）

### 验收标准
- [ ] **AS-1** 父账号在客户管理弹窗中开启"销售智能体"开关 → `POST /v1/customers/sub-users/:user_id/features` 写入成功 → 子账号刷新页面可进入销售智能体
- [ ] **AS-2** 父账号关闭"销售智能体"开关 → `DELETE` 写入成功 → 子账号下次请求销售智能体任意端点 → **立即** 403（撤权即时生效，不依赖缓存过期）
- [ ] **AS-3** 子账号访问 `GET /v1/sales-rag/check-permission` → 返回 `{"has_permission": false}`（未授权情况下）；**此端点本身不被 gate 拦截**（否则拿不到信号）
- [ ] **AS-4** 子账号访问 `POST /v1/sales-rag/sessions/:id/chat` 等 11+ 运行端点（未授权）→ 全部 403，返回 `{"code": 100207, "message": "未开通该功能权限，请联系管理员"}`（沿用 `ErrForbidden`）
- [ ] **AS-5** 父账号（`parent_user_id IS NULL`）访问销售智能体所有端点 → 无需任何 `user_feature_permission` 记录也能通过 gate
- [ ] **AS-6** `user_feature_permission` 表在本次发布后**没有任何 backfill 写入** — 所有现有子账号默认 deny-all
- [ ] **AS-7** `/v1/sales-rag/check-permission` 返回值与 gate 判断一致（同源查询，不出现 "UI 显示有权但调用被拒" 或反之的不一致）
- [ ] **AS-8** 对积分 / 会员状态 / billing_mode 无任何影响（本功能在 credit 预检之前 gate，gate 失败直接返回，不消耗任何 credit）

### 边界情况
- **E1 并发撤权**：父账号在子账号对话进行到一半时撤权 → `ChatWithSession` SSE 流的**下一次请求**被 403 拒（当前进行中的 SSE 连接不会中断，与 chatbot 权限的"撤销即时生效"语义一致）
- **E2 父账号身份迁移**：子账号通过某种机制变成父账号（`parent_user_id` 被 NULL 化）→ `HasFeaturePermission` 立即返回 true，无需新增 grant 记录
- **E3 不存在的 featureKey**：前端传了 `"foo_bar"` → `GrantFeatures` 写入一条脏记录（这不是本功能要解决的问题，是 API 层面的事）
- **E4 `/check-permission` 端点**：属于 authGroup 而非 salesGroup，**不可**被 FeaturePermission gate 包住

### 权限规则
- **父账号**（`parent_user_id IS NULL`）：所有端点无需授权，直接通过
- **子账号**（`parent_user_id IS NOT NULL`）：必须在 `user_feature_permission` 表有 `(sub_user_id, 'sales_agent')` 记录才能通过 gate
- **未登录**：所有端点 401（与当前 `authGroup` 的 `middleware.AuthToken()` 一致）
- **管理端**（admin_token）：不受影响，本功能只影响用户端

### UI 行为规格
- **前端零源码改动**（UI 已存在）
- 客户管理弹窗的 "销售智能体" 开关状态由 `featurePermissions['sales_agent']` 驱动，现有 `grantFeatures` / `revokeFeatures` 调用直接生效
- 销售智能体首页 `HomeView.vue` + `SalesView.vue` 的 `checkSalesPermission()` 调用会在权限变化后获得真正的 `false`，触发已有的 "未开通" UI 分支（该 UI 分支早已存在但当前死代码）
- 状态处理：
  - loading：`checkSalesPermission` 请求中（已存在）
  - empty：N/A
  - error：`checkSalesPermission` 网络错误时的已有处理（已存在）
  - success-denied：显示"未开通，请联系管理员"（`HomeView.vue` / `SalesView.vue` 已有此分支）
  - success-granted：正常进入（当前行为）
