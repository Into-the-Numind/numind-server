# SOP 运行页视觉 / 信息架构重设计

## 来源
- 提出人：产品负责人 / 创始人（dogfood 触发）
- 提出日期：2026-04-11
- 前置依赖：`sop-runtime-vue-rewrite`（S5 验证中，24/25 task done）
- 轨道：**Standard 精简版**

---

## 1. 背景与触发

### 1.1 用户表述
> "SOP 运行的 UI/UX 现在太丑了。"

一句话触发，但展开后是三类叠加问题。

### 1.2 前置 feature 遗留空间

刚完成 S5 验证的 `sop-runtime-vue-rewrite` 解决的是 **架构正确性** 问题：
- 7518 行 legacy vanilla JS → Vue 3 + TS 组件化
- 消除双渲染路径（`TEMPLATE_CONFIGS` 硬编码模板）
- 修复 self-service-config 配置契约（B 端配什么，C 端就展示什么）
- 删除硬编码绿色卡片、统一数据真相源到后端
- 补齐权限配额 / Langfuse trace / draft 模式 / SSE 流式 全链路

但该 feature 的 scope 显式写明 **"不做视觉重设计，保持现有视觉风格 + DESIGN.md token"**。
于是当前状态是：**代码架构干净了，视觉还停在 legacy 时期的"Tailwind 默认 SaaS"**。

### 1.3 Triage 经过

用户初期倾向"快速改一改"（Hotfix），AI 评估后挑战改判 Standard：

| 问题类别 | scope | 能否 hotfix |
|---|---|---|
| (a) 视觉打磨：间距 / 字号 / 配色 / 层次 / 动效 | 散点 | ✅ 可以 |
| (b) 设计体系对齐：DESIGN.md token + `.impeccable.md` 品牌 DNA | 系统性 | ⚠️ 边界 |
| (c) 结构 / 布局重设计：解决"进入下一步看不到上一步结果" | 10-20 个组件重组 | ❌ 超 hotfix 范围 |

三类同时存在 → **走 Standard 精简版**。理由：视觉工作需 S0/S1 探索方向，Hotfix 会变成 AI 拍脑袋。

另外 S0 阶段出现一次方法论修正：**文字采集失败** —— 用户反馈"让我看文字描述无法决策"，立即切换为 **视觉对齐模式**（subagent 生成 HTML mockup → 用户视觉选型 → 固化视觉契约）。这条经验已记录在 manifest decisions。

---

## 2. 目标

本 feature 只做 UI / UX / IA，**不动业务功能**。四个并列目标：

1. **视觉打磨**：间距 / 字号 / 配色 / 层次 / 动效全部收敛到 DESIGN.md token，消除 "Tailwind 默认"的 SaaS 味。交付标准：随机截一张页面放到首页旁边，用户能说出"是同一个产品"
2. **设计体系对齐**：与 `.impeccable.md` 品牌 DNA 一致，与已经过 frontend-design-system pivot 的首页视觉打通。全站 icon 统一 Lucide、字体统一、色板统一 token
3. **结构 / 布局重设计**：采用 **γ Focused 方案**（左 vertical step nav + 右主区单步聚焦），从结构上根除"切换下一步看不到上一步结果"的核心 UX 痛点。左 nav 支持任意步骤跳转、历史步骤 read-only 进入、状态切换有明确 context strip
4. **严格对齐后端能力**：UI 展示的每一项元信息必须后端能提供，禁止为了好看伪造数据；缺的小字段（Gap 1 model_name / Gap 2 duration_ms）在本 feature 内补齐后端，而不是前端 mock 假值

### 非目标

- 不改后端业务逻辑（执行流程 / 权限 / 配额 / SSE 协议本身 / Langfuse trace 链路）
- 不改模板编辑器（admin 侧 self-service-config）
- 不动历史 run 列表页 `SOPHistoryView`
- 不做 mobile 适配（本轮只做 1440 桌面，mobile 单独走 follow-up）

---

## 3. 范围与边界

