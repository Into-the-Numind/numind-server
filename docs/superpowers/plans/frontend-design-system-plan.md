# 前端设计体系建立 Implementation Plan (NDF S3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Each task uses checkbox (`- [ ]`) syntax for tracking. Per NDF Rule 6, every task gets two-phase Sonnet review (Spec Compliance + Code Quality) after completion. This is a **documentation-type feature** — "code quality review" applies to document quality (structure / consistency / executability), not source code.

**Goal**: 建立莫小派前端单一权威设计体系 = 由 `/teach-impeccable` 产出的 `.impeccable.md`（AI runtime enforcer）+ 200-300 行手写薄 `DESIGN.md`（人类可读 + design-review 校准）。v3 作为 master 品牌。

**Architecture**: 双文件无同步 —— `.impeccable.md` 是品牌叙事（impeccable 家族原生 4 章节模板），`DESIGN.md` 是数值参考（8 章节）。两文件关注不同层面，故意不同步内容。

**Tech Stack**: 文档型 feature。无代码改动（除注释 + CLAUDE.md 引用）。无数据库迁移。无 API。无 LLM 调用。

**Repos**: 根目录 `Codes/`（新建 .impeccable.md / DESIGN.md / 修改 CLAUDE.md / 修改 ui-ux.md）+ numind-web-v3（variables.css 注释 + CLAUDE.md 引用）+ numind-admin-web（variables.css 注释 + CLAUDE.md 引用）

**Spec**: `numind-server/docs/superpowers/specs/2026-04-10-frontend-design-system-design.md`

**Proposal**: `numind-server/proposals/frontend-design-system-proposal.md`

**Pivot 历史**：本 plan 基于 S1 R2 audit 后的 Option D 架构（拥抱 .impeccable.md 母语）。原 13 工具流水线已废弃。详见 `numind-server/proposals/runtime-skill-audit.md`。

---

## Task 拓扑总览

```
                    T1 /teach-impeccable
                          ↓
                    T2 写 DESIGN.md
                          ↓
            ┌─────────────┼─────────────┬─────────────┐
            ↓             ↓             ↓             ↓
       T3 CLAUDE.md  T4 ui-ux.md   T5 variables    T6 About 验证页
        (3 文件)      削薄          .css 注释         (内部，不 commit)
                                    (2 文件)
                                                      ↓
                                          T7 S5 验证策略 execution
                                          (NDF Rule 10 mandatory)
```

**总 task 数**：7
**依赖**：T1 → T2 → {T3, T4, T5, T6} → T7
**并行性**：T3/T4/T5 可并行（无相互依赖），但按 NDF S4 规则不并行 dispatch implementer，仍顺序执行
**预计编码时间**：约 3-4 小时跨多个 session

---

### Task 1: 跑 `/teach-impeccable` 产出 `.impeccable.md`

**Files:**
- Create: `Codes/.impeccable.md`（项目根目录）
- Modify: `Codes/CLAUDE.md`（teach-impeccable 末尾会问是否追加 Design Context 段，**回答否** —— 我们走专用引用方式，T3 task 处理）

**依赖**：无

**Step 1：访谈前准备**

- [ ] 阅读 spec §2.2 的 baseline 草稿，将以下内容作为访谈过程中的"我方答案 baseline"：
  - Users: 销售/运营/客户成功一线员工和管理者
  - Brand Personality: 专业可信 / 实干高效 / 有温度
  - Aesthetic Direction: 刊物气质 + 工业可靠（含翠绿 HSL + 衬线 heading 子段）
  - Design Principles: 5 条（数据密度优先 / 下一步明确 / 温度藏在细节 / 双端同品牌 / 不发明轮子）
- [ ] 准备 spec §2.3 reconcile 规则作为 v3 master 强约束：品牌色 `hsl(160, 72%, 40%)` / 字体 Georgia + 宋体 / 间距 T-shirt size / 组件命名

**Step 2：执行 /teach-impeccable**

