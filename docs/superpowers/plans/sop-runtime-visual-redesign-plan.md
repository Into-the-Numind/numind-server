# SOP 运行页视觉 + IA 重设计 — Implementation Plan (S3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

---

## 0. 元信息

| 字段 | 值 |
|---|---|
| feature_id | `sop-runtime-visual-redesign` |
| ndf_version | 1.0 |
| stage | S3 Plan |
| created_at | 2026-04-11 |
| author | Claude + 项目负责人 |
| repos | `numind-server`（轻）、`numind-web-v3`（重） |
| 源 spec | `numind-server/docs/superpowers/specs/2026-04-11-sop-runtime-visual-redesign-design.md` |
| 源 proposal | `numind-server/proposals/sop-runtime-visual-redesign-proposal.md` |
| 源 requirement | `numind-server/requirements/sop-runtime-visual-redesign.md` |
| 后端 audit | `numind-server/proposals/sop-runtime-visual-redesign-backend-audit.md` |
| Mockup 01 | `numind-server/proposals/sop-runtime-visual-redesign-mockups/01-active-and-history.html` |
| Mockup 02 | `numind-server/proposals/sop-runtime-visual-redesign-mockups/02-additional-states.html` |
| Branches | `feature/sop-runtime-visual-redesign`（两仓库各一支） |

**Goal：** 把已经工程化但视觉停在 legacy 的 `SOPRunView` 及其子组件改造为与 mockup 像素级对齐的 γ Focused 布局（左 vertical step nav + 右主区单步聚焦），覆盖 6 个状态（A/B/C/D/E/F），并补齐后端 2 个小字段（`sop_node_run.model_name` / `sop_chat_message.duration_ms`）支撑 footer 元信息。

**核心原则：**
1. 不动后端业务逻辑（SSE 协议 / 权限 / 配额 / 执行引擎全部冻结）
2. 不动 store 核心 action（只加 `viewingStep` 双指针 + 3 个新 action）
3. 组件树按 spec §3.1 严格落地，不擅自增减
4. 所有销毁性操作走 `ConfirmModal`（ui-ux.md 硬规则 4）
5. CSS 走 scoped token，禁止污染 `:root`

---

## 1. Plan 总览

### 1.1 Task 数量统计

| 类别 | 数量 | Task ID 区间 |
|---|---|---|
| 后端（numind-server） | 5 | B1–B5 |
| 前端（numind-web-v3） | 14 | F0–F13 |
| 验证（Rule 10 独立 task） | 1 | V1 |
| **合计** | **20** | — |

### 1.2 依赖图（Happens-Before）

```
[numind-server]                                                   [numind-web-v3]

B1 migration sop_node_run.model_name                                       │
B2 migration sop_chat_message.duration_ms                                  │
  │                                                                         │
  └─ B3 GORM model 加字段（SopNodeRun.ModelName, SopChatMsg.DurationMs）   │
         │                                                                  │
         └─ B4 biz 写入（节点执行 / chat 消息保存同步落字段）                │
                │                                                            │
                └─ B5 DTO / Response 透出（RunChatMessageItem 补字段 +       │
                       controller chat list 映射）                            │
                       │                                                      │
                  ═══ Cross-repo gate ═══                                     │
                  后端 merge develop + dev deploy + curl 验证字段             │
                       │                                                      │
                       └─────────────────────────────────────────────────────▶│
                                                                              │
                     F0 scoped token 基建（CSS 变量 + 全局 reset scope）     │
                       │                                                      │
                       ├── F1 store 改造（viewingStep + getters + actions）  │
                       │                                                      │
                       ├── F2 api 层改造（saveBookmark / removeBookmark +    │
                       │      createRun 默认 auto_apply_bookmarks + 类型扩展）│
                       │                                                      │
                       ├── F3 TopBar + StepNav + StepNavItem（导航骨架 +     │
                       │      §3.2 状态机伪代码落地 + 附录 B 单测）           │
                       │                                                      │
                       ├── F4 StepCanvas + SopStepView（主区路由器）         │
                       │                                                      │
                       ├── F5 InputCard（封装 StepInput） ─┐                 │
                       ├── F6 OutputCard + OutputEmpty +   ├── 并行          │
                       │      MetaFooter（封装 StepOutput） │                 │
                       ├── F7 ActionRow + HistoryViewStrip ─┘                │
                       │                                                      │
                       ├── F8 bookmark UI 集成（⭐ toggle + ConfirmModal）   │
                       │                                                      │
                       ├── F9 停止生成 UI（OutputCard head 按钮 + 片段处理）│
                       │                                                      │
                       ├── F10 TrailingChat + ChatBubble + ChatComposer      │
                       │                                                      │
                       ├── F11 SOPRunView 主容器重写 + initialize 改造       │
                       │       （依赖 B5 已 deploy → 才能读真实 meta 字段）  │
                       │                                                      │
                       ├── F12 单元测试更新 + 新增（StepNav/OutputCard/Meta）│
                       │                                                      │
                       └── F13 E2E selector 迁移 + 3 条新关键路径            │
                                │                                              │
                                └── V1 S5 验证策略（Playwright + gstack /qa  │
                                    + 后端 curl 复验，NDF Rule 10 要求最后一 │
                                    个独立 task）                              │
```

### 1.3 关键节点（Blocker / 关键路径）

| 节点 | 说明 | 为什么是 blocker |
|---|---|---|
| **B1+B2+B3+B4+B5 连续 5 task** | 后端字段链路 | F11 主容器最终要读 `model_name` / `duration_ms` 才能渲染 E/F 状态的 MetaFooter。没后端 = 前端只能看到 `""`，MetaFooter 整条不渲染，无法对齐 mockup E/F |
| **Cross-repo gate（B5 → dev deploy）** | 跨仓库部署门 | 这是 Rule 9 task 原子性的扩展：前端 F11 的验收标准依赖 dev 环境的 curl 复验。未 deploy 则 F 系列的验收跑不完 |
| **F0 scoped token 基建** | CSS 变量层 | F3–F11 所有组件样式都要引用 `var(--space-*)` / `var(--accent)`。没有 F0，后续组件 CSS 都会暴雷（reviewer 必拒） |
| **F1 store 改造** | viewingStep 双指针 | F3 StepNav / F4 StepCanvas / F11 主容器全部依赖 `viewingStepStatus` getter。没 F1 就没有状态机源 |
| **F11 SOPRunView 主容器重写** | 最终集成 | 所有子组件经 F11 串起来。F11 完成后系统可以跑完 A → C → D → E → F 全流程 |
| **V1 S5 验证策略** | 最后 gate | Rule 10 要求，V1 通过才能进入 S5 阶段 |

**Demo 里程碑**：
- B5 + F0 + F1 + F3 + F4 + F5 + F6 + F11 完成 → 基本可 demo 状态 A/B/C/D/E（bookmark/stop/chat/tests 未就绪）
- 再加 F8 + F9 + F10 → 全 6 状态可 demo
- 再加 F12 + F13 → 可交付 S4 完成

---

## 2. Task 列表

> **NDF Rule 9 task 原子性**：每个 task 是一个独立 commit 单位，有明确独立验收条件，完成后系统可编译可运行。
> **NDF Rule 6**：每个 task 完成后 → commit → Spec Compliance Review (Sonnet) → Code Quality Review (Sonnet) → 两个都 PASS → 才能开始下一个 task。
> **NDF Rule 8**：每个 implementer subagent 返回后主控必须 `git log --oneline -1 && git status` 验证 commit。

---

### Task B1：migration — sop_node_run.model_name

- **仓库**：numind-server
- **类别**：backend / migration
- **依赖**：无
- **预计文件改动**：1 个新建
- **预计代码量**：~10 行 SQL

#### Scope

加一列 `model_name VARCHAR(64) NOT NULL DEFAULT ''` 到 `sop_node_run` 表。用于前端 mockup E 态 footer 展示。虽然可以从 `sop_node.model_id` 反查，但反查性能差且模板换模型后会污染历史记录；冗余落在 node_run 上最合理。**见 spec §4.1**。

