# 前端设计体系建立 — 技术设计 (NDF S2 Spec)

## 概述

为莫小派建立一个**双文件设计体系**：
- `.impeccable.md`（项目根目录）—— 由 `/teach-impeccable` 一次访谈产出，**impeccable 家族 runtime 工具的真实输入**。承载品牌叙事（who/why/feel/principles）。
- `DESIGN.md`（项目根目录）—— 手写 200-300 行薄文档，**人类可读 + `/design-review` 校准**。承载数值参考（hex/字号/间距/组件清单）。

**涉及仓库**：
- numind-web-v3（v3 是 master，spec 中所有 token 数值来源）
- numind-admin-web（仅 variables.css 加注释指针，不改值）
- 根目录 `Codes/`（新建 .impeccable.md + DESIGN.md，修改 CLAUDE.md，修改 .claude/rules/ui-ux.md）

**明确排除**：
- admin 实际 token 重画（→ follow-up `admin-rebrand-to-design-md`）
- 用新体系做的第一个真实新页面（→ follow-up `frontend-design-system-validation`）
- S5 内部验证用的"关于莫小派"页代码 commit（仅作为本 feature 验证手段，不进 develop）
- 任何 LLM 调用 / 数据库变更 / API 端点（本 feature 是文档型，N/A）

**前置依据**：
- Requirement card: `numind-server/requirements/frontend-design-system.md`
- S1 R1 office-hours design: `~/.gstack/projects/Codes/zhiyuchen-ndf-s1-design-20260410-155313.md`
- S1 R2 runtime audit: `numind-server/proposals/runtime-skill-audit.md` (FAIL → Option D pivot)
- S1 R3 proposal: `numind-server/proposals/frontend-design-system-proposal.md`

---

## §1 文件清单与边界

### 1.1 双文件分工原则

| 维度 | `.impeccable.md` | `DESIGN.md` |
|------|------------------|-------------|
| **关注层** | Narrative（叙事） | Reference（数值） |
| **谁读** | `/frontend-design`、`/normalize`、`/polish`、`/audit`（自动）+ AI 在 prompt 里 @ 引用 | `/design-review`（自动）+ 人类手翻 + AI 需要具体数值时 @ 引用 |
| **格式** | 纯散文段落 + bullet（impeccable 家族原生格式） | 结构化 markdown 表格 |
| **修改频率** | 低（品牌定下来很少改） | 中（增加新 token 时改） |
| **同步成本** | 与 DESIGN.md **不需要同步**，关注层不同 | 同上 |
| **长度** | 50-100 行 | 200-300 行 |

**关键约定**：两份文件**故意不同步内容**。一份是叙事，一份是数据。修改 DESIGN.md 的 hex 值不需要更新 .impeccable.md 的"翠绿系"描述；修改 .impeccable.md 的品牌人格不需要改 DESIGN.md 的字号表。这避免了双文件维护的隐形陷阱。

### 1.2 完整文件清单

| # | 路径 | 操作 | 预估行数 | 内容来源 |
|---|------|------|----------|---------|
| 1 | `.impeccable.md` | 新建 | 50-100 | `/teach-impeccable` 访谈产出 + 手工校准 |
| 2 | `DESIGN.md` | 新建 | 200-300 | 手写，数值来自 `numind-web-v3/src/shared/styles/variables.css` |
| 3 | `CLAUDE.md`（根目录） | 修改 | +3 行 | 在 §3 通用硬规则末尾加"前端任务必读 `@.impeccable.md`，详细 token 见 `@DESIGN.md`" |
| 4 | `.claude/rules/ui-ux.md` | 削薄重写 | 当前 ~50 行 → 削至 ≤30 行 | 仅保留硬规则 + 指针 |
| 5 | `numind-web-v3/src/shared/styles/variables.css` | 顶部加注释 | +5 行 | 注释标识 v3 = master，指向 DESIGN.md §2 |
| 6 | `numind-admin-web/src/styles/variables.css` | 顶部加注释 | +5 行 | 注释标识 admin pending rebrand，指向 DESIGN.md §2 + follow-up |
| 7 | `numind-web-v3/CLAUDE.md` | 修改 | +2 行 | UI 章节加指针到根 DESIGN.md |
| 8 | `numind-admin-web/CLAUDE.md` | 修改 | +2 行 | 同上 |
| 9 | `frontend-workflow.md` | **不创建** | 0 | 工具数已减半，14 verbs 直接列在 ui-ux.md 末尾，不需独立文件 |
| 10 | S5 验证页面（"关于莫小派"） | 不 commit | ~150 行 Vue 单文件 | 拆为独立 follow-up `frontend-design-system-validation` |