- [ ] 调用 `/teach-impeccable` skill
- [ ] Phase 1（codebase 扫描）：让它自动读 v3 的 `variables.css`、`CLAUDE.md`、`src/components/`
- [ ] Phase 2（访谈）：用 baseline 答案回应每个问题。如果 teach-impeccable 推荐方向与 v3 master 冲突，按 spec §2.3 拒绝并修正
- [ ] Phase 3（写入 .impeccable.md）：让它写到项目根目录 `.impeccable.md`，不追加到 CLAUDE.md

**Step 3：访谈失败处理（spec §2.4）**

- [ ] 如果访谈卡住（case A）：用 baseline 占位 + 标注 `[BASELINE]`，记录到 manifest deferred
- [ ] 如果超过 60 分钟（case B）：强制暂停，用 baseline 直接 commit，记录 deferred
- [ ] 如果访谈结果与 baseline 偏差 >50%（case C）：**Pause and Ask** 用户判断是有意还是无意

**Step 4：验证 .impeccable.md 结构**

- [ ] 读取生成的 `.impeccable.md`，确认含 4 个章节（impeccable 原生模板，**不可加章节**）：
  1. `### Users`
  2. `### Brand Personality`
  3. `### Aesthetic Direction`（含 Color Direction 和 Typography Direction 子段，不必单独加 ### 标题）
  4. `### Design Principles`
- [ ] 确认翠绿 HSL 在 Color Direction 子段被描述（描述性表述，不含 hex）
- [ ] 确认 Georgia + 宋体在 Typography Direction 子段被描述
- [ ] 确认 5 条 Design Principles 与 spec §2.2.4 一致或合理变体

**验收条件 (Definition of Done)**：
- `.impeccable.md` 文件存在
- 含 impeccable 原生 4 章节（PRD AC1 满足，4 章节版本）
- v3 master 视觉语言被准确捕捉
- 文件长度 50-100 行
- baseline 偏差 ≤50% 或经用户确认接受偏差
- **`git diff Codes/CLAUDE.md` 为空** —— 验证 teach-impeccable 没有自动追加 Design Context 段（避免后续 T3 产生重复指针）

**Pause and Ask 触发条件**：
- spec §2.4 case C 偏差 >50%
- teach-impeccable 拒绝按 v3 master 修正（极小概率）

---

### Task 2: 手写薄 `DESIGN.md`（8 章节）

**Files:**
- Create: `Codes/DESIGN.md`（项目根目录）

**依赖**：T1 完成（DESIGN.md 中部分章节会引用 .impeccable.md 的品牌叙事作为 context）

- [ ] **Step 1: 读取 v3 variables.css**：作为 §2-§5 数值的来源
- [ ] **Step 2: 写 §1 品牌总览**（约 30 行）：参照 spec §3.2，含 master 决策表 5 行
- [ ] **Step 3: 写 §2 颜色 Tokens**（约 60 行）：完整 inline spec §3.3 的 5 个子表 (背景与表面 / 文本 / Sidebar / 品牌色与强调 / 边框与分割) + 状态色 gap 段落
- [ ] **Step 4: 写 §3 字体系统**（约 30 行）：参照 spec §3.4，含正确的 `--font-sans` / `--font-heading` / `--font-mono` token 名称 + 字号阶梯表
- [ ] **Step 5: 写 §4 间距系统**（约 25 行）：参照 spec §3.5，T-shirt size 命名 (xs/sm/md/lg/xl/2xl/3xl/4xl) + 数字索引兼容别名注释
- [ ] **Step 6: 写 §5 圆角与阴影**（约 30 行）：参照 spec §3.6，inline 完整 5 个 radius + 5 个 shadow 的 hex 值
- [ ] **Step 7: 写 §6 布局规则**（约 25 行）：参照 spec §3.7，用户端 vs 管理端布局差异表 + 响应式断点 + 主要硬规则
- [ ] **Step 8: 写 §7 组件清单**（约 50 行）：参照 spec §3.8，inline v3 完整 23 个组件 + admin 完整 10 个组件 + 关键 variants
- [ ] **Step 9: 写 §8 工具清单**（约 30 行）：参照 spec §3.9，runtime 6 个工具 + setup 1 个工具 + 14 个情境动词清单 + 已废弃工具备注

