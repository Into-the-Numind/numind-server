# 前端设计体系建立

> ## ⚠️ S1 Round 2 Architectural Pivot (2026-04-10)
>
> **本 requirement card 的"工具编排策略"段落已被 S1 Round 2 audit 发现的根本架构问题作废**。
>
> **Audit 发现**：原 13 工具流水线建立在一个错误假设之上 —— 我们以为 impeccable 家族的 runtime 工具（normalize/audit/frontend-design/polish/harden）会读 DESIGN.md。实际上 5/6 完全不读 DESIGN.md，它们读的是 `.impeccable.md`（impeccable 家族的母语）。详见 `numind-server/proposals/runtime-skill-audit.md`。
>
> **Pivot**：选定 **Option D** —— 拥抱 `.impeccable.md` 作为 AI runtime enforcer，保留 200-300 行薄 DESIGN.md 给 `/design-review` 校准 + 人类可读。setup 时间从 2.5-3.5h 砍到 1.5h。
>
> **新架构**：
> - **Enforcer 文件**：`.impeccable.md`（由 `/teach-impeccable` 一次访谈产出）—— AI runtime 真正读这个
> - **Documentation 文件**：薄 `DESIGN.md`（手写 200-300 行）—— 人类可读 + design-review 校准
> - **Setup 工具**（精简）：`/teach-impeccable`（唯一一个）
> - **Runtime 工具**：`/frontend-design` + `/normalize` + `/polish` + `/harden` + `/audit` + `/design-review`
> - **废弃**：`/design-consultation`、`getdesign` CLI、`/design-shotgun`、`/plan-design-review` —— 它们产出/消费 DESIGN.md 但与 impeccable 家族不咬合，强行用是齿轮塞错传动装置
>
> **下游工件**：本 requirement card 下方的"工具编排策略"和"Deliverable 范围"段落仅作为**历史记录**保留。新的执行计划见 Round 3 proposal：`numind-server/proposals/frontend-design-system-proposal.md`。
>
> ---

## 来源
- 提出人：zhiyuchen（产品负责人 / 一人公司创始人）
- 提出日期：2026-04-10
- 利益相关方：zhiyuchen（唯一所有者，同时是 PM / 设计 / 工程审核者）

## 需求描述

> 用户原话："我需要形成一个规范化、体系化的前端规范，以致于我在开发新的功能和新的页面的时候，能够很好的规范 AI，拿到我想要的效果。"

当前莫小派项目的前端设计规范散落在 7+ 个位置（`.claude/rules/ui-ux.md`、两套 `variables.css`、两套 `main.css`、两个仓库的 `CLAUDE.md`、组件实现本身），存在三个根本问题：

1. **没有单一权威来源 (Single Source of Truth)** —— AI 开发新页面时不知道该读哪个，导致输出不稳定
2. **用户端 (numind-web-v3) 与管理端 (numind-admin-web) 已经分裂为两套不同的设计系统**（不是漂移，是分裂）：
   - **v3**：翠绿主色 `hsl(160, 72%, 40%)`，HSL 语义命名（`--accent-soft/--accent-link`），衬线 heading 字体（Georgia + 宋体），有 gradient bg 和多层 shadow，明显有定制设计思考
   - **admin**：靛蓝主色 `#4F46E5`（Tailwind `indigo-600` 默认值），数字阶梯命名（`--primary-50/100/200`），灰阶完全是 Tailwind `neutral-*` 原样，几乎无品牌定制
   - 两套品牌色是不同色相，不可能"自然合并"，必须显式选 master
3. **现有规则是碎片化条文，缺乏决策依据** —— 例如 `ui-ux.md` 说"用 8px 网格"但未说明"何时用 16px / 何时用 24px"，AI 缺锚点只能瞎猜

## 业务目标

建立一份 **`DESIGN.md`（Google Stitch 风格的 LLM 原生设计文档）作为整个莫小派前端的单一权威源**，**并配套一条可运行时强制执行的开发工作流**，让：

- **AI 开发新页面时**：只需 `@DESIGN.md` 即可获得完整设计语言，输出与现有页面气质一致的代码
- **AI 输出后**：通过 `/normalize` + `/design-review` 等 runtime 工具自动对齐 / 修复偏离 DESIGN.md 的代码（**这是稳定性的真正保障 —— 文档存在 ≠ 文档生效**）
- **人类开发者**：有一份可阅读、可 review、可演进的活文档
- **未来 6-12 个月的所有前端工作**：站在统一的设计语言之上，避免越走越散

