# Agent 输出细节精修 — 实施计划（agent-output-refine, S3）

> 基于 spec `2026-06-16-agent-output-refine-design.md`。每 task 双 Sonnet review（spec + quality）。
> 按**文件归属**切 task，确保可并行/不冲突。

## 文件归属表（无交集 → 可并行）
```
T-server (numind-server)      : internal/numind/biz/agent/tool_image_gen.go (+ _test)
T-thinking (web)              : src/components/sales/ThinkingBlock.vue (+ __tests__/ThinkingBlock.spec.ts)
T-artifacts (web)             : src/utils/agentArtifacts.ts (+ __tests__/agentArtifacts.spec.ts)
                                src/components/agent/AgentArtifactItem.vue (+ spec)
T-finalanswer (web)           : src/components/agent/AgentFinalAnswer.vue (+ spec)   ← 依赖 T-artifacts 的 groupAdjacentImages 导出
T-toolcall (web)              : src/components/agent/AgentToolCallList.vue (+ spec)
T-question (web)              : src/components/agent/QuestionPrompt.vue (+ spec)
```
跨仓库（server vs web）Tier-2 天然并行。web 内 5 个 task 文件互不相交，唯一**代码依赖**：T-finalanswer import T-artifacts 的 `groupAdjacentImages` → T-artifacts 先于 T-finalanswer。其余无序。

## 依赖图
```
T-server      ── 独立（别的仓库）
T-thinking    ── 独立
T-artifacts   ──┐
                ├─→ T-finalanswer
T-toolcall    ── 独立
T-question    ── 独立
```

## T-server — 后端 #2b：image_gen 干净默认名
**仓库** numind-server
**改动**：`tool_image_gen.go:103` `gemini-image-%d.png`(time.Now().Unix()) → `image-YYYYMMDD-HHMMSS.png`（`time.Now().Format("20060102-150405")`，ASCII 日期式）。COS key 仍 sanitize（不变）。
**测试**：单测断言生成名匹配 `^image-\d{8}-\d{6}\.png$` 且不含 `gemini`（或 grep 守卫常量）。
**验收**：`go test ./internal/numind/biz/agent/...` 0；`task lint` 0。

## T-thinking — #1：ThinkingBlock chevron 方向
**仓库** web
**改动**：`ThinkingBlock.vue` import `ChevronRight`（替 `ChevronDown`）；`.thinking-icon` base rotate(0)=指右（折叠）；`.thinking-container:not(.collapsed) .thinking-icon` rotate(90deg)=指下（展开）。transition 不变。
**测试**：`ThinkingBlock.spec.ts` 更新——断言折叠态/展开态 icon 存在 + class 切换（`collapsed` ↔ 非）；若旧 spec 断言 ChevronDown 具体则改 ChevronRight。保留既有 autoCollapse/toggle 行为断言。
**验收**：vitest ThinkingBlock PASS；type-check 0；eslint 0。

## T-artifacts — #2a 显示名 + #3 图片分组逻辑/S2 卡
**仓库** web
**改动**：
- `agentArtifacts.ts`：
  - `artifactRefOf(isImageNode, text, url)` 新增 `text` 参数；`standaloneArtifactOf` 把 `m[2]`（link text/alt）传入；`extractArtifacts` 回调里同样传 `_text`。filename 取值：`const label = text?.trim(); return { filename: label || filenameOf(url) || \`artifact.${ext}\`, ... }`。
  - 新增导出 `RenderSegment` 类型 = `Segment | { type:'image-group'; refs: ArtifactRef[] }` 与 `groupAdjacentImages(segments: Segment[]): RenderSegment[]`：遍历，连续 image-mime artifact 段累积，遇非 image/非 artifact 段时——累积 ≥2 → push `image-group`，==1 → push 原 `artifact` 段，==0 跳过；其余段原样 push；收尾 flush。（image 判定：`ref.mime.startsWith('image/')`。）
- `AgentArtifactItem.vue`：图片分支 S2——去 `.artifact-item--image` 的 border/padding 外框，`.thumb` 给 `border-radius:--radius-md` + `box-shadow:--shadow-md` + `cursor:pointer`，caption 柔和置下；文件分支（A1）不动。
**测试**：`agentArtifacts.spec.ts` 加：①alt 优先（`![销售图](cos…png)`→filename「销售图」；`[报告.docx](cos…docx)`→「报告.docx」）；②空 alt 回退 filenameOf；③`groupAdjacentImages`：连续 3 图→1 个 image-group(refs.length 3)；单图→保持 artifact；图-文-图→两个独立 artifact（不分组）；图+doc→分别 artifact；doc 不进 image-group。`AgentArtifactItem.spec.ts` 更新 S2 结构（无外框 class / thumb 有 shadow）。
**验收**：vitest 全量 0 FAIL（splitIntoSegments 既有测试不回归）；type-check 0；eslint 0。

