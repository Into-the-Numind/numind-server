# SOP 运行页视觉 + 信息架构重设计 — 提案

> Feature: `sop-runtime-visual-redesign`
> Track: Standard（精简版）
> 阶段：S1（回填）
> 前置依赖：`sop-runtime-vue-rewrite`（S5 已完成 24/25 task，架构正确性 + 数据真相源已落地）
> 最后更新：2026-04-11

---

## §1 背景 [AI 内部]

### 1.1 前置 feature 交付了什么，没交付什么

`sop-runtime-vue-rewrite` 用了 3 个 sprint 把 SOP 运行页从原型代码重写为工程化 Vue 3 代码，交付：

- ✅ SSE 流式执行正确性（event 幂等、心跳忽略、JSON-encoded message 解析）
- ✅ Draft run lazy 创建、节点输入/文件持久化、断线刷新不丢数据
- ✅ 配额检查 + 权限门 + 业务错误码处理（403 + business code）
- ✅ Trailing chat 上下文自动注入（前面所有步骤 output 作为 system message）
- ✅ Pinia store 拆分、composable 抽取、TypeScript 类型完备

但没交付：

- ❌ **视觉打磨**：间距/字号/字体/配色/层次/动效散落在多个组件里，没对齐 `DESIGN.md` token 体系
- ❌ **信息架构**：当前是垂直长列表（所有步骤串行排下来），长内容场景下用户迷失，没有 context anchor
- ❌ **状态态清晰度**：active / history / draft / streaming / done / trailing-chat 6 个状态没有明确的视觉差异，用户分不清"能不能改"
- ❌ **元信息呈现**：耗时、模型、token 用量数据在后端都有，但前端从未展示过

### 1.2 本 feature 的定位

**纯视觉 + IA 重设计**。不改后端逻辑、不改执行时序、不改权限与配额。只重塑"看到的东西"和"单屏信息组织方式"。

跨 2 仓库但重量级不对称：

| 仓库 | 改动量 | 说明 |
|---|---|---|
| `numind-web-v3` | 重 | SOPRunView.vue + ~15 个子组件重写，新组件体系 |
| `numind-server` | 轻 | 2 个 migration + `biz/sop/sop.go` 几行写入改动（见 §6） |

### 1.3 不做什么（Out of Scope）

为避免 scope creep，明确以下事项**不在本 feature 内**：

- ❌ 执行引擎改动（SSE 协议、event schema、幂等逻辑等全部冻结）
- ❌ 权限/配额逻辑（free/trial/standard/premium 的门槛判断不动）
- ❌ 模板管理、模板编辑、节点编辑（只是"运行页"，不是"管理页"）
- ❌ 管理端（numind-admin-web）任何改动
- ❌ Mobile 响应式适配（见 §7 Q1，走 follow-up）
- ❌ 国际化、a11y 专项优化（常规 WCAG AA 要求保留，但不做专项）
- ❌ LLM 模型切换 UI（是另一个独立 feature `llm-model-switch`）

---

## §2 探索过程 [AI 内部]

> 这一节是 proposal 的核心。S0 + S1 走了很多弯路，必须结构化记录，避免下次重设计时踩同样坑。

### 2.1 Round 0：文字探索失败（S0 第一次尝试）

最初 AI 按标准 NDF S0 流程向用户列了 28 条潜在 pain points 让逐条选择（是/否/优先级）。用户明确反馈：

> "我觉得你这样用文字让我选的形式，我根本没法做决策。"

**教训**：视觉类需求不能用文字让用户做决策。用户看不到东西时无法建立"这个选项意味着什么"的心智模型。**切换到视觉对齐模式**：dispatch subagent 生成真 HTML mockup，用户直接看图选型。

### 2.2 Round 1：3 个结构方案探索（已废弃，但决策路径必须记录）

Dispatch design subagent 产出 3 个结构方案 HTML：

| 方案 | 结构 | 长内容处理 |
|---|---|---|
| A | Wizard + 折叠式上一步摘要 | 历史步骤折叠成摘要行 |
| B | 垂直时间线累积 | 所有步骤 inline 展开，页面不断向下生长 |
| C | 左 nav + 主区上下文累积 | 左导航 + 主区累积显示多步 |