### 3.1 包含

**numind-web-v3（主仓库）**
- `src/views/SOPRunView.vue` 及其全部子组件（stepper / content card / chat area / toolbar / footer meta）
- `src/stores/sopRun.ts`：可能需要补 state 字段承载 γ Focused 的"当前聚焦步骤"与"查看历史步骤"的区分
- `src/api/sop.ts`：补 `saveBookmark` 封装（后端 endpoint 已存在，前端缺）
- 全部 6 个状态覆盖

**numind-server（辅仓库，小改动）**
- `migrations/`：加 2 个 migration
  - `sop_node_run` 表加 `model_name VARCHAR(64)` 字段
  - `sop_chat_message` 表加 `duration_ms INT` 字段
- `biz/sop/`：写入 sop_node_run 时同步落 model_name
- `biz/sop/chat.go`（或等价位置）：chat 消息写入时记录 duration_ms

### 3.2 覆盖的 6 个状态

视觉契约 mockup 已固化全部 6 个状态，后续 spec / implementation 必须像素级对齐：

| 状态 | 代号 | 描述 | mockup 文件 |
|---|---|---|---|
| 当前任务（active）| A | 左 nav 高亮当前步骤，右侧显示标题 / 描述 / 输入区 / 执行按钮 | 01 |
| 查看历史（read-only）| B | 左 nav 点击已完成步骤，右侧只读展示输入与 AI 输出，隐藏 textarea | 01 |
| Draft 入口 | C | 从模板进入但尚未执行任何节点，第一步为 active 输入态 | 02 |
| 执行中流式 | D | 点击执行后 AI 输出流式打出，"停止生成"按钮可用 | 02 |
| 完成态 | E | 单步已执行完毕，textarea 隐藏，footer 展示元信息（模型 / 耗时 / token），操作按钮 = 重新生成 / 下一步 / ⭐ 收藏 | 02 |
| Trailing chat | F | 所有 SOP 步骤完成后，底部延伸出"继续问 AI"对话区 | 02 |

### 3.3 显式不在范围

- 后端执行引擎 / executor / SSE 事件协议
- 新增 SOP 功能（例如多模态输入、协作、新节点类型）
- 模板创建 / 编辑的 B 端界面
- 历史 run 列表页
- mobile / pad 适配

### 3.4 影响文件估算（供 S3 plan 参考）

| 仓库 | 类别 | 预估文件数 |
|---|---|---|
| numind-web-v3 | view / 子组件改写 | 10-15 |
| numind-web-v3 | store（sopRun.ts 扩状态）| 1 |
| numind-web-v3 | api 封装（sop.ts 补 saveBookmark）| 1 |
| numind-web-v3 | 新增组件（StepNav / ContextStrip / ChatArea 复用）| 3-5 |
| numind-server | migration SQL | 2 |
| numind-server | biz 写入改动 | 2-3 |
| 合计 | | **约 20-28 文件** |

---

## 4. 痛点清单（dogfood 六维）

用户在实际使用中挑出的问题，按维度归类。这是 S1 proposal 需要 one-to-one 回应的清单。

### 4.1 信息密度 / 空间利用

- 桌面 1440 严重浪费：内容卡 880px 居中，左右各 280px 空白，视觉上像 "phone frame in desktop"
- **进入下一步就看不到上一步的 AI 输出**，必须 "切回去看一眼、再切回来继续填" —— 结构性核心痛点，直接影响用户把多步 SOP 当成"流水作业"时的 working memory
- 页面多处重复信息（模板名、进度数字在 header + sidebar + stepper 三处重复出现）
- 侧边栏节点列表占用宽度但只承载"步骤名"，信息密度极低
- 一屏只能容纳 1 个主要交互（输入框或输出块），上下滚动成本高
- 长输出场景（2000+ 字）下，上一步结果与当前输入隔了超过一屏，用户必须反复滚动定位

### 4.2 视觉层次错位