**注：** 第 9 项是 S2 拍板的简化决策。原 R3 proposal 留待 S2 决定 frontend-workflow.md 是否新建。本 spec 决定 **不新建** —— 工具清单（impeccable 家族 7 个 runtime + 14 个动词）直接列在削薄后的 ui-ux.md 末尾，避免文件碎片化。

---

## §2 .impeccable.md 内容规格

### 2.1 章节结构

按 `/teach-impeccable` SKILL.md 第 51-65 行的**impeccable 原生 4 章节模板**（不可改名 / 不可加章节，否则 runtime 工具读取会失效）：

```markdown
## Design Context

### Users
[谁用 / 什么场景 / 想完成什么 job]

### Brand Personality
[品牌人格 3 个形容词 / 期望情绪]

### Aesthetic Direction
[视觉方向 / 参考 / 反参考 / 主题]
[**含 Color Direction 子段**：描述性的色彩方向，不含 hex 值]
[**含 Typography Direction 子段**：字体家族 + 衬线/无衬线决策]

### Design Principles
[3-5 条核心原则]
```

**注**：proposal §4.2 AC1 列出的 6 项（Project Context / Brand Personality / Aesthetic Direction / Design Principles / Color Direction / Typography Direction）映射到 impeccable 原生 4 章节如下：
- "Project Context" ≈ Users 章节内容
- Color Direction 和 Typography Direction 是 **Aesthetic Direction 的子段**，不是独立章节
- 其余 4 项一一对应

这一映射规则会在本 spec §8 的 AC 检查中明确，避免 S4 实施时按 6 章节写而破坏 impeccable 解析。

### 2.2 各章节内容草稿（teach-impeccable 访谈前的我方 baseline）

**注**：以下是 spec 阶段的 baseline 草稿。实际 .impeccable.md 由 `/teach-impeccable` 访谈产出，会与 baseline 比对修正。

#### §2.2.1 Users

> 莫小派 (Numind) 服务于**销售/运营/客户成功/服务岗位的一线员工和管理者**。他们需要把"模糊的工作目标"快速转化为"标准化可复制的执行步骤"。典型用户每天面对：客户跟进、销售话术、SOP 执行、知识检索、报告撰写等高频但重复的任务。
>
> 用户在使用时的心智状态：**忙、被多项任务切断、希望少思考多产出**。AI 需要"懂他在做什么并主动给出下一步"，而不是问"你想要什么"。

#### §2.2.2 Brand Personality

> 三个形容词：**专业可信 / 实干高效 / 有温度**
>
> 期望情绪：用户打开莫小派的第一感觉应该是"这工具像一个靠谱的同事"，不是"又一个炫酷的 AI 玩具"。颜色温暖（翠绿而非冷蓝），版式克制（不堆砌图标和卡片），文案直接（不绕弯子）。
>
> 反参考：避免硅谷 SaaS 通用气质（cyan-on-dark、purple gradient、glassmorphism、AI 色彩调色板）。避免过度 playful（Notion 式的 emoji 装饰）。避免过度严肃（Salesforce 式的拥挤表格）。

#### §2.2.3 Aesthetic Direction

> **核心方向：刊物气质 + 工业可靠**
>
> 灵感来源：早期 Stripe 的稳重 + Linear 的克制 + 中文阅读类产品的温度（如「得到」app 的 typography 选择）。
>
> 视觉特征：
> - 翠绿主色（HSL `160, 72%, 40%`）—— 不是科技感的绿，是带土地感的绿
> - 衬线 heading（Georgia + 宋体）—— 像杂志而不是控制台
> - 多层 shadow + gradient bg（v3 已有）—— 不扁平、有空间感
> - 间距宽松（不挤）—— 用户的眼睛要能呼吸
>
> 反参考：material design / fluent / Tailwind 默认 indigo / 任何"AI 色彩调色板"

#### §2.2.4 Design Principles

> 1. **数据密度优先于装饰**：表格胜过卡片，列表胜过网格，数字胜过图标
> 2. **下一步明确**：每个屏幕都应该让用户知道"现在做什么 / 接下来去哪"，不留迷茫
> 3. **温度藏在细节里**：不靠 emoji 和插画堆砌，靠衬线字体 + 翠绿色调 + 文案语气体现
> 4. **管理端 ≠ 用户端，但同一品牌**：管理端用 DataTable 严肃布局，用户端可灵活，但品牌色 / 字体 / 间距 / 组件 API 完全一致
> 5. **不发明轮子**：先看 v3 现有组件清单（AppButton/AppInput/MainLayout 等）能否复用，再考虑新建

### 2.3 与 v3 现状的 reconcile 规则

