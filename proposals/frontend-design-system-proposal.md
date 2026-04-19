# 前端设计体系建立 — 提案 (NDF S1)

> **生成日期**：2026-04-10
> **轨道**：Standard
> **依据**：
> - Requirement card: `numind-server/requirements/frontend-design-system.md`
> - S1 Round 1 (Office-hours premise challenge): `~/.gstack/projects/Codes/zhiyuchen-ndf-s1-design-20260410-155313.md`
> - S1 Round 2 (Runtime skill audit): `numind-server/proposals/runtime-skill-audit.md`
> - 架构选定：**Option D** (Pivot from 13-tool DESIGN.md pipeline → impeccable-family-native)

---

## §1 方案概述 [客户可见]

> 本节用非技术语言写给 zhiyuchen 自己看。一人公司没有外部客户，"客户可见"等于"未来一个月的我"。

**问题**：每次让 AI 写新前端页面时，它的输出风格都不稳定 —— 有时候对，有时候偏离现有页面气质。原因不是 AI 笨，而是它没有一份"莫小派的设计语言是什么"的权威参考。

**方案**：花 1.5 小时建立两份文件：

1. **`.impeccable.md`**（由 `/teach-impeccable` 30 分钟访谈产出）—— AI 真正会读的文件。它知道莫小派的目标用户、品牌人格、美学方向、设计原则。未来当 AI 用 `/frontend-design` 写新页面、用 `/normalize` 检查代码、用 `/polish` 微调时，它会自动读这个文件作为指引。

2. **薄 `DESIGN.md`**（手写 200-300 行，约 1 小时）—— 人类可读的设计参考。包含 v3 的品牌色 hex 值、字号阶梯、间距系统、组件清单。给 `/design-review` 工具做校准用，也给未来的 zhiyuchen 自己当快查表。

**预期效果**：
- 新建页面时，AI 输出与 v3 现有页面风格一致度大幅提升
- 检查代码时，`/normalize` 和 `/audit` 能基于莫小派的品牌而不是通用规则给出建议
- 你不用每次都手把手告诉 AI "用绿色不要用紫色"

**这次和之前最大的不同**：原计划装 13 个工具搭流水线，audit 发现其中 5 个根本不读 DESIGN.md。pivot 后回到工具家族的母语 `.impeccable.md`，工具数减半，setup 时间砍 50%，但执行能力反而真实可用。

**不在本次范围内**：
- admin (numind-admin-web) 的实际重画 → 拆为独立 follow-up `admin-rebrand-to-design-md`
- 用新体系做出来的第一个真实新页面 → 拆为独立 follow-up `frontend-design-system-validation`

---

## §2 工作量估算 [一人公司无报价]

**对话回合数估算**：

| 阶段 | 估算工作量 | 说明 |
|------|------------|------|
| S2 spec | 60-90 min | `/teach-impeccable` 访谈 (~30min) + 对话校准（你回答品牌问题）+ 写薄 DESIGN.md 大纲 (~30min) |
| S3 plan | 30 min | 把 spec 拆成 5-7 个 task（写 .impeccable.md 内容 / 写 DESIGN.md 各章节 / 修改 ui-ux.md / 修改根 CLAUDE.md / variables.css 加注释指针） |
| S4 编码（文档生成） | 2-3 hours | 实际产出文档内容 + 每个 task 后两阶段 review (NDF Rule 6) |
| S5 验收 | 60 min | 用 `/frontend-design` 试做一个新页面 → 跑 `/normalize` 看是否真的应用了 .impeccable.md → `/design-review` 校准 → 写 QA 报告 |
| S6 人工验收 | 15 min | 你确认体系建立完毕 |
| S7 部署 | 5 min | 文档型 feature 无部署，只是 commit + tag |

**总计**：约 4-5 小时跨多个 session（可分多天）。

**与原计划对比**：原计划 setup 阶段 2.5-3.5h + 后续阶段 = 约 7-9h。pivot 后约 4-5h。**节省 35-50% 时间**，且执行能力从"空头支票"变成"真实可用"。

**交付时间线**：用户决定。建议本周内完成 S2-S4，下周做 S5-S6 验收。

---

## §3 技术可行性 [AI 内部]

### 3.1 现有功能复用