- **输入区是用户主任务**，但当前被装饰性 hero（大图 + 渐变）抢戏，视线先被吸到"好看但无用"的区域
- 元信息（耗时 / 模型 / token）放在内容卡顶部抢 primary 位置，应降到 footer secondary
- "继续问 AI" trailing chat 被放在 stepper 列表里和 SOP 节点平起平坐，**IA 错位**：它不是 SOP 节点，是后置对话区，用户看到会误以为"还有一个步骤没做"
- 节点标题字号偏小贴边，和描述文字层次拉不开，要盯很久才找到当前在做第几步

### 4.3 视觉品牌缺失

- 完全是 "Tailwind 默认 SaaS"：白底 + 细灰线 + `blue-500` 按钮 + 系统字体
- 没有 `.impeccable.md` 要求的品牌温度（材质感 / 克制的意外 / 叙事节奏）
- **与首页视觉断层**：首页已过 frontend-design-system pivot 有自己的语言，SOP 运行页还停在旧世界
- 返回按钮用 unicode `←`，不是 Lucide icon（和全站 icon 体系不一致）

### 4.4 流式 / loading 状态弱

- "AI 正在分析中..."：一根细绿色竖线 + 灰色斜体小字，**完全不像现代 LLM 应用**
- 没有 skeleton、没有 pulse、没有 thinking animation
- 执行前后过渡硬切，缺 micro-interaction

### 4.5 trailing chat 设计简陋

- chat UI 明显比项目的销售助手（`ChatArea` + `ChatMessage` 完整组件体系）简陋一档
- 顶部"继续问 AI"的标题 + 描述占用空间，却与左 nav 里的条目**语义重复**
- 用户气泡 vs AI 气泡**视觉不对称**，AI markdown 渲染的 h1 会溢出卡片边界
- 没有耗时 / 模型显示（后端也缺 duration_ms → Gap 2 要补）

### 4.6 完成态混乱

- 节点执行完后 textarea 依然占大块空间，但此时用户已无需编辑，只是干扰
- 底部三个按钮 "复制 / 重新生成 / 下一步" 散落并排、缺 visual hierarchy，用户不知道主操作是哪个
- 无 ⭐ 收藏入口（后端已有 bookmark endpoints，前端 UI 完全缺失）
- 元信息没地方放，只能挤在按钮旁边

---

## 5. 用户筛选的核心问题（按优先级）

用户 dogfood 阶段明确指认的 **"必须解决"** 清单（S1 proposal 必须给方案）：

1. 节点标题字号太小 / 贴边 → **层级拉起来**
2. 输入区应是视觉主体 → **去 hero 装饰，把输入区拉为 primary focus**
3. 底部三按钮缺 hierarchy → **primary / secondary / tertiary 分层**
4. 装饰过度 → **去衬线字体 / 去灰底 / 去渐变 hero**
5. 品牌温度缺失 → **对齐首页设计语言**
6. 非 Lucide 图标 → **全部替换为 Lucide**
7. **结构性**：切换步骤时看不到上一步结果 → **γ Focused 布局 + 可单步聚焦 + 左 nav 任意跳转**

---

## 6. 关键约束（用户过程中明确给出）

这些约束是 S1 / S2 不可违反的硬边界：

### 6.1 单步展示内容（白名单）

每个 SOP 步骤**只能展示**以下元素：
- 标题
- 描述（可能为 null，需 graceful fallback）
- 输入区（仅 active 状态显示，历史步骤隐藏）
- AI 输出
- 可选元信息（完成后的 footer：模型 / 耗时 / token）

### 6.2 明令禁止