**用户初看后给出两条关键修正，这两条成为所有后续方案的硬约束**：

- **修正 1 — 不展示 prompt**：每步只展示 `标题 / 描述 / 输入区（仅 active 时）/ AI 输出 / 可选元信息`。系统 prompt 是商业机密；用户输入也不需要在步骤卡上重复展示。
- **修正 2 — 长内容是核心约束**：单步 AI 输出长度不可预测，可能非常长（几千字 markdown）。方案必须科学处理长内容，不能让短内容和长内容共用同一种布局。

Round 1 的 3 个方案都不满足修正 2（B 垂直累积最糟 — 一步长内容就把后续全推到页面底部）。

### 2.3 Round 2：3 个新结构方案（应用修正 1+2）

| 代号 | 结构 | 长内容策略 |
|---|---|---|
| α Stream | 单流时间线 + 长内容"收起/展开" | toggle 控制每步高度 |
| β Pinned | 当前固定 + 历史折叠卡 + drawer 查看 | 当前步骤固定在视口，历史全折叠 |
| γ Focused | 左 vertical step nav + 右主区单步聚焦 | 单步占满整个主区 viewport |

**用户选定 γ**。决策理由（记录供未来参考）：

1. **长内容承载**：单步占满整个 viewport，长 markdown 天然有足够空间
2. **N 步骤扩展性**：左 nav 一键切任意步，步数从 3 到 10 都不会拥挤
3. **成熟模式类比**：Linear issue view / Notion database page / IDE 文件树 + 编辑区 — 用户心智模型已存在
4. **状态清晰**：单屏聚焦一步后，"这一步能不能改"的 affordance 可以用整个主区的布局变化来表达，而不是挤在一个小卡片里

### 2.4 Round 3：γ 微调（多轮修复）

γ 方向选定后又走了 4 次微调 round，按时间顺序：

#### Round 3.1 — 去重 + 去装饰

用户一次性指出 6 个问题：

| 问题 | 修复 |
|---|---|
| 顶部 header 的"进度 3/5"数字和左 nav 重复 | 删 top header 进度数字 |
| RUN-ID #1042 用户不关心 | 删 RUN-ID |
| 左 nav 上方的模板名和 top header 重复 | 删 left nav 模板名 header |
| italic eyebrow "Step 02 · Draft" 装饰感强 | 删 eyebrow |
| pull-quote bar（大号引用块）太像 blog 设计 | 删 pull-quote bar |
| 全局衬线字体太刻意杂志风 | 衬线只保留在 markdown body |

输出 `sop-mockup-gamma-v2.html`。

#### Round 3.2 — 全 sans 字体

用户："把所有的衬线字体改成系统默认。"

修复：所有 `var(--font-serif)` → `var(--font-sans)`，包括 markdown body。教训 — "衬线 body 看起来像文学作品"这个 AI 本能在 B 端工具场景是错的，B 端就是要系统字体。

#### Round 3.3 — 背景统一

用户："背景颜色的使用不是很好，看起来不是很顺。"

问题：mockup 用了 5+ 种灰底区分区域（page / card / sub-card / input / output / footer），每种灰阶都不同，堆在一起像不调和的色卡。

修复：简化为 **2 种底色** — `#F6F7F9`（page bg）+ `#FFFFFF`（surface card），靠 1px `#E5E7EB` border 分区，不靠灰度分区。

#### Round 3.4 — 全白

用户："能不能不要有灰色的底呀？我觉得这个灰色的底纯粹是没什么用。"

修复：page bg 也改为 `#FFFFFF`。整页全白，区块完全靠 border + 间距组织。

**教训**：极简美学胜于"用灰阶表现层次"。纯白 + border + 间距是更成熟的 B 端审美（Linear / Stripe dashboard 都是这路线）。

### 2.5 Round 4：补缺失状态

Round 3 的 v2 mockup 只覆盖 A（active 默认查看）和 B（查看历史）。补齐剩余 4 个状态：