`/teach-impeccable` 访谈过程中，如果它推荐的方向与 v3 现状冲突：
- **品牌色**：v3 是 master，必须采用 `hsl(160, 72%, 40%)` 翠绿。如果 teach-impeccable 推荐紫蓝/科技色 → 拒绝并修正
- **字体**：v3 用衬线 heading，必须保留。如果 teach-impeccable 推荐 sans-serif everywhere → 拒绝并修正
- **间距系统**：v3 用 T-shirt size (`--space-xs` 到 `--space-4xl`)，必须保留命名规范
- **组件命名**：v3 的 `AppButton`/`AppInput`/`MainLayout`/`AppSidebar` 等命名是事实标准，不允许重命名

### 2.4 访谈失败模式处理

**case A — `/teach-impeccable` 卡在某个问题上**（用户答不出 e.g. "目标用户是谁"）

处理步骤：
1. 暂停访谈，不强行让用户给答案
2. 用 §2.2 的 baseline 答案作为占位（"销售/运营/客户成功一线员工和管理者"）
3. 在 .impeccable.md 中标注该字段为 `[BASELINE — 待用户后续校准]`
4. 通过 → 完成 .impeccable.md 写入，记录 deferred 项到 manifest
5. 不通过 → Pause and Ask: 是否要回退 S0 重新定义产品定位（极小概率，但合法触发）

**case B — 访谈耗时超过 60 分钟**

时间盒：access 进入 60 分钟后强制暂停。原因通常是：
- 问题设计太宽泛 → 改用 §2.2 baseline 直接 commit
- 用户思路不清 → 同上 + 标注 baseline
- teach-impeccable 反复纠缠某点 → 手动跳过该问题，记录在 manifest deferred

**case C — 访谈结果与 §2.2 baseline 偏差过大**（>50% 章节内容不一致）

可能原因：用户的实际品牌方向与 spec 草案 baseline 完全不同。处理：
1. **不**直接用访谈结果覆盖 baseline
2. Pause and Ask 用户：偏差是有意的（baseline 错了）还是无意的（teach-impeccable 误解了）
3. 前者 → 接受访谈结果，更新 baseline 到 spec 中（修订 spec 后回到 S2 gate）
4. 后者 → 手工编辑 .impeccable.md 修正

---

## §3 DESIGN.md 内容规格

### 3.1 章节结构（8 章）

```markdown
# 莫小派设计系统 (Design System)

> 单一权威设计参考。.impeccable.md 是品牌叙事，本文件是数值参考。两者关注层不同，无需同步。

## §1 品牌总览
## §2 颜色 Tokens
## §3 字体系统
## §4 间距系统
## §5 圆角与阴影
## §6 布局规则（用户端 vs 管理端）
## §7 组件清单
## §8 工具清单（impeccable 家族）
```

**章节调整说明**：原 spec 草案为 7 章，缺 §6 Layout Rules（违反 proposal §4.2 AC2）。现补 Layout Rules 为独立 §6，组件清单顺延为 §7，工具清单顺延为 §8。下文 §3.7 / §3.8 的章节编号也相应调整。

### 3.2 §1 品牌总览（约 30 行）

一段话品牌描述 + 一张 master 表格：

| 维度 | 决策 | 来源 |
|------|------|------|
| Master 仓库 | numind-web-v3 | S1 R1 决策 (2026-04-10) |
| 品牌主色 | 翠绿 `hsl(160, 72%, 40%)` | v3 variables.css line 31 |
| Heading 字体 | Georgia + 宋体 | v3 variables.css line 65 |
| Body 字体 | -apple-system + PingFang SC | v3 variables.css line 63-64 |
| 主要布局 | 用户端：灵活 / 管理端：DataTable 表格 | .claude/rules/ui-ux.md §2 |

### 3.3 §2 颜色 Tokens（约 60 行）

**完整 hex 表，全部从 v3 `numind-web-v3/src/shared/styles/variables.css` 抽取**（行号已核对）：

#### 背景与表面
| Token | 值 | 用途 | v3 行号 |
|-------|-----|------|---------|
| `--bg` | `#F7F8FB` | 页面背景 | 8 |
| `--bg-gradient` | `linear-gradient(165deg, #F7F8FB 0%, #FFFFFF 50%, #F5F7FA 100%)` | 全页 gradient bg | 9 |
| `--surface` | `#FFFFFF` | 卡片/容器背景 | 11 |
| `--surface-hover` | `#F3F4F8` | 悬浮态 | 12 |
| `--surface-tint` | `#F9FAFB` | 微淡表面 | 13 |

#### 文本
| Token | 值 | 用途 | v3 行号 |
|-------|-----|------|---------|
| `--text` | `#1A1D26` | 主文本 | 15 |
| `--text-secondary` | `#5F6577` | 次要文本 | 16 |
| `--text-muted` | `#8B90A0` | 弱化文本 | 17 |