#### 文件

- `numind-server/migrations/20260411_120000_add_sop_node_run_model_name.sql`（新建）：加列 + 注释回滚

#### 实现要点

```sql
ALTER TABLE sop_node_run
  ADD COLUMN model_name VARCHAR(64) NOT NULL DEFAULT '' AFTER latency_ms;
-- 回滚：ALTER TABLE sop_node_run DROP COLUMN model_name;
```

- 使用 `AFTER latency_ms` 确保字段位置紧邻耗时，方便 DBA 看 schema
- 默认值为空字符串（MetaFooter 会识别空字符串整段不渲染，见 R7）
- 必须是幂等的：加 `IF NOT EXISTS` 的等价写法（MySQL 8 支持）或注释里提示执行前检查

#### 验收标准

- [ ] 文件存在于 `numind-server/migrations/`
- [ ] 文件名遵循 `YYYYMMDD_HHMMSS_description.sql` 格式
- [ ] 本地 dev 数据库执行 migration 成功：`mysql numind_dev < migrations/20260411_120000_add_sop_node_run_model_name.sql`
- [ ] `DESCRIBE sop_node_run;` 能看到 `model_name` 字段
- [ ] 回滚 SQL 在注释里给出

---

### Task B2：migration — sop_chat_message.duration_ms

- **仓库**：numind-server
- **类别**：backend / migration
- **依赖**：无（可与 B1 并行）
- **预计文件改动**：1 个新建
- **预计代码量**：~10 行 SQL

#### Scope

加两列到 `sop_chat_message` 表：`model_name VARCHAR(100) NOT NULL DEFAULT ''` 和 `duration_ms BIGINT NOT NULL DEFAULT 0`。两者用于 trailing chat 每条 AI 消息 `.msg__meta` 显示模型名 + 耗时。**见 spec §4.2**。

> **P0-1 修复说明**：之前 backend audit §5 错误地声称"消息表字段含 model_name"；实际 `SopChatMsg`（sop.go:156-170）无 `model_name` 字段，必须在本 migration 新增（否则 B3/B5 无法对齐）。

#### 文件

- `numind-server/migrations/20260411_120100_add_sop_chat_message_model_name_and_duration_ms.sql`（新建）

#### 实现要点

```sql
ALTER TABLE sop_chat_message
  ADD COLUMN model_name VARCHAR(100) NOT NULL DEFAULT '' AFTER thinking,
  ADD COLUMN duration_ms BIGINT NOT NULL DEFAULT 0 AFTER model_name;
-- 回滚：
-- ALTER TABLE sop_chat_message DROP COLUMN duration_ms;
-- ALTER TABLE sop_chat_message DROP COLUMN model_name;
```

- `AFTER thinking` / `AFTER model_name` 保证字段次序合理
- 老数据 `duration_ms` 为 0 / `model_name` 为 ''，前端 MetaFooter 遇缺失值视为"缺失"整段不渲染

#### 验收标准

- [ ] 文件存在且命名合规
- [ ] dev 数据库执行成功
- [ ] `DESCRIBE sop_chat_message;` 可见新列 `model_name` 和 `duration_ms`
- [ ] 回滚 SQL 注释给出

---

### Task B3：GORM model 加字段

- **仓库**：numind-server
- **类别**：backend / model
- **依赖**：B1, B2（migration 先落库）
- **预计文件改动**：1 个修改
- **预计代码量**：~6 行 Go

#### Scope

在 `internal/pkg/model/sop.go` 里：
- 给 `SopNodeRun` 加 `ModelName string`（sop.go:87 LatencyMs 之后）
- 给 `SopChatMsg` 加 **`ModelName string` + `DurationMs int64`** 两个字段（sop.go:163 Thinking 之后）

注意 **不要加 `json:"-"`**，前端需要读。见 spec §4.3。

> **P0-1 修复**：`SopChatMsg` 当前（sop.go:156-170）**无** `model_name` 字段（只有 Role / Content / Thinking / Seq / *Tokens 等），必须新增，与 DurationMs 一起加。之前 backend audit 的描述错误。

#### 文件

- `internal/pkg/model/sop.go`（修改）：`SopNodeRun` 加 1 字段，`SopChatMsg` 加 2 字段

#### 实现要点

```go
// SopNodeRun — 在 LatencyMs 之后
LatencyMs  int64  `gorm:"default:0" json:"latency_ms"`
ModelName  string `gorm:"size:64;default:''" json:"model_name"`

// SopChatMsg — 在 Thinking 之后（两个字段一起加）
Thinking   string `gorm:"type:longtext" json:"thinking,omitempty"`
ModelName  string `gorm:"size:100;default:''" json:"model_name"`
DurationMs int64  `gorm:"default:0" json:"duration_ms"`
```

- GORM tag 与 migration 对齐（size:64 / default:0）
- json tag 使用 snake_case 与 DB 列一致
- 确认 `TableName()` 方法不用改

#### 验收标准

- [ ] `cd numind-server && task lint` 通过
- [ ] `go build ./...` 通过
- [ ] GORM AutoMigrate（如启用）幂等（字段已存在不报错）
- [ ] 单元测试：存在则跑 `go test ./internal/pkg/model/...`

---

### Task B4：biz 写入 model_name / duration_ms

- **仓库**：numind-server
- **类别**：backend / biz
- **依赖**：B3
- **预计文件改动**：1–2 个修改
- **预计代码量**：~15 行 Go

#### Scope

在节点执行的两个写入点都加 `"model_name": node.ModelName` 到 updateData map：
- **成功路径**：`biz/sop/sop.go` 约 **line 676-700**（`SopStatusSucceeded` 分支，`updateData := map[string]interface{}{...}` 起始于 line 676，`UpdateNodeRun` 调用在 line 700）
- **失败路径**：`biz/sop/sop.go` 约 **line 656-672**（`if err != nil` 分支，`updateData := map[string]interface{}{"status": SopStatusFailed, ...}` 起始于 line 656，`UpdateNodeRun` 在 line 671）

两个 map 都要加 `"model_name": node.ModelName`。

在 chat 消息保存路径（~line 1303-1330）记录 `chatStart := time.Now()` 并在 `assistantMsg` struct 填 `DurationMs: time.Since(chatStart).Milliseconds()` + `ModelName: <当前使用的模型名>`（从 node.ModelName 或 chat endpoint 的 modelKey 取）。见 spec §4.4。

#### 文件

- `internal/numind/biz/sop/sop.go`（修改）：
  - 成功路径 updateData map（line 676-700）加 `"model_name": node.ModelName`
  - 失败路径 updateData map（line 656-672）加 `"model_name": node.ModelName`
- `internal/numind/biz/sop/sop.go`（或 chat.go，修改）：chatStart 计时 + assistantMsg.DurationMs + assistantMsg.ModelName 赋值

#### 实现要点

- **关键边界**：`node *model.SopNode` 是当前执行的目标节点（上下文已加载），其 `ModelName` 字段即节点配置的模型名
- 失败路径同样落 model_name，不要漏（失败记录也要能显示是哪个模型失败）
- 两个写入点都必须改；遗漏失败路径会导致失败记录前端 MetaFooter 无模型名
- chat 计时从 `ExecuteNodeStreamWithThinking` 调用前开始，到流式结束后截止
- 不动现有 LatencyMs 计算逻辑

#### 验收标准

- [ ] `task lint` 通过
- [ ] `go test ./internal/numind/biz/sop/...` 通过（若有测试）
- [ ] 本地 dev 跑一次 SOP 节点执行后，`SELECT model_name FROM sop_node_run ORDER BY id DESC LIMIT 1;` 能看到非空模型名
- [ ] 本地 dev 跑一次 trailing chat 后，`SELECT duration_ms FROM sop_chat_message WHERE role='assistant' ORDER BY id DESC LIMIT 1;` 能看到 > 0 的值

