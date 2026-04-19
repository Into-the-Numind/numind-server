# AI Service 管理能力补齐（ai-service-admin-complete）

## 来源

- 提出人：项目负责人
- 提出日期：2026-04-18
- 前置调研：2026-04-18 session 对 admin-web 与 ai-service-manager 的 gap audit
- 前置 feature：`ai-service-manager`（S7-dev-done）、`legacy-llm-cleanup`（S7-dev-done）
- 触发场景：用户询问"在哪个页面调整模型路由配置"，发现 admin-web 存在关键管理能力缺失

---

## 需求描述

**核心诉求：** admin-web 需要对 AI service 相关配置**全面可查看、可编辑**，符合管理系统特性。

**现状盘点：**

`ai-service-manager` feature 交付了 Service / Task Profile / Audit Log 的读路径 + Service 写、Task 写，但未交付：
- Route CRUD（priority / pricing / is_active 当前只读，只能 SQL 改）
- Provider CRUD（`llm_provider` 表依然在用，但 legacy-llm-cleanup 删了旧 Provider 管理页面，api_key 只能 SQL 改）
- Audit Log 端点未注册（前端 `AuditLogs.vue` 已写但会 404）
- Task Profile 的 fallback_service 只支持单选，后端支持多选
- Pricing Rule 与 AI Service 关联关系在 UI 不可见（两套定价并存）

**关键架构问题（tier pricing）：** 后端已有完整的阶段计费支持（`pricing_rule_tier` 表 + `billing/recorder.go:calculateTieredCost` + `PricingRulesView.vue` 的 tier drawer UI），但 `pricing_rule` 与 `ai_service_route` **靠字符串匹配关联**（`service_type + provider + model`），不是 FK。管理员从 ServiceEdit 看不到某 service 对应哪条 pricing_rule，容易配错。本期需在 T0 做架构决策。

---

## 业务目标

| 目标 | 衡量 |
|------|------|
| admin-web 全面覆盖 AI service 配置管理 | 所有 `ai_service*` / `llm_provider` / `pricing_rule*` 表的关键字段可在 UI 编辑 |
| 修复 AuditLogs 404 | `/v1/admin/ai/audit-logs` 返回 200，前端表格正常渲染 |
| 阶段计费可视化 | ServiceEdit 页面能看到关联的 pricing_rule + 其 billing_mode（flat/tiered）+ 跳转链接 |
| 消除 SQL-only 运维 | 改 priority / 换 api_key / 停用 route 等日常操作走 UI |

---

## 优先级

**高** — 直接影响日常运营（改 route 优先级、换 provider api_key 当前只能 SQL）。

---

## 范围

### In Scope

- **T0** 架构决策（inline proposal）：pricing_rule ↔ ai_service_route 关系
- **T1** server: Audit Log controller + 端点注册
- **T2** server: Route CRUD 端点（Create/Update/Delete/Toggle + priority 重复校验）
- **T3** admin-web: ServiceEdit 路由区改可编辑 + 显示关联 pricing_rule + 跳转链接
- **T4** server: Provider CRUD 端点（List/Get/Create/Update/Delete，api_key 写时返回掩码）
- **T5** admin-web: ProvidersList + ProviderEdit 页面 + 路由注册 + sidebar 项
- **T6** admin-web: TaskEdit fallback_service 单选改多选
- **T7** admin-web: PricingRulesView 增强 — 显示每条规则关联的 AI service 列表 + 按 service 筛选
- **T9** S5 验证策略：gstack `/qa` + curl tiered 计费端到端验证（含 Gemini 3.1 Pro / GPT 5.4 tier 触发）

### Out of Scope

- **T8**（条件纳入）：`ai_service_route` 加 `pricing_rule_id` FK migration — 待 T0 决策后定，本需求不预判
- `/healthz/ai` 可视化 dashboard（P2，延后）
- 批量操作（批量 toggle / 批量改 pricing）（P2）
- Pricing Rule 与 AI Service 的 FK 强约束改造（若 T0 选"保持字符串匹配"方案，此条也不做）
- C 端用户模型选择 UI（不在 admin 范围）

---

## 约束与注意事项

### 数据安全

- Provider 的 `api_key` 写时后端保留原值（不接受空字符串清空）；读时返回掩码（`MaskedAPIKey()` 已存在）
- Audit Log 必须对以下操作完整记录：Provider CRUD、Route CRUD、Pricing Rule 改动
- Route `is_active=false` 不能让对应 task profile 的 default_service 失效（后端需校验）