## 可证伪验收标准

业务目标的达成度必须可测量。具体的验收 bar 由 S3 plan 阶段的"S5 验证策略"task 确定（NDF Rule 10 强制要求），但本 requirement card 预先约束 bar 的形态：

| 验收项 | 测量方式 | 最低门槛（具体数值由 S3 拍板） |
|--------|----------|--------------------------------|
| **AI 输出符合度** | 让 AI 用 `@DESIGN.md` 生成一个新页面 → 跑 `/critique` 持人评分 | 总分 ≥ X/10（X 在 S3 定） |
| **runtime 修复有效性** | 故意让 AI 生成偏离 DESIGN.md 的页面 → 跑 `/normalize` → 对比修复前后 | normalize 必须修复 ≥ N 类偏离（N 在 S3 定） |
| **质量综合检查** | 跑 `/audit` | P0 = 0，P1 ≤ 阈值 |
| **runtime skill 兼容性** | S1 task 验证：每个 runtime skill 的 SKILL.md 必须明确把 `@DESIGN.md` 当 source of truth，不能只当 supplement | 全部通过 |

## 优先级

**高** —— 这是基础设施类需求。当前所有未来的前端开发都受其约束。越早建立，节省的 AI 反复"猜测设计意图"的成本越多；越晚建立，已经写入代码的不一致就越多，纠偏成本越高。

## Triage

- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：**否**
  2. 新增 API 端点：**否**
  3. 新外部服务集成：**否**（本地 skill 调用为主；`getdesign` CLI 走 https://getdesign.md 一次性 fetch 模板，仅 setup 阶段使用，非长期外部依赖）
  4. 影响文件数：**>3**（新建 DESIGN.md + frontend-workflow.md + 修改两个仓库的 CLAUDE.md + 修改 .claude/rules/ui-ux.md + 调整两套 variables.css 注释指针 + 根 CLAUDE.md）
  5. 高风险业务逻辑（支付/权限）：**否**
- **风险性质（精确表述）**：对线上产品零 blast radius（不动代码、不动 DB、不动 API），但**对未来开发流程高 blast radius** —— DESIGN.md 一旦定型会塑造未来 6-12 个月的所有前端代码。`git revert` 能撤回文档，但**不能撤回已经基于它生成的页面代码**。事实上半不可逆 → 升级为 Standard 以获得 review gate
- 人类决定：**确认 Standard**（2026-04-10，zhiyuchen 在 Triage 阶段确认）

## 关键约束（用户已拍板）

### C1：v3 作为 master 品牌，DESIGN.md 真合一
**来源**：用户在 S0 review 阶段确认 (2026-04-10)，从原始的"合一份共享 tokens"修订为本条。

**v3 赢的依据**：
- v3 面向**付费用户**，admin 仅供内部员工使用 → 品牌一致性应跟随 user-facing
- v3 已有明显设计思考：翠绿 HSL + 衬线 Georgia + 宋体 + gradient bg + 多层 shadow + 语义命名
- admin 几乎是 Tailwind 默认值原样，无品牌定制
- v3 内部已经在做一次系统切换（保留了 `--color-*` 兼容别名），正好顺势并入新体系
- v3 的衬线 heading 暗示了"刊物/编辑/有温度"的产品气质，这是莫小派的真实品牌方向

**含义**：
- DESIGN.md 完全以 v3 当前的视觉语言为蓝本（翠绿、HSL 命名、衬线、gradient）
- admin 的 Tailwind 默认值在 DESIGN.md 中**不被承认为合法状态**，必须 rebrand
- admin rebrand 是**代码改动**，**不在本 NDF feature 范围内**，作为独立 follow-up feature `admin-rebrand-to-design-md` 走自己的 NDF 周期（见下文 Follow-up Features 段落）

### C2：美学锚点未定，预设 Plan A 和 Plan B
**Plan A（首选）**：S2 阶段执行 `/teach-impeccable` 访谈 → `/design-shotgun` 生成 3-6 个视觉变体板 → 用户视觉选定锚点 → `npx getdesign add <choice>` 抓种子

