# HomeView 统一 Locked 卡片 UI — 被拒资源全部显示加锁

## 来源
- 提出人：用户（产品 owner）
- 提出日期：2026-04-21（继 sales-agent-child-permission S6 验收之后）

## 需求描述

在 dev 验收 sales-agent-child-permission 时观察到 HomeView 的 UX 不一致：
- **sales-agent 被 deny** → 卡片**显示但灰化** + 右上角 lock badge
- **chatbot 被 deny** → 卡片**直接不显示**（后端 `ListVisibleChatbots` 过滤）

希望统一为一致 UX：
- 所有被 deny 的资源卡片**都正常显示**（不过滤）
- **不变灰**，正常颜色
- 右上角加一把锁（复用当前 sales-agent `.lock-badge` 设计）
- 点击 → 弹"未开通，请联系管理员"提示（保持现状）
- 应用于 HomeView 首页的 3 类资源：**SOP workflows / sales-agent / chatbot**

## 业务目标

- **可发现性**：子账号看到"有这个能力存在但我没权限" → 主动去找父账号开通（当前 chatbot"直接消失"会让子账号不知道有哪些能力）
- **UX 一致性**：3 类资源同一套 UI 语言，降低认知负担
- **不动安全语义**：运行端点 gate（`HasChatbotPermission` / `HasTemplatePermission` / `FeaturePermission(sales_agent)`）维持，只改可见性

## 优先级
**中** — UX polish 性质，不阻塞已上线的 sales-agent gate feature

## Triage

- **推荐轨道**：Standard
- **分类理由**（5 条标准逐条）：
  1. 数据库 schema 变更：**否**
  2. 新增 API 端点：**是**（chatbot `list` 改返回全部 + `has_permission` flag；SOP 模板 list 同理；**或**新增 `list-all` 端点保留现有语义）
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（后端 chatbot biz/store + SOP biz/store + DTO + 前端 HomeView 大改 + API 类型声明 + 回归测试，估 6-10 文件）
  5. 高风险业务逻辑（支付/权限）：**算**—信息可见性翻转（子账号现在能看到所有父账号创建的 chatbot/template 标题+描述）；运行 gate 本身不变
- **人类决定**：确认 Standard + **加速执行**（跳过 /office-hours）

## 业务决策封存（S0 前问卷回答）

| # | 决策 | 值 |
|---|------|-----|
| D1 | 范围 | 仅 SOP + sales-agent + chatbot（**不含** content_monitor，后者侧边栏入口已注释停用） |
| D2 | 可见字段 | 全部信息（标题、描述、图标）— 与父账号视角一致 |
| D3 | Hover tooltip | 不显示（保持"点击才弹"行为） |
| D4 | 视图范围 | **仅 HomeView**（`/config/*` 是父账号管理页不动；其他视图无资源列表） |
| D5 | UI 样式 | **正常颜色 + 右上角 lock**，去掉当前 sales-agent 的 `.no-permission` **灰化** |

## 关键技术风险（S1/S2 需细化）

| # | 风险 | 缓解方向 |
|---|------|---------|
| R1 | chatbot `ListVisibleChatbots` 改动是 child-run-permission 2026-04-20 spec 的 partial revert（当时明确选 "0 记录 = 不显示"） | S2 明确："list 返回所有 + flag" 不破坏运行 gate，只改可见性；spec §5 `HasChatbotPermission` 安全不变 |
| R2 | API 破坏性变更 vs 新增端点 | S2 选择：改现有 `list` 返回语义（加 `has_permission` 字段，前端旧代码忽略字段不影响），或新增 `/list-all`（后者更保守但代码重复）。S1 封存决定 |
| R3 | SOP 当前 list 行为未知 | S1 必须查 `/sop/templates` 列表对子账号实际返回什么；如果已过滤，SOP 侧同样要做 partial revert |
| R4 | 信息泄露（父账号创建的 chatbot 名字可能含商业机密） | D2 已接受——父账号自行负责命名 generic；不阻塞 |
| R5 | `.lock-badge` + 文案「未开通」需要跨 3 类资源复用而非复制粘贴 | S2 抽 `<LockableCard>` 共享组件 or CSS util class |

## 预期改动面（S0 粗估，S2 精修）

**后端（numind-server）**：
- `biz/chatbot/chatbot.go` `ListVisibleChatbots`（改语义或新函数）
- `biz/sop/sop.go`（SOP list 类似修改）
- 两个模块的 controller / router / DTO
- 回归测试

**前端（numind-web-v3）**：
- `views/HomeView.vue`（大改：3 个 section 都接入 permission + lock badge 渲染）
- `api/chatbot.ts` / `api/sop.ts` 类型声明
- 去掉 `.no-permission { opacity/gray }` 样式
- E2E 回归（检查 denied chatbot 出现 + 锁标志 + 点击弹提示）

## 备注

- **先例可完全照搬**：本次只是扩大 sales-agent 的 UI 模式到 chatbot + SOP。`.lock-badge` 样式已存在，不用重新设计。
- **与 S7 tag prod 无关**：本 feature 独立，不阻塞已完成的 sales-agent-child-permission。
- **加速理由**：4 个业务决策已穷尽；技术风险 R1-R5 在 S2 都有明确缓解方向，无真正未知。