- ❌ **不展示 prompt**：无论是 system prompt 还是用户填入的 prompt 文本，一律不暴露。prompt 是模板作者（B 端客户）的知识资产，泄露 = 商业敏感问题。这包括：不显示在 UI 上、不放在 tooltip、不出现在"查看详情"弹窗
- ❌ **执行后不可修改输入**：节点一旦 execute 成功，textarea 必须隐藏。用户只能选 "重新生成"（用同一份输入重跑）或 "下一步"。理由：一旦允许"改一改再看结果"，会引发 "是不是上一版更好" 的反复横跳，消耗配额也污染数据
- ❌ **重新生成 ≠ 修改输入**：重新生成是抹除旧 output、用同样 input 重跑一遍，不是打开 textarea。后端 execute endpoint 是覆盖式写入 sop_node_run.output，这个语义必须在 UI 上清晰传达
- ❌ **绝不使用 localStorage**：所有状态 / 数据走后端持久化（draft run 机制已经 lazy 创建 run + 持久化所有节点输入 + 上传文件）。localStorage 会导致跨设备不一致 + 清缓存丢数据 + 增量迁移困难
- ❌ **不伪造数据**：UI 不能展示后端没有的字段。缺字段要么补后端（Gap 1, 2），要么不展示。不允许"先放个占位数据之后再接"

### 6.3 内容承载

- ⚠️ **单步输出长度不可预测**：LLM 输出可能非常长（2000-5000 字，极端情况更长），布局必须天然承载长内容而非 overflow hidden
- ⚠️ **markdown 渲染**：AI 输出含 h1-h6 / list / code block / table，不能溢出卡片边界

### 6.4 设计语言收敛

- ✅ 去衬线字体（之前 mockup 尝试过杂志感，用户反馈"太重"）
- ✅ 去灰底（全白页面为主，分隔通过间距 + 细线而非背景块）
- ✅ 去重复信息（header / sidebar / stepper 中重复的模板名 / 进度只保留一处）
- ✅ 去装饰（hero 图 / 渐变背景 / 多余 icon 全部砍掉）

### 6.5 后端先行

UI 不能依赖后端没有的数据或操作。S1 前已完成后端能力摸底（见 §8）：

- Gap 1（sop_node_run 缺 model_name）→ **本 feature 内补**
- Gap 2（sop_chat_message 缺 duration_ms）→ **本 feature 内补**
- Gap 3（流式中 token 进度）→ **放弃**，mockup 没设计
- Gap 4-bis（保存书签 UI）→ 后端完整，前端补 `saveBookmark` API 封装 + ⭐ toggle
- Gap 4-ter（createRun 自动应用书签）→ 传 `auto_apply_bookmarks=true` 参数即可
- Gap 5（停止生成）→ 纯前端 `EventSource.close()`，后端不改

---

## 7. 视觉契约

视觉契约已固化在两个 mockup HTML，后续 S1 proposal / S2 spec / S4 implementation 必须**像素级对齐**这两个文件：

- `numind-server/proposals/sop-runtime-visual-redesign-mockups/01-active-and-history.html`
  - 状态 A：当前任务（active 输入态）
  - 状态 B：查看历史（read-only）
- `numind-server/proposals/sop-runtime-visual-redesign-mockups/02-additional-states.html`
  - 状态 C：draft 入口
  - 状态 D：执行中流式
  - 状态 E：完成态
  - 状态 F：trailing chat

**布局方案**：**γ Focused** —— 左 vertical step nav + 右主区单步聚焦。选型理由（manifest Round 2 决策）：

- 单步占用整个 viewport → 天然承载长内容
- N 步骤扩展性好（左 nav 可滚动）
- 左 nav 一键切换任意步骤 → 根除"切回去再切回来"痛点
- read-only 历史步骤无 textarea，状态切换有明确 context strip 提示

### 7.1 γ Focused 布局骨架（从 mockup 提炼）

```
┌─ topbar (模板名 + 返回 + 用户菜单) ─────────────────────────┐
├────────────┬──────────────────────────────────────────────┤
│ 左 nav     │  主区                                        │
│ (固定 ~280)│                                              │
│            │  步骤标题（大号 display）                     │
│ 1 ● 已完成 │  步骤描述（可为 null → 隐藏不占位）             │
│ 2 ● 已完成 │  ─────────────────────                       │
│ 3 ● 进行中 │  输入区 / AI 输出（长内容自由撑开）            │
│ 4 ○ 未开始 │                                              │
│ 5 ○ 未开始 │                                              │
│            │  ─────────────────────                       │
│ ⎯⎯⎯⎯⎯⎯⎯⎯⎯ │  footer 元信息 + 操作按钮                     │
│ chat (F)  │                                              │
└────────────┴──────────────────────────────────────────────┘
```