**验收条件**：
- `DESIGN.md` 文件存在于项目根目录
- 含 8 个 `## §` 章节（PRD AC2 满足，8 章节版本）
- 文件长度 200-300 行
- 所有 hex 值与 v3 variables.css 一致（可用 grep 校对）
- 状态色 gap 在 §2 末尾被明确标注
- 工具清单 §8 含已废弃的 5 个工具备注（design-consultation / getdesign / design-shotgun / plan-design-review / critique）

---

### Task 3: 更新三份 `CLAUDE.md`（根 + v3 + admin）

**Files:**
- Modify: `Codes/CLAUDE.md`（根目录）
- Modify: `numind-web-v3/CLAUDE.md`
- Modify: `numind-admin-web/CLAUDE.md`

**依赖**：T1 + T2 完成（引用的两个文件必须存在）

- [ ] **Step 1**: 编辑根 `CLAUDE.md`，按 spec §4.1，在 §3 通用硬规则的"必须做的事"列表末尾追加（**必须含 `§8 工具清单` 引用，否则 DoD 失败**）：
  ```markdown
  - 前端任务必读 `@.impeccable.md`（品牌叙事）和 `@DESIGN.md`（数值参考）；前端 UI 工具链详见 `DESIGN.md §8 工具清单`
  ```
- [ ] **Step 2**: 编辑 `numind-web-v3/CLAUDE.md`，按 spec §4.2，在 §1 技术栈声明 末尾追加：
  ```markdown
  - **设计语言**: 见根目录 `DESIGN.md`（v3 是 master 品牌）+ `.impeccable.md`（品牌叙事）
  ```
- [ ] **Step 3**: 编辑 `numind-admin-web/CLAUDE.md`，按 spec §4.3，在 §1 技术栈声明 末尾追加：
  ```markdown
  - **设计语言**: 见根目录 `DESIGN.md`（v3 是 master，admin 当前是 Tailwind 默认值，rebrand 计划见 follow-up `admin-rebrand-to-design-md`）+ `.impeccable.md`
  ```

**验收条件**：
- 三份 CLAUDE.md 全部含正确的设计语言指针
- 引用路径准确（`@.impeccable.md` 和 `@DESIGN.md`）
- §8 工具清单的章节号引用正确（已从 §7 更新为 §8）
- 不引入其他无关变更

---

### Task 4: 削薄 `.claude/rules/ui-ux.md` + 追加 14 个动词工具

**Files:**
- Modify: `Codes/.claude/rules/ui-ux.md`

**依赖**：T2 完成（削薄后保留的指针引用 DESIGN.md）

- [ ] **Step 1**: 读取当前 `ui-ux.md` 内容（约 50 行）
- [ ] **Step 2**: 识别要删除的内容（按 spec §5.1 删除清单）：CSS 变量列表 / 间距 8px 网格细节 / 字体层次 / 公共组件清单 / 表单细节模式
- [ ] **Step 3**: 识别要保留的硬规则（5 条不可妥协）：
  1. 管理端必须用 DataTable 表格布局
  2. 异步视图必须处理 4 状态
  3. 表单 blur 触发验证
  4. 销毁性操作必须确认 dialog
  5. 禁止使用外部 UI 框架
- [ ] **Step 4**: 重写 ui-ux.md，结构为：
  ```markdown
  # ui-ux.md (削薄版)

  > 详细设计 token 和组件清单见根目录 `@DESIGN.md`。AI 工作流见 `@.impeccable.md`。本文件仅保留**不可妥协的硬规则**。

  ## 硬规则（5 条）
  [5 条 hardcoded rules]

  ## 14 个情境性 impeccable 动词工具
  [按 spec §3.9 的情境层清单 inline 14 个动词 + 一句话触发场景]
  ```
- [ ] **Step 5**: 验证最终长度 ≤30 行（不含 14 个动词工具列表，工具列表追加在硬规则之后单独段落）

**验收条件**：
- 硬规则部分 ≤30 行（PRD AC4 满足）
- **整个 ui-ux.md 文件 ≤50 行（含 14-verb 列表）**，避免文件膨胀失去削薄意义
- 含 5 条硬规则
- 含指针段落引用 `@DESIGN.md` 和 `@.impeccable.md`
- 末尾含 14 个动词工具触发场景索引（每个动词 1 行，共约 14-15 行）
- 删除的内容不再出现（grep 验证：`CSS variable` / `8px` / `AppButton` 等关键词）