- C — Draft 入口（首次进入页面，step 1 active 但未执行）
- D — 执行中流式（SSE 流 token 逐个渲染）
- E — 完成态（当前步骤刚执行完，元信息 footer 出现）
- F — Trailing chat（追问区域）

输出 `02-additional-states.html`。

### 2.6 Round 5：后端语义对齐

用户抛出额外硬约束，本质是"前端 affordance 必须反映后端真相"：

| 约束 | 影响 |
|---|---|
| "执行后不能再修改" | State D/E 必须删除可编辑 textarea（后端 sop_node_run 不支持同 run 内 user_input 改写再跑，只支持覆盖式重跑） |
| "trailing chat 主区不要标题" | 左 nav 已显示"追问"，主区不再需要 step-header，chat 铺满整个主区 |
| "AI 将在这里生成结果"占位文案多余 | 删 placeholder |

### 2.7 Round 6：后端能力摸底 + bookmark 纠正

用户："要根据后端来修改前端，有些前端的功能，如果你都不能从后端进行获取的话，那纯粹就是白搭。"

Dispatch Explore agent 对照 mockup 6 个状态逐元素核对后端，read-only 模式，输出 `sop-runtime-visual-redesign-backend-audit.md`。发现 5 个 gap，全部列出，用户逐个决策。

**Bookmark 纠正**（这一条是 Round 6 的关键收获）：

- AI 最初把"保存草稿"按钮写进了 mockup。用户纠正：draft run 机制已经隐式保存了所有状态（lazy run + 节点输入持久化 + 文件持久化），"保存草稿"按钮是重复的。
- 用户实际要的是 **bookmark（保存书签）**：把某一步的 user_input（可能包含粘贴的长文本、上传的文件）保存为模板级书签，下次运行同一模板时可以一键回填。
- 后端 bookmark endpoints 完整（save / list / apply），只是前端 `src/api/sop.ts` 没封装。
- 决定：output card head 右上角加 ⭐ toggle（**不是**单独的"保存书签"按钮）；createRun 时自动传 `auto_apply_bookmarks=true`，无需额外 UI。

---

## §3 选定方案：γ Focused

### 3.1 整体结构

```
┌───────────────────────────────────────────────────────────────┐
│ Slim header: [← 返回] [模板名]               [历史] 3 elements │ 56px
├─────────────┬─────────────────────────────────────────────────┤
│             │                                                 │
│ Step nav    │  Main area (单步聚焦)                           │
│ 260px       │                                                 │
│             │  ┌─────────────────────────────────────────┐   │
│ Main steps  │  │ Step title                              │   │
│ ● 1 完成    │  │ Step description                        │   │
│ ● 2 完成    │  ├─────────────────────────────────────────┤   │
│ ▶ 3 进行中  │  │ Input area (仅 active 时显示)           │   │
│ ○ 4         │  ├─────────────────────────────────────────┤   │
│ ○ 5         │  │ Output area   [⭐] [重新生成] [下一步]  │   │
│             │  │                                          │   │
│ ────────    │  │ AI 生成内容 (markdown, 占满可用空间)    │   │
│ 追问        │  │                                          │   │
│ ● 追问      │  ├─────────────────────────────────────────┤   │
│             │  │ 元信息 footer: 14:42 · glm-4-7 · 3.2s  │   │
│             │  └─────────────────────────────────────────┘   │
│             │                                                 │
└─────────────┴─────────────────────────────────────────────────┘
```

### 3.2 6 个状态规约

#### 状态 A — 当前任务 active（默认查看当前可执行步骤）

- **左 nav**：步骤标记 `▶`，文字高亮；前面已完成的步骤标 `●`，后面未开始的标 `○`
- **主区**：显示 title + description + **可编辑** input 区（textarea / file upload / 组合）+ 空的 output 占位（无内容但 card 已出现）
- **按钮**：主 CTA `开始执行`（primary）；次 CTA 无；`⭐ 收藏` 隐藏（没 input 内容时不显示）
- **元信息 footer**：不显示
- **Affordance 要点**：input 区有清晰 border + focus ring；textarea auto-height；底部 "Ctrl+Enter 执行" 提示

#### 状态 B — 查看历史 read-only（点左 nav 回看已完成步骤）

