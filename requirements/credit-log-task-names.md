# 积分消耗记录显示具体任务名（Credit Log Task Names）

## 来源
- 提出人：用户（产品 owner）
- 提出日期：2026-06-01
- 背景：基于已上线的「积分消耗记录」弹窗（feature `credit-consumption-log`）的用户反馈。

## 需求描述

> 「动作」需要改为「任务」，并且每一条显示的内容需要变成具体的模块的名称以及步骤名称：比如哪一个 SOP 的哪个步骤；SOP 对话要显示那个 SOP 的名称；智能对话要显示具体的名称。

结构化理解：
- 弹窗表头「动作」→「任务」。
- 每条记录从**通用名**（现在的「SOP 执行」「销售对话」「智能对话」）改为**具体实体名**：
  - `sop_run` → 「<SOP 名> · <步骤/节点名>」
  - `sop_chat` → 「<SOP 名>（对话）」（无独立步骤）
  - `salesrag_chat`（销售对话）→ 「<会话标题>」或对应 SOP/场景名
  - `chatbot_chat`（智能对话）→ 「<智能体名称>」（或会话标题）
  - 其它（file_parse/ocr/agent_test 等）→ 回退通用名

## 业务目标
进一步提升计费透明度：用户不只看到「做了某类操作」，而是「具体哪个 SOP 的哪个步骤 / 哪个对话」消耗了积分，便于核对与信任。

## 优先级
中。体验增强，非紧急生产事故。

## Triage
- **推荐轨道：Standard**（用户 2026-06-01 已确认，并指定「走快车」= 精简流程的 Standard）
- **分类理由**（5 条标准逐条）：
  1. 数据库 schema 变更：**否**（`credit_reservation.reference_id` 列已存在，复用；名字表已存在）
  2. 新增 API 端点：**否**（复用现有 `GET /v1/credits/consumption-log`，改其响应：新增/改 `action_label` 为具体名）
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（aiservice 网关中间件 + credit biz/types + 三个调用方 biz（sop/salesrag/chatbot）+ store 批量查名 + 前端 ~1-2）
  5. 高风险业务逻辑（支付/权限）：**是**（动 **计费信任边界**：reserve 时把业务 id 穿过 `aiservice` 唯一入口写进 reservation；属计费 SOT 高风险域）
- **人类决定：确认 Standard（快车）**

## 涉及仓库
- `numind-server` — 网关贯通 reference_id + 跨域查名 + 批量查 store + biz 富集 + API 响应
- `numind-web-v3` — 弹窗表头「任务」+ 渲染具体名（多为后端给名，前端渲染）

## 备注（S1/S2 必须解决的关键约束 + 调研结论）

### 🔴 关键约束（S0 调研已确认，决定方案与取舍）
- **`credit_reservation.reference_id` 在生产主路径上为空**：绝大多数扣费走 **aiservice 网关（context-budget）路径**，建 reservation 时**没写业务 id**（`reference_id=""`）。只有近废弃的「内联 SOP」老路径写了 `sop_run:<runId>:<nodeId>`。
- 名字本身好查（都在）：`sop_template.name` / `sop_node.name`（步骤）/ `sales_session.title` / `chatbot_config.name`（或 `chatbot_session.title`）。**缺的是从扣费记录指向实体的指针。**
- → 要做成,必须**在 reserve 时把业务 id 穿过 `aiservice` 计费网关中间件写进 `credit_reservation.reference_id`**（改 `internal/pkg/aiservice/middleware/context_budget.go`、credit `ReserveBudget`/types、以及 sop/salesrag/chatbot 三个调用方传 id）。
- **历史记录无法回填**：已存在的空 reference_id 行拿不到具体名 → **回退通用名**。改造**只对新产生的记录**生效。这是必须接受的产品取舍。

### S1/S2 待决策（S1 提案确认）
1. **覆盖范围**：是否全覆盖 sop_run/sop_chat/salesrag_chat/chatbot_chat，还是先 SOP（含步骤）后续再扩对话？（影响快车工作量）
2. **历史行展示**：空 reference_id 的旧行回退「SOP 执行」等通用名（默认）；是否需要在 UI 上区分「具体/通用」？默认不区分。
3. **响应字段**：复用 `action_label` 直接放具体名，还是新增 `detail_name` 字段（保留 `action_label` 通用名 + `detail_name` 具体名）？S2 定。
4. **N+1 防护**：一页 ~20 行跨 SOP/sales/chatbot 查名，需批量 `WHERE id IN (...)`（现无批量 helper，需新增）。
5. **越权**：查名时仍须限定当前用户（sales/chatbot 的 GetSession 已要求 userID）；SOP 名按 run/node 查不泄露他人数据。
6. **安全（高风险）**：改 `aiservice` 网关写 reference_id 不能破坏 billing 计费 / 路由降级 / Langfuse；S4 双 reviewer 重点审计计费信任边界。

### 复用线索（file:line，S0 调研）
- model：`sop.go`（SopRun.TemplateID / SopTemplate.Name:12 / SopNode.Name:35）、`sales_session.go`（Title:13）、`chatbot.go`（ChatbotConfig.Name:25 / ChatbotSession.Title:56,ChatbotID）。
- store getters（均单行，需加批量版）：`store/sop.go` GetTemplate/GetNode/GetRun；`store/sales_session.go` GetSession(ctx,id,userID)；`store/chatbot_session.go` GetSession(ctx,id)。
- biz 落点：`biz/credit/consumption_log.go` ListConsumptionLog（现仅 op→label）；DTO ConsumptionLogItem。
- 网关：`internal/pkg/aiservice/middleware/context_budget.go` doReserveBudget（现 BudgetReservationInput 无 IdempotencyKey/业务 id）；credit `ReserveBudget`/`reserveBudgetRow`（reference_id 来自 idempotencyKey）。