---

### Task B5：DTO / Response 透出

- **仓库**：numind-server
- **类别**：backend / controller + DTO
- **依赖**：B4
- **预计文件改动**：2 个修改
- **预计代码量**：~15 行 Go

#### Scope

三处 DTO / Controller mapping 修改：

1. **`RunChatMessageItem`（`pkg/api/numind/v1/sop.go:319-330`）新增 `ModelName string` + `DurationMs int64` 两字段**。当前 DTO 字段：ID / Role / Content / Thinking / CreatedAt / PromptTokens / CompletionTokens / TotalTokens / ReasoningTokens / EstimatedPromptTokens，**两个新字段都不存在**（backend audit 描述错误）。Controller 的 chat messages list handler 在 DTO mapping 时显式赋值 `msg.ModelName = chatMsg.ModelName; msg.DurationMs = chatMsg.DurationMs`。

2. **`CompletedNodeInfo`（`pkg/api/numind/v1/sop.go:191-203`，即 `/v1/sop/runs/:id/status` 响应中 `completed_nodes[]` 元素）新增 `LatencyMs int64` + `ModelName string` + `TotalTokens int` 三个字段**。当前 DTO 缺失这三个字段。Controller 的 status handler 在 mapping 时显式赋值：`info.LatencyMs = nodeRun.LatencyMs; info.ModelName = nodeRun.ModelName; info.TotalTokens = nodeRun.TotalTokens`。**注意：`SopNodeRun.TotalTokens` 在 model 层标注 `json:"-"`（sop.go:95），不能靠自动序列化；必须在 controller 显式赋值绕过 json tag。** 不改 model 的 json tag（避免影响其他接口）。

3. `SopNodeRun` 已经直接 json 序列化 `model_name`（B3 已保证），不需额外改动 —— **但**由于 token 字段被 json:"-" 屏蔽，前端只有通过 `CompletedNodeInfo` DTO（第 2 点）才能拿到 `total_tokens`。

见 spec §4.5。

#### 文件

- `pkg/api/numind/v1/sop.go`（修改）：
  - `RunChatMessageItem` 加 `ModelName` + `DurationMs`
  - `CompletedNodeInfo` 加 `LatencyMs` + `ModelName` + `TotalTokens`
- `internal/numind/controller/v1/sop/sop.go`（修改）：
  - list chat messages handler mapping 赋值 `ModelName` / `DurationMs`
  - status handler mapping 赋值 `LatencyMs` / `ModelName` / `TotalTokens`

#### 实现要点

- **P0-1 + P0-3 修复**：之前描述"model_name 在 chat message 表原本就有，重点是 RunChatMessageItem 是否已包含；审查一遍"是错误的 —— 两个字段 DTO 中都不存在，必须明确新增。token 字段走 DTO mapping 不改 model json tag。
- 不在 controller 写业务逻辑，只做字段映射
- mapping 点：在现有 list/status handler 的 DTO 构造循环中加 3–5 行赋值即可

#### 验收标准

- [ ] `task lint` 通过
- [ ] `go build ./...` 通过
- [ ] `curl $LOCAL_API_URL/v1/sop/runs/{id}/chat-messages -H "Authorization: Bearer $TOKEN"` 响应 JSON 中 `data.messages[].duration_ms` 和 `.model_name` 字段存在
- [ ] `curl $LOCAL_API_URL/v1/sop/runs/{id}/status -H "Authorization: Bearer $TOKEN"` 响应 `data.completed_nodes[].model_name` / `.latency_ms` / `.total_tokens` 字段存在

---

### === Cross-Repo Gate：B5 → dev deploy ===

B1–B5 全部完成并双 review PASS 后：

1. 本地 `git checkout develop && git merge --no-ff feature/sop-runtime-visual-redesign`
2. `git push origin develop`（触发 CI/CD 部署到 dev `49.233.219.254:9091`）
3. SSH 到 dev 执行 migration：`sshpass -p "$DEV_SSH_PASS" ssh ... "mysql numind_dev < /path/to/migration1.sql && mysql numind_dev < /path/to/migration2.sql"`
4. curl dev 验证：
   ```bash
   curl https://49.233.219.254:9091/v1/sop/runs/1/chat-messages -H "Authorization: Bearer $TOKEN" | jq '.data.messages[0].duration_ms'
   curl https://49.233.219.254:9091/v1/sop/runs/1 -H "Authorization: Bearer $TOKEN" | jq '.data.nodeRuns[0].model_name'
   ```
5. 确认字段可读后，才能开始 F 系列 task

详见 §3。

---

### Task F0：scoped token 基建

- **仓库**：numind-web-v3
- **类别**：frontend / styles
- **依赖**：无（可与 B 系列并行）
- **预计文件改动**：1 个新建 + 1 个修改
- **预计代码量**：~120 行 CSS/Vue

#### Scope

在 `SOPRunView.vue` 根 `.sop-run-view-v2` class 上定义本页面专用的 CSS 变量（颜色 / 间距 / 圆角 / 字体 / 阴影 / transition），**不污染 :root**。提取 spec §5.1 清单（~25 个 token）。后续所有新建组件的 `<style scoped>` 都从这些变量读。见 spec §5.1。

#### 文件

- `src/views/sop/SOPRunView.vue`（修改）：在顶层 `<template>` 外层 div 加 `class="sop-run-view-v2"`，`<style scoped>` 内定义 `.sop-run-view-v2 { --bg: #fff; --surface: #fff; ... }`
- `src/views/sop/styles/tokens.css`（新建，可选）：作为独立 scoped 样式文件，用 `@import` 到 SOPRunView（或内联到 style block）

#### 实现要点

- 遵照 spec §5.1 提取 token 清单（~25 个）
- CSS 变量名与 mockup 一致：`--bg` / `--surface` / `--text` / `--accent` / `--space-xs..4xl` / `--radius-*` / `--shadow-*`
- 不要在此 task 落任何 UI 结构，纯变量 + reset
- reset 只包含 `* { box-sizing: border-box }` 类的本页面 scope reset

#### 验收标准

- [ ] `cd numind-web-v3 && npm run lint && npm run type-check` 通过
- [ ] 页面打开无视觉回归（因为还没落新结构）
- [ ] grep 确认 `:root` 没被新增污染：`grep -rn ':root' src/views/sop/` 结果为空
- [ ] 变量定义完整对应 spec §5.1 表格（25 项）
- [ ] **P2-3 修复**：对照根目录 `DESIGN.md`，每个新增 token 在注释中标记是否有等价全局变量（如 `/* 对齐 DESIGN --color-accent-green-500 */` 或 `/* scope-only: 无对应 DESIGN token */`）。不强制替换成全局变量，记录即可

---

### Task F1：store 改造（viewingStep 双指针）

- **仓库**：numind-web-v3
- **类别**：frontend / store
- **依赖**：无（可与 F0 并行）
- **预计文件改动**：1 个修改 + 1 个新建（测试）
- **预计代码量**：~80 行 TS + ~150 行测试

给 `src/stores/sopRun.ts` 加 `viewingStep: Ref<number>` state、4 个 getters（`viewingNode` / `isViewingTrailingChat` / `viewingStepStatus` / `isViewingHistory`）、**4 个 actions**（`setViewingStep` / `returnToCurrentTask` / `advanceCurrentStep` / `refreshNodeRun`）。`currentStep` 语义收窄为"当前任务指针"，`viewingStep` 为"正在看的步骤"。不变量：`viewingStep <= currentStep`。见 spec §3.3。

> **P0-2 修复**：新增 `refreshNodeRun(nodeId: number)` action —— 内部调用 `fetchRunStatusDetail(currentRun.id)`，从响应的 `completed_nodes[]` 中找到对应 nodeId 的元素，提取 `model_name` / `latency_ms` / `total_tokens` 合并更新 `nodeRuns[nodeId]`。供 F11 的 `onDone` 回调使用（SSE done payload 不含这些字段，必须拉 /status 补齐）。