#### Sidebar 专用（v3 master 的深绿侧边栏，admin 用紫蓝侧边栏 pending rebrand）
| Token | 值 | 用途 | v3 行号 |
|-------|-----|------|---------|
| `--sidebar-bg` | `hsl(160, 45%, 28%)` | 侧边栏背景（深绿） | 19 |
| `--sidebar-text` | `hsl(160, 15%, 65%)` | 侧边栏默认文本 | 20 |
| `--sidebar-text-hover` | `hsl(0, 0%, 100%)` | 侧边栏悬浮文本 | 21 |
| `--sidebar-active-bg` | `hsl(160, 50%, 35%)` | 选中项背景 | 22 |
| `--sidebar-active-text` | `hsl(0, 0%, 100%)` | 选中项文本 | 23 |

#### 品牌色与强调
| Token | 值 | 用途 | v3 行号 |
|-------|-----|------|---------|
| `--accent` | `hsl(160, 75%, 44%)` | 强调 | 25 |
| `--accent-hover` | `hsl(160, 75%, 38%)` | 强调悬浮 | 26 |
| `--accent-soft` | `hsl(160, 60%, 93%)` | 强调淡 (badge bg) | 27 |
| `--accent-light` | `hsl(160, 70%, 68%)` | 强调亮 | 28 |
| `--accent-link` | `hsl(160, 75%, 38%)` | 链接色 | 29 |
| `--primary` | `hsl(160, 72%, 40%)` | **主色（CTA / brand）** | 31 |
| `--primary-hover` | `hsl(160, 72%, 34%)` | 主色悬浮 | 32 |
| `--primary-foreground` | `hsl(0, 0%, 100%)` | 主色背景上的文本（白） | 33 |
| `--accent-badge` | `hsl(160, 75%, 44%)` | Badge 强调 | 35 |
| `--accent-ultra-soft` | `hsl(160, 60%, 95%)` | 超淡强调 | 36 |

#### 边框与分割
| Token | 值 | 用途 | v3 行号 |
|-------|-----|------|---------|
| `--border` | `#E2E4EA` | 默认边框 | 38 |
| `--border-light` | `#EEEFF3` | 轻边框 | 39 |
| `--divider` | `#F0F1F5` | 分割线 | 40 |

#### ⚠️ 状态色缺失（known gap）

**v3 variables.css 当前没有显式定义 success / warning / danger / info 的状态色 tokens**。这是 v3 的现状漏洞，不在本 feature 范围内修复。

- admin variables.css 有完整状态色（`#10B981` 等 Tailwind 风格），但属于 admin 待 rebrand 的旧体系
- DESIGN.md §2 在状态色子节中**明确标注此 gap**，并指向 follow-up：
  - **`admin-rebrand-to-design-md`** 中将引入 v3 风格的状态色 tokens
  - 在 admin rebrand 完成前，新写的 v3 代码如需状态色，临时使用 `--accent`（成功）/ `--text-secondary`（警告）/ 内联 `#EF4444`（danger）+ TODO 注释指向此 gap

### 3.4 §3 字体系统（约 30 行）

字体家族 + 字号阶梯表（Token 名称已对照 v3 variables.css 行 63-67 修正）：

```css
--font-sans: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC',
             'Hiragino Sans GB', 'Microsoft YaHei', 'Helvetica Neue', sans-serif;
--font-heading: Georgia, 'Times New Roman', 'Songti SC', 'SimSun', serif;
--font-mono: 'JetBrains Mono', SFMono-Regular, Menlo, Monaco, Consolas,
             'Liberation Mono', 'Courier New', monospace;
```

| Token | 值 | 用途 |
|-------|-----|------|
| `--text-xs` | 12px | 标签 / 元数据 |
| `--text-sm` | 14px | 次要正文 |
| `--text-base` | 16px | 主正文 |
| `--text-lg` | 18px | 强调正文 |
| `--text-xl` | 20px | 小标题 |
| `--text-2xl` | 24px | 中标题 |
| `--text-3xl` | 30px | 页面标题 |

行高：`--line-height-tight: 1.3` / `normal: 1.5` / `relaxed: 1.7`

### 3.5 §4 间距系统（约 25 行）

T-shirt size 命名（v3 现状）：

| Token | 值 | 用途 |
|-------|-----|------|
| `--space-xs` | 4px | 紧密间距 |
| `--space-sm` | 8px | 元素内边距 |
| `--space-md` | 12px | 默认间距 |
| `--space-lg` | 16px | 段落间距 |
| `--space-xl` | 24px | 区块间距 |
| `--space-2xl` | 32px | 大区块 |
| `--space-3xl` | 40px | 视觉分组 |
| `--space-4xl` | 48px | 页面级间距 |

注：v3 还有数字索引兼容别名 `--space-1` 到 `--space-12`，DESIGN.md 不推荐使用，新代码统一用 T-shirt size。

### 3.6 §5 圆角与阴影（约 30 行）

