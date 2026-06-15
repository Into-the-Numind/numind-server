# Agent 输出细节精修 — 设计（agent-output-refine, S1+S2）

> NDF Standard S1+S2 合并。来源：User dev 实跑 agent-output-redesign 后 7 条反馈。
> playground：`docs/numind-image-playground.html`（User 选 单图=S2、多图=M1）。
> 后端仅 1 处命名改动；其余全在 numind-web-v3 前端。

## 1. 问题 → 根因 → 方案（7 项）

### #1 思考块箭头方向
- **现状**：`src/components/sales/ThinkingBlock.vue` 用 `ChevronDown`，collapsed→rotate(0)=指下、展开→rotate(180)=指上。
- **目标**：折叠→指右 ▶，展开→指下 ▼（标准 disclosure 约定）。
- **方案**：图标换 `ChevronRight`；base rotate(0)=指右（折叠态）、`:not(.collapsed)` rotate(90deg)=指下（展开态）。组件被 sales/chatbot/agent 共用，新方向是通用更优约定，全局生效。

### #2 文件/图片命名丑
- **现状**：前端 `agentArtifacts.ts` 用 `filenameOf(url)`（COS object key 末段，中文被 sanitize 成下划线，如 `..py-______.docx`）当显示名；image_gen 默认名 `gemini-image-{unix}.png`。但 markdown 节点的**链接文字/alt**（`![销售漏斗图](url)` / `[报告.docx](url)`）其实是 LLM 写的可读名，没被用。
- **方案 A（前端，主）**：`artifactRefOf` / `standaloneArtifactOf` / `extractArtifacts` 取 NODE_RE group-2（链接文字/alt）trim 后非空则作 `filename`，否则回退 `filenameOf(url)`。`fileTypeLabel`（A1）已有「无扩展名→按 mime 推 DOCX/PDF」回退，无名时仍正确。`a.download` 跨域被浏览器忽略（仅新标签打开），丢扩展名无害。
- **方案 B（后端，兜底）**：image_gen 默认名改 `image-YYYYMMDD-HHMMSS.png`（ASCII 日期式，过 sanitize 不变形），替代巨型 unix 时间戳。当 LLM 没写 alt 时回退名也可读。COS object key 仍走 sanitize 不变。

### #3 图片大外框 → 单图 S2 / 多图 M1
- **现状**：`AgentArtifactItem` 图片走 `.artifact-item--image`（padding+border+shadow 外框）；多图各自成段、各自一个外框，零散。
- **单图 S2**：去外框（无 border/padding 卡片壳），图片本身圆角 + 轻阴影（`--shadow-md`）+ 点击放大（复用现有 modal），caption 柔和置于下方。
- **多图 M1**：连续图片产物合并为一个**自适应网格**（`repeat(auto-fill,minmax(150px,1fr))`，gap 10px，max-width ~560px），点任意张放大。
- **方案**：`agentArtifacts.ts` 新增 `groupAdjacentImages(segments): RenderSegment[]`——把**连续 ≥2 个** image artifact 段并成 `{type:'image-group',refs}`；单个 image 段保持 `{type:'artifact'}`（渲 S2）；doc 段始终独立（A1）。`splitIntoSegments` 不动（保留既有测试）。`AgentFinalAnswer` 改用 `groupAdjacentImages(splitIntoSegments(md))`，模板加 image-group 分支（grid + figure，点击 `handleImageClick` 复用 `useImagePreview`）。`AgentArtifactItem` 图片分支重设计为 S2。

### #4 标题分级字号 + 段间距
- **现状**：`AgentFinalAnswer` markdown-body 已有 h1 20 / h2 17 / h3 15（全 sans 同色），层次偏弱。
- **目标**：不同标题「格式」可辨但字号差别不夸张；段间距分级。
- **方案**：h1/h2 用品牌衬线 `--font-heading`（与正文 sans 形成「格式」区分，无需放大字号），h3/h4 sans 加粗。微妙字阶 h1 21 / h2 17.5 / h3 15 / h4 13.5；上间距递减（h1>h2>h3>h4）；h4 用 text-secondary。