#### 文件

- `src/stores/sopRun.ts`（修改）：加 state + getters + actions
- `src/stores/__tests__/sopRun.spec.ts`（新建或修改）：覆盖 `viewingStepStatus` 的 6 种状态转换 + `returnToCurrentTask` + 不变量

#### 实现要点

- `viewingStepStatus` 的 6 个返回值完全对应 spec §3.3：`'draft-first' | 'active' | 'executing' | 'done-current' | 'done-history' | 'trailing'`
- `setViewingStep(step)` 内部必须守 `step <= currentStep.value`，否则 no-op + warn
- `advanceCurrentStep()` 同步把 `viewingStep` 推到新的 `currentStep`（语义：执行完成后自动 focus 下一步）
- `refreshNodeRun(nodeId)` 实现：
  ```typescript
  async function refreshNodeRun(nodeId: number) {
    if (!currentRun.value) return
    const detail = await fetchRunStatusDetail(currentRun.value.id)
    const info = detail.completed_nodes?.find((n) => n.node_id === nodeId)
    if (!info) return
    const prev = nodeRuns.value[nodeId] ?? {}
    nodeRuns.value[nodeId] = {
      ...prev,
      model_name: info.model_name ?? '',
      latency_ms: info.latency_ms ?? 0,
      total_tokens: info.total_tokens ?? 0,
    }
  }
  ```
- 单测用例至少覆盖：draft C 态 / active A 态 / executing D 态 / done E 态 / history B 态 / trailing F 态；`refreshNodeRun` mock fetchRunStatusDetail 验证 nodeRuns 合并

#### 验收标准

- [ ] `npm run type-check` 通过
- [ ] `npm run test:unit -- sopRun` 通过，6 种状态全覆盖
- [ ] 不变量测试：尝试 `setViewingStep(currentStep + 1)` 时 viewingStep 不变
- [ ] 现有代码对 `currentStep` 的读写未破坏（搜索引用确认）

---

### Task F2：api 层改造

- **仓库**：numind-web-v3
- **类别**：frontend / api + types
- **依赖**：无（可与 F0/F1 并行）；字段读取依赖 B5 dev deploy，但代码落地不依赖
- **预计文件改动**：2 个修改
- **预计代码量**：~60 行 TS

#### Scope

1. 在 `src/api/sop.ts` 新增 `saveBookmark` / `removeBookmark` 封装（spec §3.4）
2. `createRun` 默认 payload 加 `auto_apply_bookmarks: true`（caller 可覆盖）
3. 在 `src/views/sop/types.ts` 的 `SopNodeRun` 加 `model_name: string`；新增 `SopChatMessageMeta` 类型
4. `src/api/sop.ts` 的 `RunChatMessageItem` 类型补 `model_name` / `duration_ms`

见 spec §3.4, §3.5。

#### 文件

- `src/api/sop.ts`（修改）：新增 2 个函数、修改 createRun 默认参数、扩展 `RunChatMessageItem`
- `src/views/sop/types.ts`（修改）：扩展 `SopNodeRun` + 新增 `SopChatMessageMeta`

#### 实现要点

```typescript
export const createRun = async (body: CreateRunRequest): Promise<CreateRunResponse> => {
  const payload = { auto_apply_bookmarks: true, ...body } // body 后置允许覆盖
  const res = await request.post('/v1/sop/runs', payload)
  return (res as unknown as { data: CreateRunResponse }).data
}
```

- `saveBookmark` / `removeBookmark` 签名与 spec §3.4 一致
- 注意 `composables/useDraftLifecycle.ts` 调用 `createRun` 的地方不用传 flag（自动受益）
- 所有 HTTP 请求通过 `request`（不 import axios）

#### 验收标准

- [ ] `npm run type-check` 通过
- [ ] `npm run lint` 通过
- [ ] `MetaFooter` 组件（F6 task 会用到）可以从 `SopNodeRun.model_name` 读到字段（类型检查不报错）
- [ ] 调用 `createRun({ template_id: 1, first_input_text: 'test' })` 的 body 自动含 `auto_apply_bookmarks: true`（单测或 devtools 实证）

---

### Task F3：TopBar + StepNav + StepNavItem

- **仓库**：numind-web-v3
- **类别**：frontend / component
- **依赖**：F0, F1
- **预计文件改动**：3 个新建 + 1 个单测
- **预计代码量**：~350 行 Vue + ~250 行测试

#### Scope

3 个新组件：
- `TopBar.vue`：slim 56px 顶栏，`[← 返回] | [模板名] | [历史 icon]`（spec §5.2）
- `StepNav.vue`：左 264px vertical nav，两组（主流程 / 追问）（spec §5.2）
- `StepNavItem.vue`：单条 step 5 态（`active` / `done` / `viewing` / `pending-return` / `disabled`）（spec §3.2 + 附录 B 伪代码）

内部 `computeStepState()` 函数严格按 spec 附录 B 伪代码落地，写单测覆盖 10+ 种输入组合。

#### 文件

- `src/views/sop/components/TopBar.vue`（新建）
- `src/views/sop/components/StepNav.vue`（新建）
- `src/views/sop/components/StepNavItem.vue`（新建）
- `src/views/sop/components/__tests__/StepNav.spec.ts`（新建）：覆盖 5 态快照 + navigate emit + `computeStepState` 10+ 用例

#### 实现要点

- props/emits 与 spec §5.2 严格对齐
- `computeStepState` 作为 StepNav 内的纯函数 **导出**（方便单测）
- 对齐 mockup 01 行 733-785 与 02 行 930-980 的 DOM 类名
- 加 `data-testid="sop-nav-item"` 方便 E2E 定位
- 使用 `lucide-vue-next` icon，禁止 unicode `←`
- 严格从 F0 的 token 读 CSS（无 hex 字面量）

#### 验收标准

- [ ] `npm run type-check` 通过
- [ ] `npm run lint` 通过
- [ ] `npm run test:unit -- StepNav` 通过，所有 5 态渲染正确
- [ ] 点击可访问 step → emit 'navigate' 携带正确 step index
- [ ] 视觉对比：在 Playbook / Storybook（若有）下渲染与 mockup `01 行 733-785` 像素级对齐
- [ ] CSS grep：`grep -rn '#' src/views/sop/components/StepNav*.vue` 无 hex 字面量（除 SVG fill 等合法场景）

---

### Task F4：StepCanvas + SopStepView（主区路由器）

- **仓库**：numind-web-v3
- **类别**：frontend / component
- **依赖**：F0, F1
- **预计文件改动**：2 个新建
- **预计代码量**：~250 行 Vue

#### Scope

`StepCanvas.vue` 是主区路由器，根据 `store.viewingStepStatus` 决定渲染 `SopStepView`（SOP 节点）还是 `TrailingChat`（F10 task 会实现，此 task 先预留 placeholder 或 async import）。`SopStepView.vue` 组合 `HistoryViewStrip`（F7 落地）+ step header + 根据状态渲染 `InputCard` (F5) / `OutputCard` (F6) / `OutputEmpty` / `ActionRow` (F7)。

此 task 先 **渲染最小可见骨架**（step header + 文字 placeholder "内容加载中..." 代替未就绪的子组件）+ 路由器逻辑可工作，保证 F4 commit 后页面可运行（非空骨架）。实际子组件在 F5–F10 填肉。

#### 文件

- `src/views/sop/components/StepCanvas.vue`（新建）
- `src/views/sop/components/SopStepView.vue`（新建）

#### 实现要点

- `StepCanvas` 不接受 props，直接从 store 读 `viewingStepStatus`
- `SopStepView` props: `{ node: SopNodePublic, status: ViewingStepStatus }`
- 子组件用 `defineAsyncComponent` 或普通 placeholder div（后续 task 替换）
- step header 的 description 为 null 时 **不渲染描述行**（R4）

#### 验收标准