#### 圆角

| Token | 值 | 用途 | v3 行号 |
|-------|-----|------|---------|
| `--radius-sm` | `6px` | 输入框 / 小按钮 | 48 |
| `--radius-md` | `12px` | 默认（卡片） | 49 |
| `--radius-lg` | `16px` | 大卡片 | 50 |
| `--radius-xl` | `20px` | Modal | 51 |
| `--radius-pill` | `999px` | Pill / Avatar | 52 |

#### 阴影（完整 hex 值）

| Token | 值 | 用途 | v3 行号 |
|-------|-----|------|---------|
| `--shadow-sm` | `0 1px 2px rgba(0, 0, 0, 0.04), 0 1px 3px rgba(0, 0, 0, 0.03)` | 微弱阴影 | 42 |
| `--shadow-md` | `0 2px 8px rgba(0, 0, 0, 0.06), 0 1px 3px rgba(0, 0, 0, 0.04)` | 默认卡片阴影 | 43 |
| `--shadow-lg` | `0 8px 24px rgba(0, 0, 0, 0.06), 0 2px 8px rgba(0, 0, 0, 0.03)` | Modal / Popover | 44 |
| `--shadow-focus` | `0 0 0 4px hsl(158 50% 92% / 0.5)` | 焦点环（翠绿 tint） | 45 |
| `--shadow-card` | `0 1px 4px rgba(0, 0, 0, 0.04), 0 0 1px rgba(0, 0, 0, 0.06)` | 列表卡片 | 46 |

### 3.7 §6 布局规则（约 25 行）

**用户端 (numind-web-v3) vs 管理端 (numind-admin-web) 布局差异**

| 维度 | 用户端 | 管理端 |
|------|--------|--------|
| 主布局组件 | `MainLayout` + `AppSidebar` | `AdminLayout` + `AdminSidebar` |
| 列表展示 | 灵活：卡片 / 表格 / 网格 / 时间线 按场景选 | **必须用 `DataTable` 表格**，禁止卡片网格 |
| 主要操作位置 | 页面右上 / 卡片内 / floating action | 表格上方工具栏 + 行级 actions |
| 表单密度 | 宽松（每字段独占一行 + 留白） | 紧凑（多列布局，提高密度） |
| 配色 | 翠绿主导，gradient bg | 翠绿主导（rebrand 后），现状 admin 用紫蓝 |
| 字体 | Heading 用 `--font-heading`（衬线），强调编辑气质 | Heading 也用衬线（rebrand 后），现状 admin 用 sans-serif |
| 间距 | 宽松（多用 `--space-xl` 及以上） | 紧凑（多用 `--space-md`/`--space-lg`） |

**响应式断点**（两端共享）：
- mobile: `< 768px`
- tablet: `768px - 1024px`
- desktop: `> 1024px`

**主要硬规则**（迁自原 ui-ux.md，删除冗余后保留）：
- 管理端**禁止**用卡片网格代替 DataTable —— 这是历史教训（保留在削薄后的 ui-ux.md 中作为硬规则）
- 所有异步视图必须处理 4 状态：loading / empty / error / success
- 销毁性操作必须有确认 dialog

### 3.8 §7 组件清单（约 50 行）

按 v3 `CLAUDE.md` 已有的组件清单完整转录：

**用户端 (numind-web-v3) 组件**

| 分类 | 组件 |
|------|------|
| Common | `AppButton`、`AppInput`、`InsufficientCreditsDialog`、`ModelSelector` |
| Layout | `MainLayout`、`AppSidebar` |
| Sales | `ChatArea`、`ChatMessage`、`CitationModal`、`DeleteSessionModal`、`GlobalLoadingStatus`、`ImagePreviewModal`、`ImagePreviewStrip`、`InputArea`、`KbTagStrip`、`MainHeader`、`NewChatModal`、`RenameSessionModal`、`SalesStageDropdown`、`ScrollToBottomBtn`、`SessionSidebar`、`ThinkingBlock`、`WelcomeScreen` |
| Modals | `ChatStyleModal`、`KbModal`、`ProfileModal` |

**管理端 (numind-admin-web) 组件**

| 分类 | 组件 |
|------|------|
| Common | `AppButton`、`AppInput`、`AppSelect`、`AppToast`、`DataTable`、`StatusBadge`、`ConfirmModal`、`StatsCard` |
| Layout | `AdminLayout`、`AdminSidebar` |

**关键 variants**（复用前必读）：
- `AppButton`: variants `primary | secondary | text`，sizes `sm | md | lg`，`loading` 状态
- `AppInput`: 含验证状态，禁止用原生 `<input>`
- `DataTable`（admin 专用）: 含 sort / filter / pagination 内置

完整 variants / props 详见各 `.vue` 文件 `defineProps`，本 spec 不重复转录。