- **左 nav**：被点中的历史步骤前方出现一条 accent 竖线，文字稍变色
- **主区**：显示 title + description + **只读** input 区（文字保留但不可编辑，无 border 无 focus）+ 完整 output 区
- **按钮**：`重新生成`（secondary，允许覆盖式重跑，后端会更新现有 sop_node_run）+ `⭐ 收藏` toggle（已收藏高亮）；**无** "开始执行"
- **元信息 footer**：显示 `完成时间 · 模型 · 耗时 · token 用量`
- **Affordance 要点**：顶部有一条浅色 info strip `"查看历史步骤 · 输入不可修改"`，明确 read-only 语义

#### 状态 C — Draft 入口（用户首次进入页面，新建 run）

- **左 nav**：所有步骤为 `○`，step 1 标 `▶`（active）
- **主区**：显示 step 1 的 title + description + 空的可编辑 input 区 + **无** output card（还没执行过）
- **按钮**：主 CTA `开始执行`
- **元信息 footer**：不显示
- **行为**：用户改动 input / 上传文件时，前端调用 `POST /v1/sop/runs/draft` lazy 创建 draft run，并传 `auto_apply_bookmarks=true` — 后端自动应用该 (user, template, node) 下的所有 bookmark
- **Affordance 要点**：与状态 A 几乎相同，区别只是 "首次进入" 语义（前端自己判断，UI 无额外差异）

#### 状态 D — 执行中流式

- **左 nav**：当前步骤 `▶` 变为脉动 loading 状态（CSS 动画）
- **主区**：title + description + **隐藏** input 区（执行后不可修改，见 Round 5）+ output card 出现，内容流式追加
- **按钮**：`停止生成`（secondary，前端 `EventSource.close()`，后端继续跑完但前端丢弃）；**无** "开始执行" / "重新生成" / "下一步"
- **元信息 footer**：不显示（要等 done 后才填入完整数据）
- **Affordance 要点**：output card 右上角有小 loading indicator；markdown 渲染随 token 流式更新；自动 scroll 到底部

#### 状态 E — 完成态（当前步骤刚执行完）

- **左 nav**：当前步骤变 `●`（完成），自动高亮下一步为 `▶`（但主区还在当前步骤）
- **主区**：title + description + **隐藏** input + 完整 output 区
- **按钮**：`⭐ 收藏` + `重新生成`（secondary）+ `下一步`（primary）
- **元信息 footer**：显示 `完成时间 · 模型 · 耗时 · token 用量`
- **Affordance 要点**：`下一步` 是主 CTA，引导用户继续；`重新生成` 用"同样的 input 重跑"语义，不弹对话框（但会覆盖当前 output）

#### 状态 F — Trailing chat（追问区域）

- **左 nav**：点到最下方的"追问"节点，该节点高亮
- **主区**：**没有** step title / description / step-header（Round 5 修正），chat 占满整个主区
  - 顶部（chat 本身）：消息流，user 消息右对齐，assistant 消息左对齐
  - 每条 assistant 消息下方紧贴一行元信息 `14:42:11 · glm-4-7 · 3.2s · 2.3K tokens`
  - 底部（sticky 输入栏）：textarea + 发送按钮
- **按钮**：`发送`（primary）；每条 assistant 消息 hover 时显示 `重新生成`（语义：删掉最后一条 assistant 消息，重跑 chat/stream）
- **元信息 footer**：不存在（元信息已贴在每条消息下）
- **Affordance 要点**：chat 无附件上传（后端未支持，mockup 也未要求）；Enter 发送、Shift+Enter 换行

---

## §4 关键决策 [AI 内部]

> 按探索时间顺序编号，每条记录 **什么 / 为什么 / 影响**。

### D1 — 选定 γ Focused 而不是 α Stream / β Pinned
- **什么**：左 vertical step nav + 右主区单步聚焦
- **为什么**：α 的"收起/展开" 在长内容场景下用户要反复 toggle；β 的"当前固定 + 历史 drawer" 查看历史的成本太高。γ 单屏聚焦 + 左 nav 一键切换是最平衡的方案
- **影响**：整个页面从原"垂直滚动列表"变成"三栏固定 + 主区切换"，~15 个子组件需要重构