- [ ] `npm run type-check` 通过
- [ ] **浏览器打开页面能看到非空骨架**（step header 标题 + 文字 placeholder "内容加载中..."），不是空白屏幕
- [ ] 在 demo 页面渲染 StepCanvas，切换 viewingStep → 组件自动切换子视图（占位可见）
- [ ] mockup 结构骨架对齐（main + canvas 两层 div）

---

### Task F5：InputCard（封装 StepInput）

- **仓库**：numind-web-v3
- **类别**：frontend / component
- **依赖**：F0
- **预计文件改动**：1 个新建 + 1 个修改
- **预计代码量**：~200 行 Vue

#### Scope

封装原 `StepInput.vue` 的 `compose()` + 文件上传 + 字数统计逻辑为 `InputCard.vue`（spec §5.2）。`StepInput.vue` 同时修改：去掉 card wrapper，只保留 textarea + 隐式上传逻辑，由 `InputCard` 提供外层 card 样式 + toolbar。

#### 文件

- `src/views/sop/components/InputCard.vue`（新建）
- `src/views/sop/components/StepInput.vue`（修改）：去 card wrapper

#### 实现要点

- 保留 `useFileUpload` / `useInputPersistence` composable 复用
- 不破坏 draft lazy create 链路（`ensureRun` prop 传入）
- mockup 引用：02 行 989-1030
- 执行按钮调用 emit('execute')；streaming 状态下切换为 emit('stop')

#### 验收标准

- [ ] `npm run type-check` / `lint` 通过
- [ ] 现有 StepInput 单测更新 snapshot 后通过
- [ ] 视觉对比 mockup 02 state C `.card` 对齐
- [ ] 手动测：输入 + 上传文件 + 点执行 → draft run 创建（现有流程不破坏）

---

### Task F6：OutputCard + OutputEmpty + MetaFooter

- **仓库**：numind-web-v3
- **类别**：frontend / component
- **依赖**：F0, F2（需 `SopNodeRun.model_name` 类型）；字段读取依赖 cross-repo gate
- **预计文件改动**：3 个新建 + 1 个修改 + 2 个单测
- **预计代码量**：~400 行 Vue + ~300 行测试

#### Scope

3 个新组件：
- `OutputCard.vue`：3 态（streaming / read-only / empty-skip），head 含 live-dot + tiny buttons（⭐ / 复制 / 停止），body 渲染 markdown，foot 嵌 MetaFooter
- `OutputEmpty.vue`：虚线边框纯视觉占位
- `MetaFooter.vue`：mono 小字 meta 行，缺字段整段不渲染，`model_name === ''` 整段不渲染

`StepOutput.vue` 同时去 card wrapper，保留 markdown + thinking + scroll follow 逻辑，被 `OutputCard` 包裹。

见 spec §5.2 + 附录 C（后端字段同步 checklist）。

#### 文件

- `src/views/sop/components/OutputCard.vue`（新建）
- `src/views/sop/components/OutputEmpty.vue`（新建）
- `src/views/sop/components/MetaFooter.vue`（新建）
- `src/views/sop/components/StepOutput.vue`（修改）：去 card wrapper
- `src/views/sop/components/__tests__/OutputCard.spec.ts`（新建）：3 态 + bookmark toggle emit
- `src/views/sop/components/__tests__/MetaFooter.spec.ts`（新建）：缺字段不渲染 / 全字段顺序

#### 实现要点

- MetaFooter 顺序（mockup 一致）：`[clock 耗时] · [cpu 模型] · [coin tokens] · [完成时间]`
- **后端字段防御**：`model_name === ''` 或 `latency_ms === 0` 时该段不渲染（R7）
- streaming 时 head 的 tiny buttons 被"停止生成"按钮替换
- Markdown 渲染走现有 `markdown.ts` 工具，不新增

#### 验收标准

- [ ] `type-check` + `lint` 通过
- [ ] 两个新单测全绿
- [ ] 视觉：3 态与 mockup 01 行 914-968（B 态）、02 D/E 态像素对齐
- [ ] MetaFooter：给定 `{ modelName: '', ... }` 整段不渲染（单测覆盖）

---

### Task F7：ActionRow + HistoryViewStrip

- **仓库**：numind-web-v3
- **类别**：frontend / component
- **依赖**：F0
- **预计文件改动**：2 个新建
- **预计代码量**：~180 行 Vue

#### Scope

- `ActionRow.vue`：主区底部按钮行，主 CTA（执行 / 下一步 / 返回步骤 N）+ 次 CTA（重新生成 / ⭐）
- `HistoryViewStrip.vue`：State B 时主区顶部的 info strip，"正在查看历史步骤 · 输入不可修改"，右 CTA "返回当前步骤"（spec D5 硬约束要求）

#### 文件

- `src/views/sop/components/ActionRow.vue`（新建）
- `src/views/sop/components/HistoryViewStrip.vue`（新建）

#### 实现要点

- ActionRow props 符合 spec §5.2：`primary: {label, icon?, disabled?}, secondary?`
- HistoryViewStrip 不在 mockup 有直接对应，自研视觉与 mockup 同系（border-bottom 分隔 + accent 竖线）
- mockup 引用：01 行 971-976 / 02 E 态

#### 验收标准

- [ ] `type-check` + `lint` 通过
- [ ] ActionRow primary/secondary emit 事件正确
- [ ] HistoryViewStrip emit 'return' 可触发 `store.returnToCurrentTask`

---

### Task F8：bookmark UI 集成

- **仓库**：numind-web-v3
- **类别**：frontend / component + integration
- **依赖**：F2（api 封装就位）、F6（OutputCard 就位）
- **预计文件改动**：1–2 个修改
- **预计代码量**：~80 行 Vue

#### Scope

`OutputCard` head 右上角的 `⭐ tiny-btn`：
- 未收藏 → 点击 `saveBookmark({ run_id, node_id })` → `useBookmarks.load`
- 已收藏 → 点击 **弹 ConfirmModal**（ui-ux.md 硬规则 4，销毁性操作必须确认）→ 确认 `removeBookmark(id)`
- State A/C 未有 output 时 ⭐ 隐藏

见 spec §5.4。

#### 文件

- `src/views/sop/components/OutputCard.vue`（修改）：实际接线 `saveBookmark` / `removeBookmark`
- `src/views/sop/components/SopStepView.vue`（修改）：监听 `toggle-bookmark` 事件并弹 ConfirmModal

#### 实现要点

- bookmark id 通过 `useBookmarks.getBookmarksForNode(nodeId)[0]?.id` 获取
- ConfirmModal 文案：`"将移除此节点的书签"`
- 成功 toast 通过 `notifications` store
- `createRun` 响应的 `auto_applied_count > 0` 时触发"已自动应用 N 个书签"toast

#### 验收标准

- [ ] `type-check` + `lint` 通过
- [ ] 手动测：收藏 → 刷新页面 → 新建 run → 自动应用 bookmark → output 与之前一致
- [ ] 手动测：取消收藏 → 弹 ConfirmModal → 确认 → 状态变线框
- [ ] `data-testid="bookmark-toggle"` 存在方便 E2E

---

### Task F9：停止生成 UI

- **仓库**：numind-web-v3
- **类别**：frontend / component + integration
- **依赖**：F6（OutputCard）
- **预计文件改动**：1–2 个修改
- **预计代码量**：~40 行 Vue

#### Scope

streaming 状态下（state D），OutputCard head 的 tiny buttons 换为"停止生成"按钮。点击 → `useSSEStream.abort()` → 保留 `store.streamingContent` 展示（内存保留不入 nodeRuns）→ 不标 `markNodeComplete`。见 spec §5.6 + Q6 决策（保留片段不入库）。

#### 文件

- `src/views/sop/components/OutputCard.vue`（修改）：streaming 分支切换按钮
- `src/views/sop/components/SopStepView.vue`（修改）：接线 stop 事件到 `useSSEStream.abort()`

#### 实现要点