### 3.9 §8 工具清单（impeccable 家族，约 30 行）

#### Runtime 工具（每次开发新页面用）

| 工具 | 触发时机 | 干什么 | 读什么文件 |
|------|----------|--------|------------|
| `/frontend-design` | 新建页面 | 主力生成器 | `.impeccable.md` |
| `/normalize` | 生成后立即 | 对齐 tokens / 间距 | 通过 frontend-design 间接读 `.impeccable.md` |
| `/polish` | 上线前 | 微调对齐 / 间距 / 细节 | 同上 |
| `/harden` | 上线前 | 边界态 / i18n / 溢出 | 不读品牌文件，关注韧性 |
| `/audit` | 上线前周期 | a11y / 性能 / 主题综合 P0-P3 | 内置 rubric，不读品牌文件 |
| `/design-review` | 浏览器渲染 QA 时 | 视觉对照 + 自动 fix + commit | **`DESIGN.md`** ⭐（唯一直接读 DESIGN.md 的工具） |

#### Setup 工具（一次性，建立或修订品牌时）

| 工具 | 用途 |
|------|------|
| `/teach-impeccable` | 访谈 + 读现有代码，生成或更新 `.impeccable.md` |

#### 情境性动词工具（按需调用，14 个）

`/animate` `/delight` `/distill` `/clarify` `/arrange` `/typeset` `/colorize` `/bolder` `/quieter` `/overdrive` `/extract` `/adapt` `/onboard` `/optimize`

每个一句话触发场景描述（"动效不够 → /animate"、"copy 不清晰 → /clarify" 等）。

#### 已废弃（spec 不收录，仅在备注中提）

`/design-consultation`、`getdesign` CLI、`/design-shotgun`、`/plan-design-review`、`/critique` —— S1 R2 audit 验证它们与 impeccable 家族不咬合，用了等于齿轮塞错传动装置。

---

## §4 CLAUDE.md 修改规格

### 4.1 根 `Codes/CLAUDE.md` 修改

在 §3 通用硬规则的"必须做的事"列表末尾追加：

```markdown
- 前端任务必读 `@.impeccable.md`（品牌叙事）和 `@DESIGN.md`（数值参考）；前端 UI 工具链详见 `DESIGN.md §7 工具清单`
```

### 4.2 `numind-web-v3/CLAUDE.md` 修改

在 §1 技术栈声明 末尾追加：

```markdown
- **设计语言**: 见根目录 `DESIGN.md`（v3 是 master 品牌）+ `.impeccable.md`（品牌叙事）
```

### 4.3 `numind-admin-web/CLAUDE.md` 修改

在 §1 技术栈声明 末尾追加：

```markdown
- **设计语言**: 见根目录 `DESIGN.md`（v3 是 master，admin 当前是 Tailwind 默认值，rebrand 计划见 follow-up `admin-rebrand-to-design-md`）+ `.impeccable.md`
```

---

## §5 ui-ux.md 削薄规格

### 5.1 当前内容（~50 行）→ 削薄后目标（≤30 行）

**保留的硬规则**（3-5 条不可妥协）：
1. 管理端必须用 DataTable 表格布局，不用卡片网格
2. 所有异步视图必须处理 loading / empty / error / success 四种状态
3. 表单验证在 blur 时触发，不在每次 keystroke 触发
4. 销毁性操作必须有确认 dialog
5. 禁止使用外部 UI 框架（Element Plus / Ant Design Vue 等）

**删除的内容**（迁移到 DESIGN.md）：
- CSS 变量列表 → DESIGN.md §2-§5
- 间距 8px 网格 → DESIGN.md §4
- 字体层次 → DESIGN.md §3
- 公共组件清单 → DESIGN.md §6
- 表单细节模式 → 仅保留"blur 触发"硬规则，其他细节移除

**新增的指针段**：
```markdown
> 详细设计 token 和组件清单见根目录 `@DESIGN.md`。AI 工作流见 `@.impeccable.md`。本文件仅保留**不可妥协的硬规则**。
```

---

## §6 variables.css 注释规格

### 6.1 `numind-web-v3/src/shared/styles/variables.css` 顶部注释

```css
/* =====================================================
 * 莫小派设计系统变量 — 用户端 (numind-web-v3)
 *
 * ⭐ THIS FILE IS THE SOURCE OF TRUTH for brand tokens (v3 = master).
 *
 * Numerical reference (mirror): /DESIGN.md §2-§5
 *   — DESIGN.md is derived FROM this file, not the other way around.
 *   — When you change a value here, update DESIGN.md tables to match.
 *
 * Brand narrative: /.impeccable.md
 *   — Used by impeccable runtime tools (/frontend-design, /normalize, etc.)
 *
 * Admin (numind-admin-web) is currently NOT compliant; pending rebrand.
 *   — Follow-up feature: admin-rebrand-to-design-md
 * ===================================================== */
```

