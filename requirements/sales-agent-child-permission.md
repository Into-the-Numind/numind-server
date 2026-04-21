# 销售智能体 — 父账号子账号权限管控

## 来源
- 提出人：用户（产品 owner）
- 提出日期：2026-04-21

## 需求描述

目前项目有两大板块：
1. **SOP** — 已有父账号对子账号的"运行权限"管控（通过 `user_template_permission` 白名单）
2. **智能体** — 已有父账号对子账号的"运行权限"管控（通过 `user_chatbot_permission` 白名单，于 2026-04-20 上线）

**例外**：智能体中的**销售智能体**（SalesRAG）当前对所有登录用户开放，父账号无法控制子账号能否使用销售智能体。本需求要将销售智能体纳入权限管控范围，与 SOP / chatbot 保持一致。

## 业务目标

- B2B2C 场景下，父账号（代理商/运营方）需要对子账号的功能可见性有细粒度控制
- 销售智能体是**高价值能力**（消耗成本也高），必须能被父账号按子账号开关
- 对齐 SOP / chatbot 的运行权限模型，避免"三个板块两套语义"的混乱

## 优先级
高（运营方已明确提出，阻塞对新客户的销售智能体差异化报价）

## Triage

- **推荐轨道**：Standard
- **分类理由**（5 条标准逐条）：
  1. 数据库 schema 变更：**否**（`user_feature_permission` 表 + `FeatureKeySalesAgent = "sales_agent"` 常量早已定义，`GrantFeatures` / `RevokeFeatures` / `HasFeaturePermission` / middleware gate 基础设施全链路已存在，`content_monitor` 已走通同一套路）
  2. 新增 API 端点：**否**（复用现有 `POST/DELETE /v1/customers/sub-users/:user_id/features`）
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（sales_rag controller 11+ 端点需挂 gate + `CheckSalesPermission` 硬编码修复 + 前端客户管理弹窗加"销售智能体"开关 + 回归测试）
  5. 高风险业务逻辑（支付/权限）：**是**（权限默认 deny-all 翻转 + 改变线上子账号可见性）

满足 2 条 → Standard。
- **人类决定**：确认 Standard，**加速执行**（跳过 /office-hours，沿用 parent-self-grant-membership 先例）

## 关键业务决策（S0 已封存）

| # | 决策 | 理由 |
|---|------|------|
| D1 | 默认 **deny-all**（不 backfill） | 语义与 SOP / chatbot 对齐；父账号必须主动授权；上线公告承担沟通成本 |
| D2 | 父账号（`parent_user_id IS NULL`）自动 true | `HasFeaturePermission` 已实现此逻辑，无需改动 |
| D3 | 修复 `CheckSalesPermission` 硬编码 `true` | 改为真正查询 `HasFeaturePermission` |
| D4 | 所有销售智能体运行端点统一挂 gate | 与 chatbot `HasChatbotPermission` 覆盖策略对称 |
| D5 | 积分扣减顺序不变 | gate 在 credit 预检之前（无权限直接 403，不浪费 credit 预检） |

## 上线风险与缓解

- **风险**：上线瞬间所有子账号立即 403，客服压力大
- **缓解**：
  1. 上线前向父账号提前公告（产品侧负责）
  2. 父账号管理弹窗新增"销售智能体"开关（与现有功能开关对齐）
  3. 403 响应附带清晰文案（"请联系管理员开通销售智能体权限"）

## 备注
- 现状来源：`numind-server/internal/numind/controller/v1/salesrag/sales_rag.go:1019-1031` 的 `CheckSalesPermission` 永远返回 `true`
- manifest 2026-04-20 记录（self-service-config 功能）："FeatureKeySalesAgent 常量及 user_feature_permission 表保留 — 未来若引入其他分级 feature 仍需复用该基础设施；本次仅对 sales_agent 这一 key 停用" — 本需求即为该预留基础设施的激活
- NDF 先例：`child-run-permission`（chatbot 权限）即为本需求的对称参考，spec / plan / migration 结构可复用