- EventSource.close() 前端停止接收，后端继续跑完（D11 决策）
- 停止后 UI 回 state A，但 OutputCard body 仍显示已收片段
- 下次执行时 `setStreamingState` 清空覆盖

#### 验收标准

- [ ] 手动测：执行 → 流中点停止 → 按钮变回"执行" → 片段保留可见
- [ ] 再次点执行 → 从零开始新流 → 旧片段被覆盖

---

### Task F10：TrailingChat + ChatBubble + ChatComposer

- **仓库**：numind-web-v3
- **类别**：frontend / component
- **依赖**：F0, F6（MetaFooter 复用）
- **预计文件改动**：3 个新建 + 1 个修改
- **预计代码量**：~400 行 Vue

#### Scope

- `TrailingChat.vue`（新建）：headless 全铺聊天（无 step header），history + composer，沿用现有 `useSSEStream` 调 chat/stream
- `ChatBubble.vue`（修改）：加 `meta?: SopChatMessageMeta` prop，AI 气泡下方贴 `MetaFooter`（mockup 方案，Q5）
- `ChatComposer.vue`（新建）：sticky 底部 textarea + 发送/停止按钮

见 spec §5.2 + §3.2 state F。

#### 文件

- `src/views/sop/components/TrailingChat.vue`（新建）
- `src/views/sop/components/ChatComposer.vue`（新建）
- `src/views/sop/components/ChatBubble.vue`（修改）：加 meta prop
- `src/stores/sopRun.ts`（小改）：若需 `chatMessages` state，此 task 加

#### 实现要点

- TrailingChat 内部判空 → empty state "从这里开始追问"
- 消息列表从 `api.listRunChatMessages` 拉取 + stream append
- ChatComposer Enter 发送 / Shift+Enter 换行
- 不支持附件上传（后端未支持）

#### 验收标准

- [ ] `type-check` + `lint` 通过
- [ ] 手动测：F 态发送消息 → 流式 AI 回复 → meta 行显示模型 + 耗时（依赖 B5 已 deploy）
- [ ] mockup 02 F 态 `.chat` 视觉对齐

---

### Task F11：SOPRunView 主容器重写 + initialize 改造

- **仓库**：numind-web-v3
- **类别**：frontend / integration
- **依赖**：F0, F1, F2, F3, F4, F5, F6, F7, F8, F9, F10（几乎全部前置）
- **跨仓库依赖**：B5 已 merge develop + dev deploy（见 Cross-Repo Gate）
- **预计文件改动**：1 个修改（大改）+ 若干删除
- **预计代码量**：~500 行 Vue（含删除）

#### Scope

重写 `SOPRunView.vue` 的 template + style，从"垂直滚动列表"改为"topbar + 左 nav + 主区"三栏布局。保留 script 主干（store / composables 接入），删除 `StepperPanel.vue` / `ToolbarActions.vue` 引用。按附录 A 的组件交互矩阵接线。

> **P0-2 修复**：SSE `onDone` 回调里除了现有的 `setNodeRun` + `markNodeComplete` + `advanceCurrentStep` 外，**必须追加 `await store.refreshNodeRun(nodeId)`**（F1 task 已实现该 action）—— 内部拉 `/runs/:id/status` 补齐 `model_name` / `latency_ms` / `total_tokens`。否则 state E MetaFooter 永远缺字段不渲染。

> **P1-5 修复 / 跨仓库 gate 约束**：F11 可以先开发接线，但 **cross-repo gate 通过后才能完成此 task 的最终 commit**。合理流程：本地开发 → curl dev 环境验证 `/runs/:id/status` 的 `completed_nodes[]` 含 3 个新字段 → 确认 MetaFooter 能读到数据 → 才 commit。

#### 文件

- `src/views/sop/SOPRunView.vue`（大改）：template 全重写 + style 使用 F0 token
- `src/views/sop/components/StepperPanel.vue`（删除）
- `src/views/sop/components/ToolbarActions.vue`（删除）
- `src/views/sop/components/__tests__/StepperPanel.spec.ts`（删除）

#### 实现要点

- 顶层 `<div class="sop-run-view-v2">` 绑定 F0 token scope
- 布局：`TopBar` + `<div class="body">` 内 `StepNav` + `StepCanvas`
- 按附录 A 接线 14 个用户操作的触发路径
- 保留 `onMounted` 的 `loadTemplate` / `loadRun` / `enterDraftMode` / `navigation.restoreFromSession`
- `createRun` 响应的 `auto_applied_count > 0` → notifications.push toast
- 4 态处理（loading / empty / error / success）通过 `EmptyStateCard` 覆盖（ui-ux.md 硬规则 2）

#### 验收标准

- [ ] `type-check` + `lint` + `build` 通过
- [ ] 本地启动 dev server → 6 状态全部可达：
  - [ ] A: 访问 `/sop/run?templateId=1` 新建 → 默认 state A
  - [ ] B: 执行 step 1 → 点 step 1 nav → state B + HistoryViewStrip **[需要 cross-repo gate 已通过]**（MetaFooter 读 model_name + latency_ms + total_tokens）
  - [ ] C: 无 run 进入首次 → state C
  - [ ] D: 点执行 → 流式中 → state D
  - [ ] E: 执行完 → state E + MetaFooter 有 model_name + latency + total_tokens **[需要 cross-repo gate 已通过]**（onDone 触发 `refreshNodeRun` 后读取）
  - [ ] F: 进入 trailing chat → state F + 每条 AI 气泡 MetaFooter 含 model_name + duration_ms **[需要 cross-repo gate 已通过]**
- [ ] `data-testid` 全部就位方便 E2E
- [ ] 删除后 `grep -rn StepperPanel src/` 结果为空
- [ ] 跨仓库 gate 确认：dev 环境 curl 验证过 `/runs/:id/status.completed_nodes[].{model_name,latency_ms,total_tokens}` 和 `/runs/:id/chat-messages.messages[].{model_name,duration_ms}` 字段均返回正确

---

### Task F12：单元测试更新 + 新增

- **仓库**：numind-web-v3
- **类别**：frontend / test
- **依赖**：F3, F6, F11（组件稳定后）
- **预计文件改动**：若干修改 + 若干新建
- **预计代码量**：~400 行测试

#### Scope

**P2-4 修复：明确 scope，去除"若 F3/F6 未完成"的模糊兜底**。本 task 专注于 F1/F3/F6 task 未覆盖的部分：

- `StepInput.spec.ts` 更新 snapshot（template 变了）
- `StepOutput.spec.ts` 更新 snapshot
- `StepperPanel.spec.ts` 删除
- `sopRun.spec.ts` 补 **viewing state 用例**（F1 已做基础覆盖；此处补：从 B 态切回 A、historyView ↔ currentStep 切换、refreshNodeRun 合并后 nodeRuns 字段更新）

> 注：`StepNav.spec.ts` / `OutputCard.spec.ts` / `MetaFooter.spec.ts` 由 F3 / F6 task 直接产出，不在本 task 重复。

#### 文件

根据上述清单增删改。

#### 实现要点

- Vitest + @vue/test-utils
- 快照测试允许大量更新（视觉变更），但 prop/emit 断言不允许盖过

#### 验收标准

- [ ] `npm run test:unit` 全绿
- [ ] 覆盖率不低于 F1 阶段水平

---

### Task F13：E2E selector 迁移 + 3 条新关键路径

- **仓库**：numind-web-v3
- **类别**：frontend / e2e
- **依赖**：F11（主容器稳定）
- **预计文件改动**：若干修改 + 1–3 个新建
- **预计代码量**：~500 行 Playwright

#### Scope

1. 修 `e2e/sop-*.spec.ts` 现有 selector：
   - `.sop-stepper .step-item` → `[data-testid="sop-nav-item"]`
   - `ToolbarActions` 按钮 → `[data-testid="action-row"]` / `[data-testid="output-card"]`