---

### Task 5: 给两套 `variables.css` 加注释 header

**Files:**
- Modify: `numind-web-v3/src/shared/styles/variables.css`
- Modify: `numind-admin-web/src/styles/variables.css`

**依赖**：T2 完成（注释中引用 DESIGN.md）

- [ ] **Step 1**: 编辑 v3 `variables.css`，在文件最顶部插入 spec §6.1 定义的注释 header（约 14 行注释块），保留原有内容不变
- [ ] **Step 2**: 编辑 admin `variables.css`，在文件最顶部插入 spec §6.2 定义的注释 header（约 12 行注释块），保留原有内容不变
- [ ] **Step 3**: 验证两份文件的 CSS 仍然合法（注释不破坏 `:root { ... }` 语法）

**验收条件**：
- v3 注释含 "SOURCE OF TRUTH for brand tokens (v3 = master)"
- admin 注释含 "CURRENT STATE: Tailwind defaults. NOT yet rebranded"
- admin 注释含 follow-up feature 名称 `admin-rebrand-to-design-md`
- 两份 variables.css 的实际值未改动（grep 验证 `--primary` 等 token 值不变）
- (PRD AC5 满足)

---

### Task 6: 构建 "About / 关于莫小派" 验证页面

**Files:**
- Create: `numind-web-v3/src/views/AboutValidation.vue`（**不进 router，不 commit 到 develop**）

**依赖**：T1 + T2 完成（验证页面使用 .impeccable.md 和 DESIGN.md）

**注**：本 task 的产物**仅作为 T7 的内部验证手段**。不进 router 路由表，不 commit 到 develop 分支，不写测试，不与现有页面集成。完整的"建立后第一个真实新页面"作为 follow-up `frontend-design-system-validation` 走 Hotfix 流程并 commit。

- [ ] **Step 1**: 给 AI 一个明确 prompt：
  > "用 `@.impeccable.md` 和 `@DESIGN.md` 给莫小派写一个'关于莫小派 / About'静态展示页。要求：单页 Vue 文件，符合品牌叙事的版式选择，使用 DESIGN.md 定义的 token，不依赖外部 UI 框架"
- [ ] **Step 2**: 让 AI 生成 `AboutValidation.vue`（约 150 行）
- [ ] **Step 3**: 不修改 router，不 import 到任何现有页面
- [ ] **Step 4**: 用 Vite 构建检查（`npm run build` 不强制，类型检查 `npm run type-check` 必跑）—— 验证文件至少能编译

**验收条件（独立可验证，不依赖 T7）**：
- `AboutValidation.vue` 文件存在
- 单文件 Vue (`<template>` + `<script setup lang="ts">` + `<style scoped>`)，无 router 注册，无 import 引用其他业务代码
- 文件**使用 CSS 自定义属性** (`var(--*)`) 进行样式 —— grep 至少 5 次出现
- 文件**不**含外部 UI 框架引用（grep 无 `element-plus` / `ant-design` / `@vant` 等）
- `npm run type-check` 通过（无 TS 错误）
- 文件不在 git index 中（`git status` 显示为 untracked）—— 确认不会被 commit

**注意**：本 task 完成后**不 commit**。这是文档型 feature 的一个特殊任务。两阶段 review 仍正常执行（review 对象是文件的"validation 准备度"而不是代码质量）。

**NDF Rule 8 例外声明**：本 task **不 commit**，因此 Rule 8 commit-verification 不适用。T7 的 QA report commit 作为本 task 的工件持久化记录（QA report 中会引用 `AboutValidation.vue` 的 git status 作为 evidence）。

---

### Task 7: S5 验证策略 execution（NDF Rule 10 强制 task）

**Files:**
- Create: `numind-server/docs/superpowers/qa/frontend-design-system-qa-report.md`

**依赖**：T6 完成（需要验证页面存在）

> **NDF Rule 10 mandatory**：本 task 是 S3 plan 中**必须存在**的"S5 验证策略" task，独立 reviewer 一并审查其合理性。