### Priority 语义

- `ai_service_route.priority` 数值大优先（参考 aihubmix-provider：AiHubMix=10 主路由、DMXAPI=5 兜底）
- 同一 service_id 下，多条 active route 按 priority desc 选主、priority 相同按 created_at asc 稳定排序
- UI 保存时提示（非阻断）相同 priority 冲突

### Tier Pricing 决策（T0 核心）

T0 inline proposal 必须回答以下 3 个问题，二选一决策（任选其一均可进入 T3 实现）：

1. **定价真相源：** pricing_rule 为准（现状）、还是 ai_service_route 为准？
   - 现状：billing recorder 用 pricing_rule（支持 tier），ai_service_route.pricing 只作 snapshot
   - 建议：保持 pricing_rule 为真相源，ai_service_route.pricing 作 flat snapshot（简单场景够用，tiered 场景以 pricing_rule 为准）

2. **两表关联：** 字符串匹配 vs FK
   - 字符串匹配（现状）：灵活但容易失配
   - FK（T8 可选）：强一致但 migration 动静大
   - 建议：本期先字符串匹配 + UI 层展示关联（T3/T7），FK 留给未来

3. **ServiceEdit 页面展示什么：**
   - 当前路由区 + 新增"关联 pricing_rule"卡片（显示 billing_mode / tier 数 / 跳转链接）
   - 如果 pricing_rule 是 tiered，ai_service_route.pricing 显示什么？→ 显示"仅作 snapshot，实际计费看 pricing_rule"提示

---

## 成功指标

| 指标 | 验收方式 |
|------|---------|
| admin-web 所有 AI 配置可编辑 | 操作者能仅通过 UI 完成：创建 route、改 priority、停用 route、创建 provider、改 api_key、加 task fallback | 
| AuditLogs 恢复工作 | `/admin/ai/audit-logs` 页面列出 T1 之前的 Service/Task 改动日志 |
| Tier pricing 端到端可计费 | 用 Gemini 3.1 Pro 触发一次 ≤200K context 和一次 >200K context 调用，UsageRecord 的 cost_cents 分别按两个 tier 的 rate 计算 |
| 无 SQL 运维需求 | 团队运营文档去掉"改 api_key 需要 SSH 改 DB" |

---

## Triage

- **推荐轨道：** Standard 精简版（类比 legacy-llm-cleanup / aihubmix-provider）
  1. 数据库 schema 变更：**否**（本期不动 schema；T8 若纳入则改为"是"）
  2. 新增 API 端点：**是**（Route CRUD 3-4 个 + Provider CRUD 5 个 + Audit Log 1 个）
  3. 新外部服务集成：**否**
  4. 跨 >3 文件 / >1 仓库：**是**（server + admin-web 两个 repo）
  5. 回归风险：**中**（改 priority / is_active 影响 prod 流量路由，需 Sonnet reviewer 把关 + gstack /qa）

- **精简策略：** 跳过 S0/S1 的 brainstorming（范围清楚、诉求明确），S1 简化为 inline proposal（主要是 T0 架构决策），S2 spec 采用 inline task 拆分（类似 legacy-llm-cleanup），直接进 S4。

- **预估工时：** 3-4 天（含 T0 决策 0.3d + T1-T9 = 2.7-3.7d，每 task 两阶段 Sonnet review）

- **前置 context 给 AI：**
  - 2026-04-17 session 已完成 ai-service-manager S7-dev-done + legacy-llm-cleanup S7-dev-done
  - 相关 commit 落在 develop，prod 未发版
  - Gap audit 结论：见 build-manifest.yaml ai-service-admin-complete 条目的 gap_audit 字段

---

## 相关文档

- `numind-server/build-manifest.yaml` — manifest entry（本 feature 的权威状态）
- `docs/superpowers/specs/2026-04-15-ai-service-manager-design.md` — ai-service-manager spec（背景）
- `numind-admin-web/src/views/AIService/*` — 前端当前实现
- `numind-server/internal/numind/biz/aiservice_admin/biz.go` — 后端 biz 层（要扩展）
- `numind-server/internal/pkg/model/ai_service.go` — AIService / AIServiceRoute struct
- `numind-server/internal/pkg/model/billing.go` — PricingRule / PricingRuleTier struct
- `numind-server/internal/pkg/billing/recorder.go:221` — calculateCostAndRevenue（tier 分派逻辑）