2. 新增 3 条关键路径：
   - **E1**：bookmark save → 刷新 → 新 run → 自动应用验证
   - **E2**：view history → HistoryViewStrip 出现 → 返回
   - **E3**：stop generation → 片段保留

#### 文件

- `e2e/sop-execute.spec.ts`（修改）
- `e2e/sop-bookmark.spec.ts`（新建或扩展）
- `e2e/sop-history-view.spec.ts`（新建）
- `e2e/sop-stop-generation.spec.ts`（新建）

#### 实现要点

- 复用 `e2e/auth.setup.ts` 的 storage state
- 凭据通过 `E2E_USERNAME` / `E2E_PASSWORD` 环境变量
- `diagnose.ts` 辅助调试

#### 验收标准

- [ ] `E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e` 全绿
- [ ] 3 条新路径都产出 video/trace

---

### Task V1：S5 验证策略（NDF Rule 10 必须的最后独立 task）

- **仓库**：跨仓库（验证而非代码）
- **类别**：verification planning
- **依赖**：F13
- **预计文件改动**：1 个新建（验证文档）
- **预计代码量**：~200 行 markdown

#### Scope

本 task 的产出是 **S5 阶段的执行剧本**，不是代码。NDF Rule 10 要求 S3 plan 的最后一个独立 task 明确：验证方式 / 理由 / 关键用户路径 / 回归保护诚实声明。此 task 由 S3 gate 的独立 Sonnet reviewer 一并审查。

#### 文件

- `numind-server/docs/superpowers/plans/sop-runtime-visual-redesign-verification.md`（新建）：S5 执行剧本

#### 实现要点

内容对应本 plan 的 §4（见下）。V1 task 的实施就是"把 §4 内容固化成独立文档，可执行"。

#### 验收标准

- [ ] 文档存在
- [ ] 包含 4 个子 section（验证方式 / 关键用户路径 / 回归保护声明 / 命令）
- [ ] 至少覆盖 P1–P7 共 7 条用户路径
- [ ] S3 gate reviewer 审查通过

---

## 3. Cross-Repo Gate（跨仓库部署门）

### 3.1 为什么需要 gate

本 feature 跨 `numind-server` + `numind-web-v3` 两个独立仓库。后端加的 `model_name` / `duration_ms` 字段必须先部署到 dev 环境，前端 F6/F10/F11 的验收才能验证 MetaFooter 真实渲染（否则永远是空字符串不渲染，验收"过"但其实失效）。

### 3.2 Gate 触发时机

**触发**：B1–B5 全部完成并双 review PASS，准备开始 F 系列前。

**例外**：F0, F1, F2, F3, F4, F5, F7, F8, F9, F12 等**不直接读 meta 字段**的 task 可以在 gate 之前并行开始（因为它们的验收不依赖后端真实字段）。但 **F6 + F10 + F11 + F13 必须等 gate 通过**才能开始验收。

### 3.3 Gate 步骤

1. **本地 merge**：`cd numind-server && git checkout develop && git merge --no-ff feature/sop-runtime-visual-redesign`
2. **Push 触发 CI**：`git push origin develop` → dev 环境自动部署（约 3–5 分钟）
3. **Migration 执行**（AI 自动 SSH，参考 §7 credentials）：
   ```bash
   sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no "$DEV_SSH_USER@$DEV_SSH_HOST" \
     "mysql -u root numind_dev < /opt/numind-server/migrations/20260411_120000_add_sop_node_run_model_name.sql && \
      mysql -u root numind_dev < /opt/numind-server/migrations/20260411_120100_add_sop_chat_message_duration_ms.sql"
   ```
4. **Curl 复验**（不依赖硬编码 runId，先触发一次执行产生新数据）：
   ```bash
   TOKEN=$(curl -sX POST $DEV_API_URL/v1/web/login \
     -d '{"username":"'$E2E_USERNAME'","password":"'$E2E_PASSWORD'"}' | jq -r .data.token)

   # Step 4a: 先创建一个新 run，拿到 runId
   RUNID=$(curl -sX POST $DEV_API_URL/v1/sop/runs \
     -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"template_id": 1, "first_input_text": "gate check"}' | jq -r .data.id)

   # Step 4b: 触发首节点执行（需要新 run 至少跑一个节点产生 SopNodeRun 数据）
   # 使用 SSE execute endpoint 执行第一个节点 —— 也可直接跑一轮 trailing chat 拿 chat message
   # （具体调用由 gate 执行者根据模板情况决定）

   # Step 4c: 验证 /runs/:id/status 返回的 completed_nodes[] 字段是否齐全
   curl -s "$DEV_API_URL/v1/sop/runs/$RUNID/status" \
     -H "Authorization: Bearer $TOKEN" \
     | jq '(.data.completed_nodes | length > 0) and
           (.data.completed_nodes[0] | has("model_name") and has("total_tokens") and has("latency_ms"))'
   # 期望输出：true

   # Step 4d: 验证 chat messages 字段
   curl -s "$DEV_API_URL/v1/sop/runs/$RUNID/chat-messages" \
     -H "Authorization: Bearer $TOKEN" \
     | jq '.data.messages | (length == 0) or
           (.[0] | has("model_name") and has("duration_ms"))'
   # 期望输出：true（空列表也算通过，只要字段名存在于 schema）
   ```
5. **Gate 通过条件**：Step 4c 和 4d 的 jq 输出均为 `true`（字段存在，与具体值无关）。字段存在即证明 DTO mapping 正确。

### 3.4 Gate 失败处理

- curl 返回字段不存在 → migration 未执行或 controller DTO 映射漏字段 → 回到 B5 修复
- 字段全 0 / 全空 → biz 写入未生效 → 回到 B4 修复

---

## 4. S5 验证策略（Task V1 的内容）

### 4.1 验证方式

组合策略：
- **Playwright E2E（持久化）**：高风险业务逻辑路径 P1/P3/P4（bookmark 涉及配额/持久化，重新生成涉及配额扣减）
- **gstack /qa 视觉对比（一次性）**：6 个状态截图逐个与 mockup HTML 比对（P6）
- **后端 curl 安全复验**：直接 curl API 确认 meta 字段透出正确（P7）

**理由**：
- Playwright 覆盖会引发回归的业务逻辑（bookmark 持久化、重新生成的覆盖式写入）
- gstack /qa 覆盖纯视觉层面（没有 LOC 代码表达），一次性验证足够
- 纯后端字段透出用 curl 最快且免依赖前端

### 4.2 关键用户路径

| ID | 路径 | 方式 | 覆盖状态 |
|---|---|---|---|
| P1 | 进入无 runId → state C → 输入文本 → 执行 → state D 流式 → 完成 state E | Playwright | C, D, E |
| P2 | 切换历史 step → state B 只读 → HistoryViewStrip 出现 → 点返回 → state A | Playwright | A, B |
| P3 | 重新生成（弹 ConfirmModal） → 确认 → 旧 output 抹除 → 新 output 覆盖写入 sop_node_run | Playwright | E → D → E |
| P4 | 收藏 ⭐ → 新建 run → 验证 `auto_applied_count > 0` → 自动应用 toast 显示 | Playwright | E, C |
| P5 | 进入 trailing chat → 发消息 → 流式 AI 回复 → MetaFooter 显示模型 + 耗时 | Playwright | F |
| P6 | 6 个状态 gstack 截图 → 逐个与 mockup 01/02 比对 | gstack /qa | A, B, C, D, E, F |
| P7 | curl `/runs/:id` + `/runs/:id/chat-messages` → 验证 `model_name` / `duration_ms` 字段 | curl | — |

### 4.3 回归保护诚实声明（NDF Rule 10 要求）

- **Playwright E2E 是持久化测试，提供回归保护**：P1–P5 五条路径写为 spec 文件，未来本页面任何修改触发 CI 都会自动跑
- **gstack /qa 是一次性验证，不产生持久化测试代码**：P6 视觉对比不入代码仓库，未来视觉修改（例如改个间距）需要手动重跑 gstack /qa
- **curl 复验是一次性**：P7 只在 S5 执行，之后靠 Playwright 间接保证