**Plan B（escalation path，用户对 Plan A 输出不满时按序尝试）**：
1. **B1**：循环 `/design-shotgun`，基于上一轮反馈精化约束（最多 2 次）
2. **B2**：放弃外部锚点，**直接以 v3 当前视觉语言为锚点**（v3 既然是 master，本身就是合法锚点）
3. **B3**：从 `getdesign.md` 的 59 个现成模板中人工挑选最接近的

S2 阶段必须明确记录走的是 Plan A 还是哪一级 Plan B，写入 manifest decisions。

### C3：Standard Track 全流程 + 文档型 task 适配
走 NDF Standard Track S0→S7，但因为是文档生成型功能：
- S4 task 定义为"文档生成步骤"而非"代码 task"
- 每个 task 仍按 NDF Rule 6 走两阶段 review（Spec Compliance + Code Quality），review 对象是文档质量（结构完整性、内部一致性、可执行性）而非代码
- S5 验收的对象是"DESIGN.md 是否真的能让 AI 输出更稳定"，不是"代码是否运行"

## 工具编排策略（待 S1 proposal 详化）

impeccable 系列是**两层架构**，本次需要两层都覆盖。原始方案只覆盖了 Setup 层（4 工具），是不完整的；扩展后共 13 个工具进入流水线 + 14 个情境性微调动词独立成清单（位置见 Deliverable §3）。

### Setup 层（本次 NDF S0-S7 一次性执行）

| 阶段 | 工具 | 角色 |
|------|------|------|
| S2 spec | **`/teach-impeccable`** | 灵魂层访谈 + 读现有 v3 代码，蒸馏"我们是谁、为谁做、想要什么感觉" |
| S2 spec | **`/design-shotgun`** | 基于灵魂访谈生成 3-6 个视觉变体板供人选择 —— **直接解决 C2 美学锚点未定问题**；如失败按 C2 Plan B 升级 |
| S2 spec | **`getdesign` CLI** | 用户选定锚点后，`npx getdesign add <choice>` 抓取种子 DESIGN.md（一次性 fetch，非长期依赖） |
| S2 spec | **现状提取**（AI 直接读） | 读 v3 `variables.css`（master）+ admin `variables.css`（标注偏离）+ 现有组件 + `ui-ux.md`，标注事实和漂移点 |
| S2 spec | **`/design-consultation`** | 合成最终 DESIGN.md，以 v3 为视觉语言基础 |
| S3 plan | **`/plan-design-review`** | 设计师视角审 DESIGN.md 大纲 0-10 评分。**与 NDF Rule 6 的 Sonnet reviewer 并列（不替代）**：Sonnet reviewer 审 plan 原子性 + S5 验证策略合理性，`/plan-design-review` 审设计维度覆盖度。两者是不同角色 |
| S5 验收 | **`/critique`** | 量化评分 + persona 测试，分数作为验收基线（具体阈值由 S3 拍板） |
| S5 验收 | **`/design-review`** | 自动识别漂移并修复 commit |
| S5 验收 | **`/audit`** | a11y + 性能 + 主题 + 响应式综合 P0-P3 报告 |

### Runtime 层（DESIGN.md 上线后日常开发持续使用）

| 时机 | 工具 | 角色 |
|------|------|------|
| 新建页面 | **`/frontend-design`** + `@DESIGN.md` | 主力生成器 |
| 生成后立即 | **`/normalize`** ⭐ | 把已写代码对齐到 DESIGN.md 的 tokens 和间距 —— **稳定性的核心保障** |
| 上线前 | **`/polish`** | 最后微调（对齐/间距/细节） |
| 上线前 | **`/harden`** | 边界态/i18n/溢出处理 |
| 上线前 | **`/design-review`** | 设计师视角自动 QA + commit |
| 周期性 | **`/audit`** | 综合质量 P0-P3 报告 |
| 决策辅助 | **`/ui-ux-pro-max`** | 50+ 风格 / 161 调色板 / 99 UX 准则查询库（不写文件） |

⚠️ **未验证假设**：以上 6 个 runtime skill 是否真的把 `@DESIGN.md` 当 source of truth，目前**未读过它们的 SKILL.md**。S1 proposal 必须包含一个明确的 audit task：读取每个 skill 的 SKILL.md，验证它们对 DESIGN.md 的实际尊重程度。如果发现某个 skill 只是"参考"而非"遵循"DESIGN.md，本 feature 的 runtime 层就是空头支票，必须重新设计或替换该 skill。

### 情境层（位置见 Deliverable §3，不在 DESIGN.md 内）