**S5 验证方式**：基于 spec §7.2 定义的 4 个二元判定维度。**不**采用 A/B 控制实验（用户在 R1 P4 显式拒绝）。

**为什么这个验证策略合理（reviewer 审查重点）**：
- 二元判定避免了"vibe-based pass" —— 每个维度有 grep 触发条件或数字门槛
- 4 维度覆盖了 .impeccable.md（维度 a）和 DESIGN.md（维度 b/d）和 runtime tools（维度 c）
- 反例驱动：维度 c 故意引入 polluted 代码测试 normalize 是否真的能识别和修复
- 单点验证的局限被诚实记录在 spec §7.2 末尾：维度 a 仍含部分主观元素，但 5 个二元 FAIL 触发条件已大幅减少主观空间
- 不采用 A/B 实验的代价（无变量控制）已被用户明示接受

**关键路径**：

- [ ] **Step 1：执行维度 a（版式选择）**
  - [ ] grep `AboutValidation.vue` 检查 5 个二元 FAIL 触发条件：
    - **(1) sans-serif 在 h1-h6**：grep `font-family.*sans` 在 heading 元素或 heading class 选择器内
    - **(2) 卡片网格**：grep `display: grid` 和 `grid-template-columns: repeat` 同时出现
    - **(3) 区块间距过密**：grep `margin.*var(--space-(xs|sm|md|lg))` **仅检查 outer block 容器**（class 名含 `section` / `block` / `container` / `wrapper` / `page` 等顶层布局元素），不检查内部子元素的 padding。避免对合理的内部紧凑间距误报
    - **(4) emoji 装饰**：grep emoji 多 Unicode range（覆盖常见 emoji 集合）：
      ```
      [\u{1F300}-\u{1F9FF}]   # Symbols & Pictographs
      [\u{2600}-\u{27BF}]     # Miscellaneous Symbols & Dingbats
      [\u{1F000}-\u{1F0FF}]   # Mahjong / Dominoes
      [\u{1FA70}-\u{1FAFF}]   # Symbols and Pictographs Extended-A
      ```
      任一范围内字符出现于模板部分 → FAIL
    - **(5) 非翠绿系主色**：grep `color: #[^F]` 排除翠绿/中性灰系，扫到非翠绿色调主色 → FAIL
  - [ ] 任一触发 → 维度 a FAIL，记录 FAIL 类型

- [ ] **Step 2：执行维度 b（token 引用率）**
  - [ ] 统计 `AboutValidation.vue` 中 `var(--*)` 引用次数 = X
  - [ ] 统计同文件中 hex 字面量数量（`#[0-9A-Fa-f]{3,8}`）= Y
  - [ ] 计算引用率 = X / (X + Y)
  - [ ] 引用率 ≥ 80% → PASS，否则 FAIL

- [ ] **Step 3：执行维度 c（normalize 修复能力）**
  - [ ] 创建 `AboutValidation.vue` 的 polluted 副本：硬编码 `#4F46E5` + `padding: 13px` + `font-family: Inter, sans-serif`
  - [ ] 运行 `/normalize` skill on the polluted file，**调用时显式 prompt 含 `@.impeccable.md` 引用**（`/normalize @.impeccable.md AboutValidation-polluted.vue` 或等效形式），确保 normalize 在 .impeccable.md context 下工作。否则可能因 context 缺失而产生空建议，PASS 也无意义
  - [ ] 检查 normalize 输出是否含至少 3 类修复建议：
    - 紫蓝 → 翠绿
    - 13px → token (`--space-md` 或 `--space-lg`)
    - Inter → Georgia
  - [ ] 含 ≥3 类 → PASS，否则 FAIL