### D2 — 全白页面（去灰底）
- **什么**：page bg / surface card / input bg 全部 `#FFFFFF`，靠 1px `#E5E7EB` border 分区
- **为什么**：用户 Round 3.3/3.4 反馈 "灰色底纯粹没什么用"；对齐 Linear / Stripe dashboard 的纯白审美
- **影响**：`DESIGN.md` 的 surface token 在本页面只用白色；所有 skeleton loader 也从灰底改为白底 + 描边

### D3 — 全 sans 字体（去衬线）
- **什么**：`--font-serif` 在本页面不使用，markdown body 也用系统 sans
- **为什么**：B 端工具场景，衬线装饰感过强；用户明确要求
- **影响**：重置 markdown 渲染器的字体变量，可能需要重写 prose class

### D4 — 不展示 prompt
- **什么**：步骤卡内不展示 system prompt 也不展示用户输入的完整文本（仅在 input 区显示可编辑的 textarea，执行后隐藏）
- **为什么**：system prompt 是商业机密；用户输入在执行后属于"已提交的事实"，不需要在页面上重复展示占地方
- **影响**：状态 B 的 read-only input 区设计需要特别处理 — 显示文字但无 border、无 label；或者完全收起，只给一个 "查看输入"折叠按钮（S2 定稿）

### D5 — 执行后 textarea 隐藏（不可修改语义）
- **什么**：状态 D/E 不再显示可编辑 input 区
- **为什么**：后端 sop_node_run 不支持"保留 run + 改 input + 不重跑"的中间态；UI 必须反映这个事实，否则用户会以为改了就算数
- **影响**：状态 B 需要一种方式让用户能回看当时的 input（否则调试时找不到）— S2 定稿具体形式

### D6 — 重新生成允许但语义明确
- **什么**：`重新生成`按钮在状态 B/E 可见，点击后用当前 input 重跑，后端覆盖式更新现有 sop_node_run
- **为什么**：真实需求（结果不满意要重来），但不能让用户误以为是"改 input 再跑"
- **影响**：按钮文案用"重新生成"而非"重新运行"，避免歧义；不弹确认对话框（覆盖本身是用户明确意图）

### D7 — trailing chat 主区无 step-header
- **什么**：状态 F 主区不显示"追问"标题和描述，chat 直接占满
- **为什么**：左 nav 已有"追问"节点高亮，重复 header 是信息冗余；chat 区需要最大垂直空间
- **影响**：ChatArea 组件需要支持"headless"模式（S2 决定是新建还是复用 sales ChatArea）

### D8 — ⭐ 收藏放在 output card head 右侧
- **什么**：bookmark toggle 是 output card head 右上角的小图标按钮（toggle 形态，已收藏填充、未收藏描边）
- **为什么**：候选方案有 (1) 独立的"保存书签"大按钮 (2) output card head toggle (3) 输入区旁边。选 (2) 因为 bookmark 绑定的是 (user, template, node) 而不是单次 run，放在 output head 语义最接近"这步的设置"
- **影响**：需要前端 sop.ts 补 saveBookmark / removeBookmark 封装

### D9 — createRun 时 auto_apply_bookmarks=true
- **什么**：前端调用 `POST /v1/sop/runs/draft` 时固定传 `auto_apply_bookmarks=true`
- **为什么**：后端 bookmark 已支持自动应用，候选方案有 (A) 用户手动"从书签加载" (B) 打开页面时自动应用 (C) 创建 run 时自动应用。选 (C) 最符合"书签 = 模板级默认值"的直觉
- **影响**：无额外 UI，只是一个参数；需要文档化

### D10 — 后端补 sop_node_run.model_name + sop_chat_message.duration_ms
- **什么**：两个 migration + biz 写入时同步落字段
- **为什么**：元信息 footer 是 mockup 状态 E/F 的关键呈现元素；当前数据可以从 join sop_node 拿但性能差且每次查 model_id 反解，不值得
- **影响**：manifest.repos 从 `[numind-web-v3]` 扩展为 `[numind-web-v3, numind-server]`；~20 行 Go 改动 + 2 个 migration SQL