### 6.2 `numind-admin-web/src/styles/variables.css` 顶部注释

```css
/* =====================================================
 * 莫小派设计系统变量 — 管理端 (numind-admin-web)
 *
 * ⚠️ CURRENT STATE: Tailwind defaults. NOT yet rebranded.
 * Master brand lives in numind-web-v3/src/shared/styles/variables.css
 * Reference: /DESIGN.md §2-§5
 *
 * Rebrand planned as follow-up feature: admin-rebrand-to-design-md
 * Until rebrand completes, expect visual divergence from user-side.
 * ===================================================== */
```

---

## §7 S5 验证页面规格："关于莫小派 / About"

### 7.1 选定理由

**为什么是"关于"页**：
1. **小**：单一静态展示页，1-2 小时可建
2. **测得到品牌叙事**：含品牌介绍文案 → 测试 .impeccable.md 是否真的影响 AI 的文案语气和版式选择
3. **测得到数值参考**：含 hero / cards / typography hierarchy → 测试 DESIGN.md 的 token 是否被 AI 应用
4. **真实有用**：任何 SaaS 都缺一个像样的"关于"页，不是凭空发明的验证页
5. **不依赖后端**：纯前端展示，无 API 改动，无 DB 迁移，无权限控制

### 7.2 验证页面所需测试维度

| 维度 | 测试方法 | 通过标准（**全部二元判定**） |
|------|----------|------------------------------|
| **a. AI 的版式选择是否符合 .impeccable.md 的"刊物气质"** | grep + 视觉 review 生成的 .vue 文件 | **5 个二元 FAIL 触发条件**（任一触发 → 维度 a FAIL）：(1) heading 元素使用 sans-serif（grep `font-family.*sans` 在 h1-h6 内）→ FAIL；(2) 出现卡片网格 layout（grep `display: grid` + `grid-template-columns: repeat`）→ FAIL；(3) 区块间距 < `--space-xl`（grep `margin.*var(--space-(xs\|sm\|md\|lg))` 在区块容器上）→ FAIL；(4) 出现 emoji 装饰（grep `[\u{1F300}-\u{1F9FF}]`）→ FAIL；(5) 出现非翠绿系主色（grep `color: #[^F].*` 排除翠绿/中性灰系）→ FAIL |
| **b. AI 是否用了 DESIGN.md 定义的 token** | grep 生成的 .vue 文件，统计 hex 值数量 vs `var(--xxx)` 引用数量 | **token 引用率 ≥ 80%**：`var(--*)` 引用次数 ÷ (引用 + hex 字面量) ≥ 0.80 |
| **c. /normalize 跑过后是否真的应用 .impeccable.md** | 故意制作一个 "polluted" 版本：硬编码 `#4F46E5`（admin 紫蓝）+ 硬编码 `padding: 13px`（非 token 间距）+ heading 用 `font-family: Inter, sans-serif`，跑 /normalize | normalize 必须给出**至少 3 类**修复建议：(1) 紫蓝改翠绿 (2) 13px 改为 `--space-md` 或 `--space-lg` (3) Inter 改 Georgia |
| **d. /design-review 是否引用 DESIGN.md** | 跑 /design-review，文本搜索其输出 | 输出中**至少出现 1 次** `DESIGN.md` 字样（"DESIGN.md says X but code does Y" 或类似形式） |

**整体通过门槛**：4 个维度全部 PASS。**任一 FAIL** → 进入 NDF S5 失败处理流程：分析根因（是 .impeccable.md 内容质量问题 / 工具机制问题 / spec 错误），按 NDF Rule 6 回退协议处理。

### 7.3 验证页面**不**做什么

- 不 commit 到 develop（仅作内部验证）
- 不进 router（避免污染线上路由表）
- 不写测试代码
- 不与现有页面集成

正式的"建立后第一个真实新页面"作为 follow-up `frontend-design-system-validation` 走 Hotfix 流程并 commit。

---

## §8 PRD 验收标准 (AC) 覆盖检查