| 复用项 | 来源 | 用途 |
|--------|------|------|
| `/teach-impeccable` skill | 已安装于 `~/.claude/skills/teach-impeccable/` | Setup 阶段唯一入口工具，访谈 + 读现有 v3 代码产出 .impeccable.md |
| `/frontend-design` skill | 已安装于 `~/.claude/skills/frontend-design/` | Runtime 主力生成器，自动读 .impeccable.md |
| `/normalize` skill | 已安装 | Runtime enforcer，依赖 /frontend-design 加载 .impeccable.md 作为 context |
| `/polish` `/harden` `/audit` skills | 已安装 | Runtime 上线前检查 |
| `/design-review` skill (gstack) | 已安装 | 浏览器渲染 QA + DESIGN.md 校准 |
| v3 现有 `variables.css` | `numind-web-v3/src/shared/styles/variables.css` | 作为 master 品牌的视觉来源 → /teach-impeccable 会读它 |
| v3 现有组件清单 | `numind-web-v3/CLAUDE.md` 已列出 | /teach-impeccable 会扫描组件目录 |
| 现有 `.claude/rules/ui-ux.md` | 已存在 | 削薄为硬规则 + 指针到 DESIGN.md |

**关键观察**：本 feature **几乎不引入任何新工具**。所有需要的能力都已经在 `~/.claude/skills/` 里。这是一个"用对工具"的 feature，不是"装新工具"的 feature。

### 3.2 技术风险

| 风险 | 缓解方案 |
|------|---------|
| **R1**: `/teach-impeccable` 访谈结果与 v3 现状有冲突（例如它推荐的色调与 v3 的翠绿不一致） | 在 S2 访谈时显式告诉 teach-impeccable "v3 是 master，必须以 v3 视觉为基础"。如果它仍然偏离，手工编辑 .impeccable.md 修正 |
| **R2**: `.impeccable.md` 格式不够详细（它是 brand context 文件，不是 design tokens spec），AI 可能仍然在某些细节上漂移 | 用薄 DESIGN.md 作为补充，存详细的 hex 值、字号表、间距规范。当 AI 需要具体数值时引用 DESIGN.md（虽然 normalize 不读，但你可以在 prompt 中显式 @DESIGN.md） |
| **R3**: 信息在 .impeccable.md 与 DESIGN.md 之间不同步（双文件维护成本） | 显式约定：`.impeccable.md` 是 narrative（品牌人格、美学方向、设计原则），DESIGN.md 是 reference（数值表）。两者**关注不同的层次**，不需要同步。修改一处不影响另一处 |
| **R4**: design-review 用 80 项硬编码 checklist 而不是 DESIGN.md 主导，可能给出与 DESIGN.md 矛盾的建议 | 接受这是 PARTIAL enforcement。把 design-review 的输出当 "second opinion" 而非 "ground truth"。冲突时以 DESIGN.md / .impeccable.md 为准 |
| **R5**: C2 美学锚点未定（用户没明确审美方向），原计划用 design-shotgun 解决，pivot 后 shotgun 也用不上了 | `/teach-impeccable` 的 Phase 2 访谈本身会问"你想要什么 aesthetic / 给用户什么情绪"。如果你的回答足够清晰，访谈可以承担美学锚点的角色。备用方案：引导 teach-impeccable 直接以 v3 现状为锚（C2 Plan B 第 2 级） |
| **R6**: 一人公司无外部 review，可能错过自己看不见的盲点 | 已通过 NDF Rule 6（每个 task 两阶段 Sonnet review）+ NDF Rule 10（S3 plan 阶段独立 reviewer）+ S0 阶段已经发生的两次 reviewer review 部分缓解 |

**Audit-validated 风险（Round 2 已确认）**：原"runtime 层强制 DESIGN.md"是错觉。Pivot 后此风险已消除。

### 3.3 涉及仓库

- [x] **numind-server**: requirements/proposals/spec/plan 工件 + manifest 更新（仅文档）
- [x] **numind-web-v3**: variables.css 注释指针（**仅注释，不改值**）+ CLAUDE.md 引用更新
- [x] **numind-admin-web**: variables.css 注释指针（**仅注释**，admin token 改值不在本范围）+ CLAUDE.md 引用更新
- [x] **根目录**: 新建 `.impeccable.md` + 新建薄 `DESIGN.md` + 修改 `CLAUDE.md` 引用 + 修改 `.claude/rules/ui-ux.md` 削薄
- [ ] **`.claude/rules/frontend-workflow.md`** 是否新建：**待 S2 决定**。原计划新建，pivot 后由于工具数减半，可能直接合并到 ui-ux.md 或薄 DESIGN.md 里

### 3.4 AI 可观测性（如功能涉及 LLM 调用）

- [x] 涉及 LLM 调用：**否**
- 本 feature 不调用 Langfuse 或任何 LLM API，仅生成静态文档文件
- N/A