### D11 — 停止生成 = 前端 EventSource.close()
- **什么**：状态 D 的"停止生成"按钮仅在前端 close SSE 连接，后端流继续跑完
- **为什么**：后端没有 abort 端点，实现真正的 server-side abort 需要改动 SSE handler + executor + 可能的 goroutine cancel，工作量大；前端 close 足够满足"用户视觉上停止"的需求
- **影响**：后端继续消耗 token（成本小），计费仍按完整执行记录；可作为后续 follow-up 优化

### D12 — "保存草稿"按钮删除（误判纠正）
- **什么**：Round 6 之前的 mockup 里的"保存草稿"按钮删除
- **为什么**：draft run 机制已经隐式保存了所有状态（lazy run + 节点输入持久化 + 文件持久化），"保存草稿"是语义重复。用户真正要的是 bookmark（D8）
- **影响**：节省一个按钮位；避免"保存了什么"的用户心智困惑

---

## §5 视觉契约

实施时必须 **像素级对齐** 以下 2 个 mockup HTML 文件：

1. `/Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-server/proposals/sop-runtime-visual-redesign-mockups/01-active-and-history.html`
   - 覆盖状态 A（active 默认查看）+ 状态 B（查看历史）
   - 定义 base 结构、typography scale、spacing、button style、left nav pattern

2. `/Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-server/proposals/sop-runtime-visual-redesign-mockups/02-additional-states.html`
   - 覆盖状态 C（draft 入口）/ D（执行中）/ E（完成态）/ F（trailing chat）
   - 定义状态间视觉差异、按钮 affordance 变化

S2 spec 阶段需要把 mockup 里的 design token（颜色 / 字号 / 间距 / 圆角 / border 色）提取为 `DESIGN.md` token 清单，以便 S4 实施时能用真正的 CSS var 而不是 mockup 里的 hard-code。

### 5.1 关键 token（从 mockup 提取，S2 需正式归档到 DESIGN.md）

| Token 类别 | 值 | 用途 |
|---|---|---|
| `--color-bg-page` | `#FFFFFF` | 页面背景（全白） |
| `--color-bg-surface` | `#FFFFFF` | 卡片背景（同 page，靠 border 分区） |
| `--color-border-subtle` | `#E5E7EB` | 所有区块分隔 1px border |
| `--color-border-strong` | `#D1D5DB` | focus ring、active 状态 |
| `--color-text-primary` | `#111827` | 主文字 |
| `--color-text-secondary` | `#6B7280` | 描述、元信息 footer |
| `--color-accent` | TBD（S2 定） | 左 nav accent 竖线、primary button |
| `--font-sans` | 系统默认 sans 栈 | **所有** 文字（含 markdown body，见 D3） |
| `--space-*` | 4 / 8 / 12 / 16 / 24 / 32 / 48 | 间距阶梯 |
| `--radius-card` | 8px | 卡片圆角 |
| `--radius-button` | 6px | 按钮圆角 |

### 5.2 实施注意

- **Mockup 的 hard-coded 颜色必须替换为 token**。S4 实施时 reviewer 要检查 CSS 里不出现 `#FFFFFF` / `#E5E7EB` 等字面量
- **间距必须走 `--space-*` 阶梯**，不允许出现 `padding: 14px` 这种非阶梯值
- Markdown 渲染样式在本页面有独立 scope，不能污染其他页面的 prose 类

---

## §6 后端 Gap 与处理方案

详见 `sop-runtime-visual-redesign-backend-audit.md`。总结：

| # | Gap | 处理 | 工作量 |
|---|---|---|---|
| 1 | `sop_node_run` 无 `model_name` 字段 | 本 feature 内补：加列 migration + biz/sop/sop.go 写入时同步落 | 1 migration + 1 行 Go |
| 2 | `sop_chat_message` 无 `duration_ms` 字段 | 本 feature 内补：加列 migration + chat biz 写入时记录 | 1 migration + 几行 Go |
| 3 | SSE 无中间 token 进度 | **放弃** — mockup 未设计 | 0 |
| 4 | "保存草稿"按钮 | **删除** — 误判，draft 机制已覆盖（见 D12） | 0 |
| 4-bis | bookmark UI + API 封装 | 前端补 `sop.ts` 的 saveBookmark / removeBookmark / listBookmarks 封装 + output head toggle UI（后端完整） | 纯前端 |
| 4-ter | 自动应用书签 | 前端 createRun 时传 `auto_apply_bookmarks=true`（后端完整） | 1 行参数 |
| 5 | 停止生成 | **纯前端** `EventSource.close()`（见 D11） | 纯前端 |