14 个动词技能：`/animate` `/delight` `/distill` `/clarify` `/arrange` `/typeset` `/colorize` `/bolder` `/quieter` `/overdrive` `/extract` `/adapt` `/onboard` `/optimize`

不进固定流水线，作为运行时按需调用工具。**清单位置已从 DESIGN.md 改为 `frontend-workflow.md`**，原因：DESIGN.md 应保持纯设计语言，工具清单随 gstack 升级会变化，放进 DESIGN.md 会污染并加速文档腐烂。

## Deliverable 范围

本次 feature 的产出**全部是文档**（含极少量配置注释）。任何代码改动都拆为独立 follow-up feature：

1. **`/DESIGN.md`** —— 项目根目录，单一权威设计文档（Google Stitch / awesome-design-md 生态约定位置）
   - 以 v3 视觉语言为蓝本（翠绿、HSL 命名、衬线 heading、多层 shadow）
   - 含组件 API、布局原则、间距系统等结构性章节
   - **不含 14 verb skill 工具清单**（移出，见下方第 3 项）

2. **根 `CLAUDE.md` 更新** —— 加一句"前端任务必读 `@DESIGN.md`"，让 AI 默认加载

3. **`.claude/rules/frontend-workflow.md`**（新建）—— 前端开发工作流文档，明确：
   - Setup 层工具的使用边界（一次性，不在日常）
   - Runtime 层 6 个工具的标准触发顺序（生成 → normalize → polish/harden → design-review）
   - 情境层 14 个动词技能的触发场景索引
   - **Tool Map 章节**也在此文件，不在 DESIGN.md（避免文档腐烂）

4. **`.claude/rules/ui-ux.md` 削薄** —— 改为只保留 3-5 条不可妥协的硬规则 + 指针到 `@DESIGN.md`

5. **两套 `variables.css` 加注释指针** —— 明确 "Source of truth: `/DESIGN.md` §2"，防止未来漂移。**仅加注释，不改值**（admin 的实际 token 改值由 follow-up feature 完成）

⚠️ **本 feature 不包含**：S5 验证页面的代码、admin 的 token rebrand 代码改动。这些拆为独立 follow-up（见下）。

## Follow-up Features（独立 NDF 周期）

本次 feature 完成后将立即创建以下条目：

### `frontend-design-system-validation`（验证页面）
- **目的**：用本次建立的 DESIGN.md + frontend-workflow.md 试做一个真实新页面，作为 DESIGN.md 实际效果的验证
- **轨道**：Hotfix（单页面 UI 改动）
- **依赖**：本 feature 完成
- **作用**：S5 验收阶段会**先在本 feature 内部完成一次验证测试**（用临时页面，不 commit），但**正式的"建立后第一个真实页面"作为独立 follow-up 走 Hotfix 流程并 commit**，便于追踪和单独 review
- **页面候选**：S2 阶段从用户日常需求中挑选

### `admin-rebrand-to-design-md`（管理端品牌迁移）
- **目的**：把 admin 的 Tailwind 默认值替换为 DESIGN.md 定义的 v3 master 品牌（翠绿 + HSL 命名 + 衬线 heading）
- **轨道**：Standard（多文件代码改动，影响 admin 所有 view）
- **依赖**：本 feature 完成
- **范围**：admin 全部 30+ view 视觉重画，需走 NDF S0-S7 完整流程
- **rollback 难度**：高（视觉影响所有页面），因此独立 manifest entry 便于追踪

## 备注

- 这是莫小派 NDF 流程下第一次跑"文档型 Standard 功能"，过程本身也是对 NDF 文档型适配能力的验证。如果顺利，可作为未来同类需求的范本
- **Rollback 现实评估**：`git revert` 能撤回 DESIGN.md 等文档 commit，但**不能撤回已经基于 DESIGN.md 生成的下游页面代码**。一旦本 feature 上线超过若干 PR 周期，事实上不可逆。这是接受 Standard 轨道审查门的核心理由
- **历史**：原始方案 4 工具 → 用户在 S0 阶段提醒 impeccable 系列还有大量未用工具 → 扩展为 13 工具进入流水线 + 14 个情境性动词独立清单 → reviewer 发现两套 variables.css 是分裂而非漂移 → C1 从"合一共享 tokens"修订为"v3 master，admin 后续 rebrand"→ 加入 S5 验证策略占位 + C2 Plan B + 风险措辞修正等 11 项 review 修复