---

## §4 产品需求定义 — PRD [AI 内部]

### 4.1 用户故事

**注意**：本 feature 的"用户"是 `zhiyuchen + AI 工作流`，不是末端客户。所有用户故事映射到"未来开发新页面时的体验"。

1. **作为 zhiyuchen**，我希望在新建一个前端页面时只需要一个简单的 prompt（不需要 paste 一堆设计指南），就能让 AI 输出与 v3 现有页面气质一致的代码，**以便**我不必每次都手动校正颜色 / 字号 / 间距。

2. **作为 zhiyuchen**，我希望 AI 在写完代码后能自动检查"这段代码符合莫小派的设计语言吗"，**以便**我不必逐行 review 视觉细节。

3. **作为 zhiyuchen**，我希望未来 6 个月内任何新的前端工作都站在统一的设计语言之上，**以便**避免代码风格越来越散，最终需要大规模重构。

4. **作为 zhiyuchen**，我希望有一份人类可读的 DESIGN.md 作为"莫小派设计速查表"，**以便**当我自己需要查"主色 hex 是多少"时不必去翻 variables.css。

5. **作为未来加入项目的人**（如果有），我希望读一份文档就能理解莫小派的设计语言，**以便**快速上手不出现风格错乱。

### 4.2 验收标准

**[AC1] `.impeccable.md` 文件存在且符合 impeccable 原生 4 章节模板**（S2 spec §2.1 确认 — 不可改名 / 不可加章节，否则 runtime 工具读取失效）：
- [ ] Users（含项目背景 + 目标用户 + 使用场景 + Job to Be Done）
- [ ] Brand Personality（品牌人格 3 形容词 + 期望情绪）
- [ ] Aesthetic Direction（含视觉方向 + 参考 + 反参考 + 主题）
  - [ ] 子段：Color Direction（描述性，无 hex 值）
  - [ ] 子段：Typography Direction（字体家族 + 衬线/无衬线决策）
- [ ] Design Principles（3-5 条核心原则）

**[AC2] 薄 `DESIGN.md` 文件存在且包含以下 8 章节**（S2 spec §3.1 确认 — 新增 §6 Layout Rules）：
- [ ] §1 品牌总览（一段话品牌描述 + master 决策表）
- [ ] §2 颜色 Tokens（v3 master 完整 hex 表 — 背景/文本/sidebar/品牌色/边框，**状态色 gap 已标注**）
- [ ] §3 字体系统（字体家族 + 字号阶梯 + 行高）
- [ ] §4 间距系统（T-shirt size 命名）
- [ ] §5 圆角与阴影
- [ ] §6 布局规则（用户端 vs 管理端布局差异）
- [ ] §7 组件清单（v3 完整 + admin 完整）
- [ ] §8 工具清单（impeccable 家族 runtime + setup + 14 情境动词）

**[AC3] 根 `CLAUDE.md` 包含**：
- [ ] 一句指示："前端任务必读 `@.impeccable.md`，详细 token 数值见 `@DESIGN.md`"

**[AC4] `.claude/rules/ui-ux.md` 削薄为**：
- [ ] ≤30 行
- [ ] 仅保留 3-5 条不可妥协的硬规则
- [ ] 加指针到 `@DESIGN.md` 和 `@.impeccable.md`

**[AC5] 两套 `variables.css` 顶部加注释**：
- [ ] v3 的注释明确指向 DESIGN.md "v3 is master brand"
- [ ] admin 的注释明确"current Tailwind defaults pending rebrand to v3 master, see follow-up admin-rebrand-to-design-md"

**[AC6] S5 验证页面**（验证 .impeccable.md + DESIGN.md 实际效果）：
- [ ] 用 `/frontend-design @.impeccable.md @DESIGN.md` 让 AI 试做一个新页面（具体哪个页面 S2 决定）
- [ ] 跑 `/normalize` 验证它真的应用了 .impeccable.md（即对 hardcoded values 提出修复建议）
- [ ] 跑 `/design-review`，验证它真的读了 DESIGN.md（在 fix 中引用 DESIGN.md token）
- [ ] **此 task 不 commit 验证页面代码** —— 仅作为内部验证。正式的"建立后第一个真实新页面"作为独立 follow-up `frontend-design-system-validation`

**[AC7] manifest 状态**：
- [ ] frontend-design-system entry stage = completed
- [ ] artifacts.spec / artifacts.plan / artifacts.qa 全部填充
- [ ] follow_ups 中两个独立 feature 已创建为新 entry