### 7.2 关键视觉原则

- **单真相源每处只写一次**：模板名只在 topbar、进度只在左 nav、元信息只在 footer
- **context strip**：当用户从 active 步骤点击一个已完成步骤时，右主区顶部出现一条明显的 "查看历史" strip（带 "返回当前步骤" CTA），防止用户迷路
- **完成态 textarea 折叠**：执行成功后 textarea 物理消失，不是 disabled 灰掉
- **执行按钮 primary**：active 输入态时"执行"是页面最 prominent 的 CTA；完成态 primary 切换为 "下一步"
- **动效克制**：transition 只用于状态切换（active ↔ history / loading ↔ done），不加装饰性动画

---

## 8. 参考材料

| 类别 | 路径 |
|---|---|
| 后端能力 audit | `numind-server/proposals/sop-runtime-visual-redesign-backend-audit.md` |
| 视觉契约 mockup | `numind-server/proposals/sop-runtime-visual-redesign-mockups/01-active-and-history.html` |
| 视觉契约 mockup | `numind-server/proposals/sop-runtime-visual-redesign-mockups/02-additional-states.html` |
| 设计 token / 字体 / 间距 | 根目录 `DESIGN.md` |
| 品牌叙事 / 美学方向 | 根目录 `.impeccable.md` |
| 前置 feature 架构 spec | `numind-server/docs/superpowers/specs/2026-04-11-sop-runtime-vue-rewrite-design.md` |
| 前置 feature requirement | `numind-server/requirements/sop-runtime-vue-rewrite.md` |
| 旧版截图（dogfood S0，临时文件）| `/tmp/sop-draft-1440.png` 等（不在仓库） |

---

## 备注

- 本 feature 与 `sop-runtime-vue-rewrite` 的关系：架构重写 + 视觉重设计是**两段式交付**。第一段保证代码干净、数据真实、功能等价；第二段把"干净的骨架"打扮成"有品牌温度的成品"。合在一起才是 SOP 运行页的完整现代化。
- Triage 默认假设（首轮未逐条确认）：trailing chat 区域**包含在本 feature**；无外部具体设计参考产品，S1 使用 `/design-shotgun` 探索方向已完成（产物即上述 mockup）。
- S0 阶段方法论教训：**纯文字需求采集对视觉决策无效**。任何涉及"好看不好看 / 布局对不对"的工作，S0 阶段必须由 subagent 出 mockup，用户在 mockup 上圈改，而不是给用户读文字描述让他脑补。此经验已沉淀到 manifest decisions，后续视觉类 feature 复用。
- Triage 历程可追溯：user "快速改" → AI 挑战（指出三类叠加）→ AI 推荐 Standard 精简版 → 用户接受 → S0 文字采集失败 → 切视觉对齐 → Round 1 / Round 2 / Round 3 逐轮收敛到 γ Focused。此流程可作为未来"偏视觉重设计类"feature 的参考路径。
- **回归保护诚实声明**：本 feature 虽然功能不动，但涉及组件结构重写，前置 feature 的 Playwright E2E 可能因为 DOM 结构变更而失效。S3 plan 的"S5 验证策略"task 必须评估：是选择更新现有 E2E，还是重写一套新 E2E 针对 γ Focused 布局。两者都不做 = 自动回归保护消失，违反 Rule 10 的诚实声明要求。
- 与 `llm-model-switch` 的 UI 协调：如果后者已交付"模型选择下拉框"，本 feature 的 active 输入区 footer 需要留出位置承载该控件（位置参考 mockup 中 "执行" 按钮左侧）。如未交付，本 feature 不主动引入，避免 scope creep。