**本 feature 选择 Playwright + gstack 组合的理由**：
- 视觉变更频率高 + mockup 已作为契约固化，用 gstack 截图比对成本最低
- 业务逻辑（bookmark/重新生成）不能依赖一次性验证，必须写 Playwright

**未来回归风险**：如果有人修改 SOP 运行页的 CSS 但不改业务逻辑，Playwright 可能全绿但视觉已跑偏。S6 阶段应人工截图再跑一次 gstack /qa 对比。

### 4.4 验证脚本 / 命令

```bash
# 前端 E2E
cd numind-web-v3
E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e -- \
  e2e/sop-execute.spec.ts \
  e2e/sop-bookmark.spec.ts \
  e2e/sop-history-view.spec.ts \
  e2e/sop-stop-generation.spec.ts

# 前端单测
npm run test:unit

# 前端 type check + lint
npm run lint && npm run type-check

# 后端 lint + test
cd ../numind-server
task lint
task test

# 后端 curl 复验（dev 环境）
TOKEN=$(curl -sX POST $DEV_API_URL/v1/web/login -d '{"username":"'$E2E_USERNAME'","password":"'$E2E_PASSWORD'"}' | jq -r .data.token)
curl -s $DEV_API_URL/v1/sop/runs/1 -H "Authorization: Bearer $TOKEN" | jq '.data.nodeRuns[] | {nodeId: .node_id, modelName: .model_name}'
curl -s $DEV_API_URL/v1/sop/runs/1/chat-messages -H "Authorization: Bearer $TOKEN" | jq '.data.messages[] | {role, modelName: .model_name, durationMs: .duration_ms}'

# gstack /qa 视觉对比（6 状态截图）
# 使用 gstack /qa skill，浏览器自动操作：登录 → 导航 → 触发每个状态 → 截图
# 截图归档到 numind-server/proposals/sop-runtime-visual-redesign-screenshots/
```

---

## 5. Manifest 进度更新约定

### 5.1 NDF Rule 4：阶段 + 进度同步

- **阶段转换**：S3 → S4 时，更新 `build-manifest.yaml` 的 `stage: "S4"`
- **每个 task 完成**：更新 `progress.completed_tasks += 1`
- **每个 task 双 review PASS**：更新 `progress.reviewed_tasks += 1`
- **session 结束前**：确认 manifest 是最新的

### 5.2 NDF Rule 6：双阶段 review（禁止跳过）

S4 每完成一个 task：

1. Commit 完成后 → **Spec Compliance Review**（dispatch Sonnet subagent, `model: "sonnet"`）
2. PASS 后 → **Code Quality Review**（dispatch 另一个 Sonnet subagent, `model: "sonnet"`）
3. P0 立即修复 → 重新 review
4. P1 立即修复
5. P2 能现在修则现在修（依赖未就绪可推迟，注明理由）
6. 两个 review 都 PASS → `progress.reviewed_tasks += 1`
7. 然后 dispatch 下一个 task 的 implementer

**禁止行为**：
- 全部 task 做完再统一 review（禁止）
- 某 task 太简单跳过 review（禁止）
- 为速度跳过 review（禁止）

### 5.3 NDF Rule 8：commit 验证

每个 implementer subagent 返回后，主控 AI **必须**验证：

```bash
cd numind-server && git log --oneline -1 && git status
cd numind-web-v3 && git log --oneline -1 && git status
```

- 最近 commit message 与 task 内容匹配
- working tree 干净
- 多仓库 task 对两个仓库都检查

**异常处理**：subagent 未 commit → 主控 `git diff` 审查 → 合理则自己 commit，有问题则 dispatch fix subagent。

### 5.4 Rule 6 硬规则摘要（贴在此便于 S4 dispatcher 查）

**`progress.reviewed_tasks` 必须等于 `progress.completed_tasks`**。不等 = 有 task 跳过 review，必须补做。

---

## 6. 风险与缓解

引自 spec §7 + plan 阶段的具体缓解：

| # | 风险 | 概率 | 影响 | Plan 阶段缓解措施 |
|---|---|---|---|---|
| R1 | 长内容（5000+ 字 markdown）溢出 OutputCard body | 中 | 中 | F6 task 验收加"长样本测试"；V1 task 用真实长样本截图比对 |
| R2 | 现有 E2E 因 DOM 结构变更全 fail | 100% | 中 | F13 task 专门处理 selector 迁移；不可低估工作量 |
| R3 | `viewingStep` vs `currentStep` 双指针混淆 | 高 | 中 | F1 task 必须含单元测试覆盖所有转换；附录 B 伪代码直接落地 |
| R4 | 老节点 description NULL → step header 空白 | 100% | 低 | F4 task 实现 SopStepView 时加 `v-if="node.description"` |
| R5 | ConfirmModal 对"重新生成"过度打扰 | 中 | 低 | 遵循 ui-ux.md 硬规则 4 默认弹；可作为 S5 后 follow-up 收集反馈 |
| R6 | 停止生成后 partial content 与下次执行冲突 | 中 | 中 | F9 task 明确规则：partial 只内存展示，不入 nodeRuns；下次执行清空 |
| R7 | 后端 model_name 老数据全为空字符串 | 100% | 低 | F6 MetaFooter 收到 empty string 整段不渲染，单测覆盖 |
| R8 | CSS scoped 变量污染全局 | 低 | 高 | F0 task 验收明确 grep 检查 `:root` 未被新增 |
| R9 | 左 nav 节点 > 10 溢出 | 中 | 低 | F3 task `.nav { overflow-y: auto; min-height: 0; flex: 1 }` |
| R10 | trailing chat 消息 > 100 渲染性能 | 低 | 中 | 不做虚拟滚动；F10 task 只保证基础渲染，follow-up 记录 |
| R11 | Cross-repo gate 后端未部署 F 系列即开工 | 中 | 高 | 本 plan §3 明确 gate；主控 AI dispatcher 必须先验 dev curl 再开 F6/F10/F11 |
| R12 | F11 主容器改动过大引入集成 bug | 中 | 高 | F11 前所有子组件 task（F3–F10）已 review PASS；F11 focus 只做接线 |

---

## 7. 工件引用

| 工件 | 路径 | 状态 |
|---|---|---|
| Requirement (S0) | `numind-server/requirements/sop-runtime-visual-redesign.md` | ✅ |
| Proposal (S1) | `numind-server/proposals/sop-runtime-visual-redesign-proposal.md` | ✅ |
| Backend audit | `numind-server/proposals/sop-runtime-visual-redesign-backend-audit.md` | ✅ |
| Mockup 01（A/B 态）| `numind-server/proposals/sop-runtime-visual-redesign-mockups/01-active-and-history.html` | ✅ |
| Mockup 02（C/D/E/F 态）| `numind-server/proposals/sop-runtime-visual-redesign-mockups/02-additional-states.html` | ✅ |
| Spec (S2) | `numind-server/docs/superpowers/specs/2026-04-11-sop-runtime-visual-redesign-design.md` | ✅ |
| **Plan (S3, 本文件)** | `numind-server/docs/superpowers/plans/sop-runtime-visual-redesign-plan.md` | ✅ 本次产出 |
| Verification doc (V1) | `numind-server/docs/superpowers/plans/sop-runtime-visual-redesign-verification.md` | 待 V1 task 产出 |
| Screenshots (S5) | `numind-server/proposals/sop-runtime-visual-redesign-screenshots/` | 待 S5 产出 |

---

*本 plan 是 `sop-runtime-visual-redesign` feature 的 S3 产出，基于 S2 spec §3/§4/§5/§9 和 NDF Rule 6/8/9/10 写成。S4 阶段 dispatcher 应严格按 Task 顺序执行，每个 task 双 review 不跳过，Cross-Repo Gate 通过后再开 F6/F10/F11 的验收环节。*