## T-finalanswer — #3 grid 渲染 + #4 标题分级 + #5 翠绿格式
**仓库** web（依赖 T-artifacts 的 `groupAdjacentImages` + `RenderSegment`）
**改动**：`AgentFinalAnswer.vue`：
- script：`renderSegments = computed(() => groupAdjacentImages(splitIntoSegments(props.markdown)))`，import `groupAdjacentImages`。
- 模板：v-for `renderSegments`：`prose`→markdown-body v-html（@click handleImageClick 保留）；`artifact`→`AgentArtifactItem`（S2/A1 自动按 mime）；`image-group`→`<div class="image-grid" @click="handleImageClick">` × `<figure>`(img.thumb + figcaption filename)，点击放大复用 `useImagePreview`。
- CSS #4：h1/h2 用 `--font-heading` 衬线；h1 21/h2 17.5/h3 15/h4 13.5；上间距递减；h4 text-secondary、加粗。
- CSS #5：blockquote 翠绿左条+ultra-soft 底+muted 字；table 表头 ultra-soft 底+primary-hover 字+翠绿淡分隔线；inline code 红→翠绿（primary-hover on ultra-soft）；`a`→accent-link+下划线；`.image-grid` 网格样式；修 `.ai-action-btn` hover stale 蓝 fallback→翠绿。`pre` 不动。
**测试**：`AgentFinalAnswer.spec.ts`：保留分段就地断言；加 image-group 渲染（多图→1 个 `.image-grid` 含 N 个 img；单图→不进 grid 走 AgentArtifactItem）；prose/doc 顺序不回归。
**验收**：vitest 全量 0；type-check 0；eslint 0。

## T-toolcall — #6：过滤 ask_user_question
**仓库** web
**改动**：`AgentToolCallList.vue`：`const visibleGroups = computed(() => props.toolGroups.filter(g => g.tool_name !== 'ask_user_question'))`；模板渲 `visibleGroups`，`v-if="visibleGroups.length > 0"`。props 改 `defineProps` 取变量（当前是裸 `defineProps<Props>()`）。
**测试**：`AgentToolCallList.spec.ts`（无则新建）：传含 1 个 ask_user_question + 1 个普通 group → 只渲普通；全 ask_user_question → 不渲 timeline（容器不出现）；普通 group 照常渲。
**验收**：vitest PASS；type-check 0；eslint 0。

## T-question — #7：answered 态 C3 统一
**仓库** web
**改动**：`QuestionPrompt.vue`：
- import `Check`（lucide）。
- answered 块加 avatar header（复用 `.question-prompt__avatar` 翠绿圆 + Check + 「已回答」文案），点击展开/收起（整行 toggle）；移除/合并旧重复「✓ 已回答」badge。
- `.question-prompt--answered` 背景改柔和翠绿家族（`--color-accent-ultra-soft` 底 + `--color-accent-soft` 边，比 asking 渐变更静）。
- `.question-prompt__answered-q` 用 `--font-heading` 衬线（对齐 C3 问题）。
- 保留 `answeredExpanded`/`displayAnswer`/Q-A 列表/全部既有类名与行为。
**测试**：`QuestionPrompt.spec.ts`：保留 answered 渲染/展开/displayAnswer 断言；加 avatar/「已回答」存在；selector 若变同步。asking 态 C3 行为不回归。
**验收**：vitest QuestionPrompt PASS；type-check 0；eslint 0。

## T-verify — S5 验证策略（Rule 10）
- **方式**：后端 Go 单测（image 名）+ 前端 vitest（6 组）+ vue-tsc + eslint + **dev browse 实跑**（S6 后）。
- **理由**：纯前端样式/逻辑，无 DB/API/支付；逻辑分支（分组/过滤/命名/分段）可单测持久回归，视觉精细度（箭头角度/字阶/翠绿浓淡/S2/M1/C3 观感）须真浏览器看。本地难复现完整 agent run（需 LLM+积分），主体走 dev browse。
- **关键路径**：agent 生成「多图+docx」的 run → 最终回答里 单图 S2 / 多图 M1 网格 / docx A1 卡、文件名可读、标题分级、表格引用翠绿；思考块箭头折叠右/展开下；问题答完——时间线无转圈行、answered 卡 C3 统一。
- **回归诚实声明**：vitest 覆盖逻辑与结构；视觉观感一次性 dev 确认，无自动回归（可接受，非支付/权限高风险）。

## 顺序
1. T-server（server 仓库，可与 web 并行）
2. web：T-artifacts → T-finalanswer（代码依赖）；T-thinking / T-toolcall / T-question 任意序穿插
3. 每 task 完成 → 双 Sonnet review → 修 P0/P1 → 下一个
4. 全绿 → S5 gate（全量 vitest + tsc + lint + go test）→ S6 ndf-done → /deploy-dev server 先于 web → dev browse 实跑

## 风险
- R1（alt 当名引入怪异内容）：仅文本插值非 v-html，无 XSS；空回退 filenameOf。测试覆盖。
- R2（groupAdjacentImages 破坏顺序/误并 doc）：纯函数充分单测（图-文-图不并、doc 不进组）。
- R3（#6 过滤误伤真工具）：只匹配精确 `ask_user_question` 串；普通 group 测试守护。
- R4（ThinkingBlock 全局共用，影响 sales/chatbot）：方向改为通用更优约定，视觉中性；spec 守护。
- R5（AgentFinalAnswer 同文件多 issue）：归并到单一 T-finalanswer task，串行实现，不并行写同文件。