| AC | 描述（来自 R3 proposal §4.2） | Spec 中的覆盖位置 | 状态 | 备注 |
|----|------------------------------|-------------------|------|------|
| AC1 | .impeccable.md 存在且含 6 个章节 | §2.1 + §2.2 草稿 + §2.3-§2.4 失败处理 | ⚠️ **AC1 需更新** | proposal AC1 列 6 章节，但 impeccable 原生模板只有 4 章节。Color Direction 和 Typography Direction 是 Aesthetic Direction 的**子段**，不是独立章节。**proposal AC1 需要同步更新为 4 章节 + 子段说明** |
| AC2 | 薄 DESIGN.md 存在且含 7 个章节 | §3.1 (8 章节) + §3.2-§3.9 | ⚠️ **AC2 需更新** | spec 实际定义 8 章节（新增 §6 Layout Rules）。**proposal AC2 需要同步更新为 8 章节** |
| AC3 | 根 CLAUDE.md 含前端任务必读指针 | §4.1 | ✅ | 完整 |
| AC4 | ui-ux.md 削薄到 ≤30 行 | §5.1 | ✅ | 完整 |
| AC5 | 两套 variables.css 加注释指针 | §6.1 + §6.2 | ✅ | 完整 |
| AC6 | S5 验证页面（关于页）+ 4 维度测试 | §7.1 + §7.2 | ✅ | 4 维度全部二元化判定标准 |
| AC7 | manifest 状态完整 | 由 S4 task 末尾自动更新，spec 不需特别规格 | ✅ | 完整 |

**结论**：spec 内容完整，但 **AC1 和 AC2 需要同步更新到 R3 proposal**，否则 S4 实施时会出现 spec / proposal 不一致。S2 → S3 gate 时会同步更新 proposal。

---

## §9 风险与缓解（spec 阶段新增的风险）

| 风险 | 缓解 |
|------|------|
| **R7**: `/teach-impeccable` 访谈产出与 §2.2 baseline 偏差大 | spec §2.3 已定义 reconcile 规则；偏差时手工修正 .impeccable.md |
| **R8**: `/design-review` 的 80 项 hardcoded checklist 与 DESIGN.md §2-§5 token 冲突 | 本 spec §3.8 已记录此为 PARTIAL enforcement 已知风险；冲突时以 DESIGN.md 为准 |
| **R9**: §7 验证页面的 4 维度测试 (a/b/c/d) 中维度 (a) 是主观判断 | 用主观判断 + 反例（卡片网格出现 = FAIL）作为客观补充 |
| **R10**: 管理端 admin 暂时与 v3 master 视觉不一致，可能造成用户困惑 | variables.css 注释明确标注；follow-up entry 已登记；不在本 feature 范围 |

---

## §10 S2 → S3 Gate 检查清单

| 检查项 | 状态 |
|--------|------|
| ✅ Spec 文件存在 | 本文件 |
| ⚠️ Spec 覆盖 PRD 全部 AC | AC3-AC7 ✅；**AC1/AC2 需要同步更新 proposal**，详见 §8 |
| ✅ 多仓库 API 契约 | N/A — 本 feature 不涉及 API |
| ✅ LLM trace topology | N/A — 本 feature 不涉及 LLM 调用 |
| ✅ 文件清单完整可执行 | §1.2 含 10 项 + 行数估算 + 内容来源 |
| ✅ 内容草稿可作为 S4 task 输入 | §2.2 / §3.2-§3.8 / §4 / §5 / §6 / §7 全部含足够细节供 S4 编码 |
| ✅ 边界情况已考虑 | §9 R7-R10 + §2.4 case A/B/C |
| ✅ S5 验证策略可证伪 | §7.2 全部二元化判定 |
| **PENDING 用户确认事项**（gate 硬门禁）| 详见 §10.1 |
| ⏳ 用户确认设计方向 | **PENDING — 硬门禁** |

### 10.1 用户确认事项（必须 gate 时拍板）

1. **不创建 `frontend-workflow.md` 文件**（spec §11 决策 1）—— 14 verb 工具清单合并到削薄后的 ui-ux.md 末尾。原 R3 proposal 列其为 deliverable 第 3 项，本 spec 单方面取消。**需用户确认接受**。
2. **AC1 / AC2 同步更新到 proposal**：AC1 改为 4 章节（impeccable 原生格式）+ Color/Typography 作为 Aesthetic Direction 子段；AC2 改为 8 章节（新增 §6 Layout Rules）。**需用户确认更新**。
3. **状态色 gap**：v3 variables.css 不含 success/warning/danger tokens。本 feature **不修复**，标注为 admin-rebrand follow-up 范围。**需用户确认接受 gap**。
4. **About 页作为 S5 验证页面**：S2 在 §7 已选定。如有更想要的，告知。

---

## §11 备注：spec 阶段的简化决策

S2 阶段相比 R3 proposal 做了 1 个**简化决策**：

**决策 1**：**不创建 `frontend-workflow.md`**
- 原 R3 proposal 留待 S2 决定
- 本 spec 决定：14 个动词工具清单直接列在削薄后的 ui-ux.md 末尾，避免再多一个文件
- 理由：工具数已从 13 减半，分散到独立文件得不偿失；ui-ux.md + DESIGN.md §7 + .impeccable.md 三处已经完整覆盖

如果 S4 编码阶段发现 ui-ux.md 加上 14 个动词后超过 50 行，再考虑拆出 frontend-workflow.md。