### 4.3 边界情况

| 场景 | 处理方式 |
|------|---------|
| `/teach-impeccable` 访谈完后产出的 .impeccable.md 与 v3 现状冲突（如它推荐紫色但 v3 是绿色） | 显式告诉 teach-impeccable "v3 是 master"；如果还偏离，手工编辑 .impeccable.md 修正后再保存 |
| `/teach-impeccable` 卡在某个问题上（比如"目标用户是谁"你答不出来） | Pause and Ask user，可能需要回到 S0 重新定义产品定位（极小概率，但合法触发回退） |
| 薄 DESIGN.md 写完后发现某个数值（如 spacing scale）在 v3 和 admin 不一致 | v3 是 master，DESIGN.md 记录 v3 的值。admin 的偏离记录在 follow-up admin-rebrand 范围 |
| 验证页面 AC6 跑完后 `/normalize` 没有任何修复建议（说明它没读到 .impeccable.md） | **回退 S2 重新生成 .impeccable.md**（按 NDF Rule 6 的回退协议）。这是 audit-validated 后的小概率事件 |
| 验证页面 AC6 跑完后 `/normalize` 修复建议过多（说明 .impeccable.md 与 v3 实际代码偏离太大） | 检查偏离的具体类型：如果是品牌方向偏离 → 修 .impeccable.md；如果是历史代码遗留 → 接受偏离，记录到 admin-rebrand follow-up |
| `/teach-impeccable` 访谈过程超过 60 分钟未完成 | 暂停，分析为什么 —— 通常是问题设计太宽泛或用户思路不清。可能需要 Pause and Ask 缩窄问题范围 |
| 人类（你）在 S5 验收阶段发现 .impeccable.md 不能稳定 AI 输出（即与原计划承诺不符） | **Pause and Ask**。考虑是 .impeccable.md 内容质量问题（修内容） vs 工具机制问题（pivot 到 Option C 修改 skill 文件） vs 根本不可行（cancel 整个 feature） |

### 4.4 权限规则

**N/A** —— 文档型 feature，不涉及用户登录、会员等级、积分扣费、权限控制。所有改动是文档与配置注释，对 production 用户零影响。

### 4.5 UI 行为规格

**N/A** —— 文档型 feature，不涉及任何 UI 改动。本 feature 的"UI"是对 AI 的指令文件本身。

唯一例外是 S5 验证阶段的验证页面，但该页面**不 commit**，仅作为本 feature 的内部验证手段。正式的"建立后第一个真实新页面"是独立 follow-up。

---

## §5 S2-S7 各阶段预期任务（草案，待 S3 plan 详化）

### S2 spec
- **Task S2-1**: 跑 `/teach-impeccable`（30 min 访谈）→ 产出 `.impeccable.md` 草稿
- **Task S2-2**: 手写薄 DESIGN.md 大纲（30 min）→ 列出 7 个章节标题 + 每章节占位说明
- **Task S2-3**: 在 spec 文档中明确 .impeccable.md 与 DESIGN.md 的边界（哪个写什么、不写什么）
- **Task S2-4**: 决定是否新建 `frontend-workflow.md` 或合并到其他文件
- **Task S2-5**: 选定 S5 验证页面（从用户日常需求中挑一个未来要做的页面作为验证目标）
- **Spec 工件**：`numind-server/docs/superpowers/specs/2026-04-XX-frontend-design-system-design.md`

### S3 plan
- 把 spec 拆成 5-7 个原子 task
- 加入 NDF Rule 10 强制的"S5 验证策略"task：明确 .impeccable.md 与 DESIGN.md 各自的可证伪验证方法
- 加入"S2 spec 后立即跑独立 Sonnet reviewer 审 task 原子性 + S5 策略合理性"
- **Plan 工件**：`numind-server/docs/superpowers/plans/frontend-design-system-plan.md`

### S4 编码（文档生成）
- 按 plan 顺序逐 task 执行（用 subagent-driven-development 顺序，不并行）
- 每个 task 完成后两阶段 review（Spec Compliance + Code Quality，对象是文档质量）
- 每个 task 完成后更新 manifest progress

### S5 验收
- 用试做的新页面跑 `/frontend-design @.impeccable.md @DESIGN.md`
- 跑 `/normalize`、`/design-review`、`/audit`
- 写 QA 报告到 `docs/superpowers/qa/frontend-design-system-qa-report.md`
- 验证页面代码不 commit