### #5 表格/引用/带色格式 → 柔和翠绿（不刺眼）
- **blockquote**：左条 `--color-accent-light hsl(160,70%,68%)`（柔和翠绿，非高饱和）+ 底色 `--color-accent-ultra-soft hsl(160,60%,95%)` + 文字 muted。
- **table**：表头底 `--color-accent-ultra-soft` + 表头字 `--color-primary-hover hsl(160,72%,34%)`（深翠可读）+ 表头底分隔用翠绿淡线 `hsl(160,40%,86%)`；单元格 border 浅。
- **inline code**：由刺眼红 `#b91c1c` 改翠绿 `--color-primary-hover` on `--color-accent-ultra-soft`。
- **链接 a**：翠绿 `--color-accent-link` + 下划线。
- **code block `pre`**：保持深色（代码块惯例，避免可读性回退），不动。
- 顺手修 `.ai-action-btn` hover 的 stale 蓝 fallback `#2563eb`→翠绿。

### #6 「等你回答一个问题」绿底行还转圈
- **根因**：`configs/tool-display.yaml` 给 `ask_user_question` 配了 narration（verb「等你回答」detail「一个问题」），但它是 yield 工具——emit StateUse 后整个 run 暂停，**永远拿不到 result** → 时间线那行永久 in-flight 转圈；且与下方 QuestionPrompt 卡片重复表达同一件事。
- **方案**：前端在 `AgentToolCallList` 过滤掉 `tool_name === 'ask_user_question'` 的 group（computed `visibleGroups`，`v-if` 用过滤后长度）。单点覆盖 streaming/polling/reload 三路（唯一渲染入口）。不动后端 narration emit（安全，避免影响后端状态机）。

### #7 问题卡「已回答态」样式不统一
- **现状**：asking 态是 C3（翠绿渐变 wash + avatar「助手想跟你确认一下」+ 衬线问题 + 翠绿 chip）；answered 态却掉成中性灰卡、无 avatar、无衬线，像换了个组件。
- **方案**：answered recap 拉回 C3 同一家族——保留柔和翠绿身份（`--color-accent-ultra-soft` 底 + `--color-accent-soft` 边，比 asking 渐变更「静」表达「已锁定」）；加 avatar header（翠绿圆 + `Check` 图标 + 「已回答」），点击整行展开/收起；recap 问题文字用衬线 `--font-heading`（与 C3 问题一致）；保留 `displayAnswer`/展开折叠/Q-A 列表全部既有行为与类名。

## 2. 不变量 / 安全
- I5 aiservice 唯一入口、image_gen 计费 Reserve/Reconcile 链路不动（#2b 只改默认文件名常量）。
- 前端 markdown 仍 DOMPurify sanitize（v-html 不变）；HTML artifact 仍走 sandbox iframe。
- #2a 用 group-2 文字不引入 XSS：filename 仅作文本插值（非 v-html）。

## 3. 验证策略（S5，详见 plan T-verify）
- 后端 Go 单测/grep（image_gen 名）。
- 前端 vitest：agentArtifacts（alt 优先名、groupAdjacentImages 分组）、AgentArtifactItem（S2）、AgentFinalAnswer（grid/heading/emerald 结构）、AgentToolCallList（过滤 ask_user_question）、QuestionPrompt（answered C3）、ThinkingBlock（chevron）。
- vue-tsc + eslint（改动文件）。
- dev browse 实跑：思考块箭头方向、文件/图片名、单图 S2/多图 M1、标题层次、表格引用翠绿、问题答完无转圈行、answered 卡 C3。
- 回归保护：逻辑/结构有 vitest 持久保护；纯视觉精细度（字阶/翠绿浓淡/箭头角度）为一次性 dev 确认（可接受）。