**后端合计改动**：

- `migrations/YYYYMMDD_HHMMSS_add_sop_node_run_model_name.sql`
- `migrations/YYYYMMDD_HHMMSS_add_sop_chat_message_duration_ms.sql`
- `internal/numind/biz/sop/sop.go` — 节点执行完落 `model_name`
- `internal/numind/biz/sop/chat.go`（或相应文件）— chat 完成后落 `duration_ms`
- GORM model 同步更新：`internal/pkg/model/sop_node_run.go` + `sop_chat_message.go`

---

## §7 风险与开放问题 [AI 内部]

### 风险

| # | 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|---|
| R1 | 长内容真塞满 viewport 的滚动体验未实测 | 高 | 中 | mockup 用中等长度文案；S5 必须用真实长 markdown 样本（5000+ 字）在 Playwright 截图测 |
| R2 | bookmark UI 在 sopApi 没封装，S4 要补 | 高 | 低 | 工作量小，S3 plan 单列一个 task |
| R3 | 老节点 `description` 为 NULL（migration 未 backfill） | 100% | 低 | 前端 graceful fallback — description 为 null 时不渲染描述行 |
| R4 | state B 的 read-only input 呈现未定稿（D5 影响） | 中 | 中 | S2 spec 必须给出明确方案（折叠按钮 vs 无 border 纯文字） |
| R5 | bookmark 绑定 `(user, template, node)` 粒度可能与用户心智不符 | 低 | 低 | 后续观察用户实际使用；本 feature 不动后端语义 |

### 开放问题（S2/S3 决定）

- **Q1**：mobile 适配 — γ 结构在窄屏下如何降级？本 feature **不做 mobile 适配**（走 follow-up）；桌面最小宽度约定 1280px，S5 验证只测桌面
- **Q2**：trailing chat 是否复用 sales 模块的 `ChatArea` 组件？
  - 复用：省代码，但 sales ChatArea 有附件上传等多余能力，需要拆 "headless" 变体
  - 新建：干净但重复 ~200 行代码
  - S2 spec 决定
- **Q3**：状态 B 的 read-only input 呈现形式（见 R4）
- **Q4**：左 nav 在步骤数 > 10 时是否需要 scroll？mockup 按 5 步设计，S2 补 overflow 方案
- **Q5**：状态 F 的 trailing chat 是否有消息数上限？后端无限制，前端是否虚拟滚动？S3 决定（初版不做虚拟滚动，监控是否必要）

---

## §8 工件路径

| 工件 | 路径 | 状态 |
|---|---|---|
| Requirement | `numind-server/requirements/sop-runtime-visual-redesign.md` | S0 已产出 |
| Proposal（本文件） | `numind-server/proposals/sop-runtime-visual-redesign-proposal.md` | S1 本次回填 |
| Mockup 01 | `numind-server/proposals/sop-runtime-visual-redesign-mockups/01-active-and-history.html` | 已产出 |
| Mockup 02 | `numind-server/proposals/sop-runtime-visual-redesign-mockups/02-additional-states.html` | 已产出 |
| Backend audit | `numind-server/proposals/sop-runtime-visual-redesign-backend-audit.md` | 已产出 |
| Spec | `numind-server/specs/sop-runtime-visual-redesign-spec.md` | 待 S2 产出 |
| Plan | `numind-server/plans/sop-runtime-visual-redesign-plan.md` | 待 S3 产出 |

---

*本 proposal 回填于 2026-04-11，基于 S0 + S1 已完成的视觉探索、用户决策和后端摸底。S2 spec 阶段应直接基于本 proposal 的决策清单、视觉契约 mockup、以及未决问题 Q1-Q5 展开设计。*
