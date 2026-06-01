# 积分消耗记录显示具体任务名 — 提案（快车）

> S1 工件。对应 `requirements/credit-log-task-names.md`。Standard 轨道（用户指定走快车）。

## §1 方案概述 [客户可见]

把「积分消耗记录」弹窗里每条记录的「动作」列改名「任务」，并从通用名升级为**具体任务名**：

| 时间 | 任务 | 消耗积分 |
|---|---|---|
| 06-01 14:32 | 获客SOP · 第3步 开场白 | 18 |
| 06-01 11:05 | 销售对话：李总跟进 | 6 |
| 05-31 22:14 | 智能体：合规问答助手 | 5 |

即：SOP 执行显示「哪个 SOP · 哪个步骤」、SOP 对话显示 SOP 名、销售对话显示会话名、智能对话显示智能体名。**历史记录**（改造前产生的）因系统当时未记录指向，仍显示通用名（「SOP 执行」等）——这是一次性的、随新数据自然消失的过渡现象。

## §2 报价与周期 [客户可见]
- 预估工作量：**3 ~ 4 天**（后端 2.5-3 天：网关贯通 reference_id + 跨域批量查名 + 富集；前端 0.5 天）。比普通 Standard 略重，因要安全改动计费网关。
- 报价：内部功能。
- 交付：S2 设计 → S3 计划 → S4 编码（每 task 双 review，重点审计计费边界）→ S5 本地验收 → S6 dev 验收。

## §3 技术可行性 [AI 内部]

### 核心改动（两段）
1. **写入端：把业务引用穿过 aiservice 计费网关写进 `credit_reservation.reference_id`**（这是 S0 调研定位的根本前提——现在生产路径 reference_id 为空）。
   - `internal/pkg/aiservice/middleware/context_budget.go` `doReserveBudget`：`BudgetReservationInput` 增加业务引用字段（refType+refID），写进 reservation。
   - 业务引用从调用方经 `aiservice.Chat` 的 ctx 传入：`biz/sop`（sop_run_id + node_id）、`biz/salesrag`（session_id）、`biz/chatbot`（session_id）。复用现有 `biz/agent/callctx` 式 ctx 注入模式。
   - credit 侧 `ReserveBudget`/`reserveBudgetRow` 接受并落库 refType/refID。
2. **读取端：富集消耗记录**（`biz/credit/consumption_log.go::ListConsumptionLog`）。
   - 按 reference_type/reference_id 解析具体名：sop_run→`sop_template.name`+`sop_node.name`；sop_chat→SOP名；salesrag_chat→`sales_session.title`；chatbot_chat→`chatbot_config.name`。
   - **批量查名防 N+1**：一页 ~20 行先按类型分组收集 id，各域一次 `WHERE id IN (...)` 查名（现无批量 helper，需新增 store 批量方法）。
   - 空 reference_id / 查不到 → 回退通用 label。

### 复用线索（S0 调研 file:line）
- 名字字段：`sop_template.name`/`sop_node.name`/`sales_session.title`/`chatbot_config.name`（均已存在）。
- 既有单行 getters 可参考：`store/sop.go` GetTemplate/GetNode/GetRun、`store/sales_session.go` GetSession、`store/chatbot_session.go` GetSession。
- ctx 注入模式：`biz/agent/callctx`（每次 aiservice.Chat 注 callID）。

### 技术风险
| 风险 | 缓解 |
|---|---|
| **改计费网关破坏 billing/路由降级/Langfuse**（最高风险）| 只在 reserve 时多写一个引用元数据，不改扣费金额/路由/trace 逻辑；S4 双 reviewer 专项审计 `aiservice` 唯一入口 invariant + billing 中间件未受影响；S5 触发真实 AI 操作验证 trace + 扣费正常 |
| N+1 查名 | 批量 `IN (...)`，每域每页一次查询 |
| 越权（看到他人 SOP/会话名）| sales/chatbot getSession 已要求 userID；SOP 按 reservation 自身 user 的 run/node 查；store 批量查名带 user 约束或仅查本用户 reservation 引用的 id |
| 历史行无名 | 明确回退通用 label，UI 不报错 |
| 多调用方传 id 遗漏 | 缺 id 时回退通用 label（不报错），逐调用方加 + S5 各路径验证 |

### 涉及仓库
- [x] numind-server（网关中间件 + credit biz/types + sop/salesrag/chatbot 调用方 + store 批量查名 + 富集）
- [x] numind-web-v3（弹窗表头「任务」+ 渲染具体名）
- [ ] numind-admin-web（不涉及）

### AI 可观测性
- [x] 涉及 LLM 调用：**间接**——本功能不新增 LLM 调用，但**改动了 aiservice 网关中间件**。要求：reserve 写 reference_id 不得影响 Langfuse trace/generation 与 billing。S5 触发一次真实 SOP/销售/对话操作，确认 trace 正常 + 扣费金额不变 + reference_id 已写入。

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事
- 作为用户，我在「积分消耗记录」里希望「任务」列显示**具体**是哪个 SOP 的哪个步骤 / 哪个销售对话 / 哪个智能体消耗了积分，而不只是「SOP 执行」这种笼统词，以便精确核对。

### 验收标准
- [ ] 表头「动作」→「任务」
- [ ] 改造**上线后**新产生的记录：sop_run 显示「SOP名 · 步骤名」、sop_chat 显示「SOP名」、salesrag_chat 显示会话名、chatbot_chat 显示智能体名
- [ ] 历史记录（reference_id 空）回退通用名，不报错、不崩
- [ ] 一页 20 行查名无 N+1（批量查询；后端日志/测试验证查询次数有界）
- [ ] 越权：用户只看到自己 reservation 引用的实体名，绝不泄露他人 SOP/会话名
- [ ] reserve 写 reference_id **不改变扣费金额**、不破坏 Langfuse trace（S5 对账 + trace 验证）
- [ ] 未知/未配置 operation → 回退通用名

### 边界情况
- 引用的实体已删除（SOP/会话被删）→ 回退通用名或「已删除」占位
- 同一 SOP 多步骤 → 各步骤行显示对应步骤名
- 空 reference_id（历史）→ 通用名
- 调用方未传 id（遗漏/异常）→ 通用名

### 权限规则
- 用户端，所有账户看自己；查名严格限定本用户引用的实体 id。

### UI 行为规格
- 弹窗（沿用已上线的 `CreditConsumptionLogModal`）表头「任务」；「任务」列文本=具体名（后端给）；长名截断 + tooltip（S2 定）。其余沿用现有样式（已对齐客户管理）。

## §5 产品思考（office-hours 式）
- **价值**：从「花在某类操作」升级到「花在具体哪件事」，是计费透明度的质变,直接支撑用户对账与信任。
- **最窄可行**：全覆盖四类操作（用户已选）；不做导出、不做按任务筛选（未来增强）。
- **关键风险点**：动计费网关。原则——**只增不改**：reserve 时多写一个引用元数据，绝不碰扣费金额/路由/trace 逻辑；用 S4 双 review + S5 对账守住「钱不能算错」。
- **历史数据取舍**：明确接受历史行回退通用名（不做高成本回填）；新数据自然生效。