- [ ] **Step 4：执行维度 d（design-review 引用）**
  - [ ] 运行 `/design-review` skill on `AboutValidation.vue`
  - [ ] **强化判定**：文本搜索其输出，必须出现以下两类引用之一：
    - **a.** 引用 DESIGN.md 中定义的具体 token 名（如 `--primary` / `--accent` / `--font-heading` / `--space-xl`）
    - **b.** 引用 DESIGN.md 中的具体 hex/HSL 值（如 `hsl(160, 72%, 40%)` / `#1A1D26`）
    - **c.** 引用 DESIGN.md 的具体章节号（如 "DESIGN.md §2 颜色 Tokens" / "§3 字体系统"）
  - [ ] **拒绝**仅含 "I checked DESIGN.md" 这种 boilerplate 提及（必须证明 design-review 真的读了内容）
  - [ ] 任一类型引用出现 ≥1 次 → PASS，否则 FAIL

- [ ] **Step 5：写 QA 报告**
  - [ ] 使用 `templates/ndf/qa-report.md` 模板
  - [ ] 记录 4 维度的 PASS/FAIL 状态 + 具体证据
  - [ ] 整体通过门槛：4/4 PASS → 整体 PASS；任一 FAIL → 整体 FAIL，按 NDF Rule 6 回退协议处理
  - [ ] 写到 `numind-server/docs/superpowers/qa/frontend-design-system-qa-report.md`

- [ ] **Step 6：清理验证页面**
  - [ ] 验证完成后，确认 `AboutValidation.vue` 仍未 commit（`git status` 检查）
  - [ ] 不删除文件 —— 留作 follow-up `frontend-design-system-validation` 的起点
  - [ ] 在 manifest 的 follow-up 备注中记录 "AboutValidation.vue 已存在于 v3，作为 follow-up feature 的起点代码"

**验收条件**：
- QA 报告文件存在于 `numind-server/docs/superpowers/qa/`
- 报告含 4 维度的 binary PASS/FAIL 判定 + 证据
- 整体 PASS（4/4）→ S5 阶段通过；否则触发 NDF 回退（spec §10 + edge case 处理）
- (PRD AC6 满足)

**Pause and Ask 触发条件**：
- 整体 FAIL → 必须 Pause and Ask 用户判断回退到 S2（修订 spec）/ S3（修订 plan）/ cancel feature

---

## 全 Plan 验收（S5 阶段执行）

### PRD AC 与 Task 映射

| PRD AC | 对应 Task | 完成条件 |
|--------|-----------|---------|
| AC1 | T1 | .impeccable.md 4 章节 + v3 master 视觉准确捕捉 |
| AC2 | T2 | DESIGN.md 8 章节 + 完整 token + 状态色 gap 标注 |
| AC3 | T3 | 三份 CLAUDE.md 含设计语言指针 |
| AC4 | T4 | ui-ux.md 削薄 ≤30 行 + 5 硬规则 |
| AC5 | T5 | 两套 variables.css 含注释 header |
| AC6 | T6 + T7 | About 验证页 + S5 4 维度验证报告 |
| AC7 | (manifest 自动) | T1-T7 各 task 完成后更新 manifest progress |

### 不在本 plan 内的工作

- **admin 实际 token 重画** → follow-up `admin-rebrand-to-design-md`
- **第一个真实新 commit 页面** → follow-up `frontend-design-system-validation`（基于 T6 的 AboutValidation.vue 起点）
- **状态色 token 引入到 v3** → admin-rebrand follow-up 范围

---

## Plan 元数据

**Total Tasks**: 7
**Atomic 验证**: 每个 task 都可独立构建和验证（per NDF Rule 9）
**依赖图**: T1 → T2 → {T3, T4, T5, T6} → T7 (无环)
**S5 验证策略 task**: T7（per NDF Rule 10）
**预计编码时间**: 3-4 小时跨多 session
**预计 review 时间**: 7 task × 两阶段 review × ~5 min = 70 min

**Spec 引用一致性核对**：
- spec §2.1 → T1 (impeccable 4 章节)
- spec §2.4 → T1 失败处理
- spec §3.1-§3.9 → T2 (8 章节)
- spec §4.1-§4.3 → T3 (三份 CLAUDE.md)
- spec §5.1 → T4 (ui-ux.md 削薄)
- spec §6.1-§6.2 → T5 (variables.css 注释)
- spec §7.1-§7.3 → T6 + T7 (About 页 + 4 维度验证)
- spec §10.1 → S2 gate 已确认（不创建 frontend-workflow.md / AC1AC2 同步 / 状态色 gap / About 页选定）