### S6 人工验收
- 你 review 全部工件，确认：
  - `.impeccable.md` 是否真的捕捉到莫小派的品牌
  - 薄 DESIGN.md 是否准确
  - S5 验证是否说服你"未来 AI 输出会更稳定"

### S7 部署
- 文档型 feature 无生产部署
- commit 所有文档到 develop
- 创建两个 follow-up manifest entries: `frontend-design-system-validation` (Hotfix) 和 `admin-rebrand-to-design-md` (Standard)
- 写 Obsidian 功能笔记

---

## §6 Pivot 后被废弃的工件清单

为了保持工件历史的清晰，以下原计划被本 pivot 作废，**不再使用**：

| 原计划 | 被作废原因 |
|--------|-----------|
| `/design-consultation` 工具 | 产出 DESIGN.md 但 impeccable runtime 不读，浪费时间 |
| `getdesign` CLI 抓种子模板 | 同上，产出 DESIGN.md 格式 |
| `/design-shotgun` 视觉变体探索 | 产出视觉变体，但 .impeccable.md 不消费视觉，且 /teach-impeccable 访谈承担美学锚点角色 |
| `/plan-design-review` 作为 S3 reviewer | 设计师视角审 plan，但 plan 现在简单到 5-7 task，原子性审查由 NDF Rule 10 reviewer 足够 |
| `/critique` 量化评分作为 S5 验收基线 | A/B 控制实验已被用户 P4 拒绝，单点 critique 评分不能证明稳定性，且 NDF Rule 10 reviewer 会拒绝这种 demo path 测试 |
| 14 verb skills 在 frontend-workflow.md 列清单 | 工具数已减半，可能不需要单独的 frontend-workflow.md 文件，14 个 verbs 直接列在 ui-ux.md 末尾即可（待 S2 决定） |

---

## §7 S1 → S2 Gate 检查清单

| 检查项 | 状态 |
|--------|------|
| ✅ Proposal 文件存在 | 本文件 |
| ✅ §1 方案概述清晰可读（一人公司视角） | 是 |
| ✅ §2 工作量估算合理 | 是（4-5 hours 跨多 session） |
| ✅ §3 技术可行性含现有功能复用 + 风险 + 涉及仓库 | 是 |
| ✅ §3 标注 LLM 调用 | 否 (N/A) |
| ✅ §4 PRD 含用户故事 / 验收标准 / 边界情况 / 权限 / UI 规格 | 是 |
| ✅ 验收标准可证伪 | AC1-AC7 全部可二元判定 |
| ✅ Round 2 audit FAIL 已被吸收为 pivot | 是，新架构基于 audit 验证后的事实 |
| ✅ NDF Rule 10 (S5 验证策略) 已留 placeholder | 是，留给 S3 plan 拍板 |
| ✅ Manifest stage 与 artifacts 同步 | stage=S1, requirement + proposal artifacts 已填充 |
| ⏳ 用户确认 proposal | **PENDING — 等用户回复** |

---

## §8 用户确认请求（S1 → S2 硬门禁）

NDF S1 的 gate 是"客户确认提案"。一人公司"客户"= zhiyuchen 自己。请你 review 本 proposal 并确认或修改：

**关键确认点**：
1. **§1 方案概述** —— 是否准确捕捉了本 feature 要解决的问题和方案？
2. **§3.1 复用清单** —— 是否还有遗漏的现有功能没列入？
3. **§3.2 风险表 R1-R6** —— 风险是否有遗漏，缓解方案是否合理？
4. **§4.1 用户故事** —— 5 条用户故事是否覆盖了你想要的体验？
5. **§4.2 验收标准 AC1-AC7** —— 是否足够具体，能让 S5 阶段二元判定？
6. **§4.3 边界情况** —— 是否有未列出的场景需要补？
7. **§5 各阶段预期任务草案** —— 是否合理？S2 的 5 个 task 你接受吗？
8. **§6 废弃清单** —— pivot 后被作废的 6 项你接受吗？还是有哪个你想保留？

**特别需要你拍板的两件事**：

- **A. S5 验证页面候选**：本 proposal 留到 S2 决定。你心里有想法吗？建议挑一个**未来 1-2 周内本来就要做的页面**（比如某个新功能页面、某个 dashboard 改版），这样验证 + 实际工作合二为一，没浪费。
- **B. `.impeccable.md` 与薄 DESIGN.md 的边界**：本 proposal 提议 `.impeccable.md = 品牌叙事` + `DESIGN.md = 数值参考`。你认同这个分工吗？还是想让其中一个承担更多角色？

回复 **"S2 继续"** 进入下一阶段，或者告诉我哪些段落需要修改。
