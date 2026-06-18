# Agent Output UX Followup — Spec + Plan（S0-S3 合并）

> agent-output-ux-fixes 在 dev 验收发现的 5 个问题（用户报告 2026-06-18）。新 Standard feature。
> 跨 numind-server + numind-web-v3。Bug-from-Customer：问题4/5a 需复现测试。
> 设计决策（用户拍板 Option A）：每阶段一个 liveness 信号 + 生成期明确"正在生成…"提示。
> **关键简化**：问题3 用前端启发式（不依赖后端 eino 流式 partial tool call），前后端解耦、低风险。

## 根因（已调查确认）

| # | 现象 | 根因（file:line） | 修复侧 |
|---|------|------|------|
| 1 | 纯附件气泡多余分割线 | `AgentMessageItem.vue` `.user-atts` 有 `border-top`(~420)，纯附件时空 `<p class="text">`(~142) 仍渲染+分割线还在 | 前端 |
| 2 | 工具行上下 padding 不均 | `AgentToolCallItem.vue` `.tl-ic` `margin-top:2px`(~93)（当初为对齐跳动点加）使上间隙(7px)>下(5px) | 前端 |
| 3 | 生成文件时只有光标无进度 | LLM 流式生成工具参数(文件内容)期间，eino 未给完整 tool call(tc.ID 空)→`tool_call_start` 直到生成完才发(runner_stream.go:262-284)→那段只有 caret；"正在生成文件"一闪而过 | 前端(启发式) |
| 4 | 文件不稳定变卡片 | **真凶**：docx 经 docx-author 技能→`run_python` 生成，输出 `{"files":[{url,filename,...}],...}` **数组**；`artifactFromToolResult`(tool_create_helpers.go:200-210) 只认单个 `fileCreateOutput{url,...}`→run_python 文件**漏收**→收集器空→stripNodes 无东西剥→表格链接原样保留。create_html 单文件格式才被收 | 后端 |
| 5a | 任务结束还显"处理中" | `isRunning`(agentChat.ts:229) 依赖 `currentRun.status`；terminal 后若 reconcileFromDB 失败/吞异常，status 不复位→`AgentRunPulse`(visible 条件:isRunning && !waiting && !lastMsgStreaming) 不消失 | 前端 |
| 5b | 3+ liveness 指示器重复 | ②工具行 spinner(AgentToolCallItem:49) + ③工具行跳动点(.tl-dots:57，上轮加的) 同行重复；①流式光标 ④底部"处理中"呼吸灯 | 前端 |

## 任务计划（task）

### 后端（numind-server worktree）— Bug-from-Customer，repro-first

**BE1（问题4，多文件 artifact 收集）**：
- 让收集器解析 `run_python` 的 `runPythonOutput{files:[{filename,url,size_bytes}]}` 数组（多文件），每个文件 `add` 一次。
- 改动：`tool_create_helpers.go` 新增解析 run_python 形状（或 adapter_full_to_eino.go 调用站点循环）；保持 create_html 单文件路径不变。新 helper 返回 `[]artifact{url,filename,mime}`（多文件），call site 循环 `artifactCollectorFrom(ctx).add(...)`。mime 用 mimeFromArtifact（已认 html/docx/pdf 等）。
- **复现测试先 RED**：构造 run_python 输出 `{"files":[{"filename":"a.docx","url":"<cos agent-outputs>"},{"filename":"b.html","url":"<cos>"}]}` → 断言收集器收到 2 个 artifact（docx + html），finalizeInto 把它们嵌为独占行卡片链接（含表格里行内链接被 stripNodesReferencing 剥离的场景）。pre-fix 收 0 → FAIL。
- 验收：`go test ./internal/numind/biz/agent/` + vet 绿；artifactFromToolResult 单文件路径回归不破。

### 前端（numind-web-v3 worktree）

**FE1（问题1）**：`AgentMessageItem.vue` user 分支——`<p class="text">` 加 `v-if="asUser.text"`；`.user-atts` 的 `border-top`+`padding-top` 改为仅在有文字时生效（用 `.text + .user-atts` 选择器，或纯附件时不加）。纯附件→只显 chip，无分割线。

**FE2（问题2）**：`AgentToolCallItem.vue` `.tl-line` 改 `align-items: center` + 去掉 `.tl-ic` 的 `margin-top:2px`，使上下 padding(各5px)视觉均匀；**同步调整虚线连接器 `::after` 的 top 偏移**（原 22px 依赖 margin-top，去掉后微调，别画坏连接线）。

**FE3（问题5b）**：删掉 `.tl-dots` 跳动点（template + CSS + 上轮 vitest 里相关断言），消除与 spinner 的同行重复。工具行 active 只保留 spinner。（注意 FE2/FE3 同文件，串行改。）

**FE4（问题5a）**：保证 run 终止后 `isRunning`/`AgentRunPulse` 可靠复位。terminal 事件(agentChat.ts ~1308) 即时把 `currentRun.status` 设为终态（不只依赖异步 reconcileFromDB；reconcile 失败也不回退）；`AgentRunPulse` 可加守卫：currentRun 为终态(completed/failed/cancelled/timeout 等)时一律不显。补 vitest：terminal 后 isRunning=false / pulse 隐藏。

**FE5（问题3，生成期"正在生成…"启发式）**：
- 在 streaming assistant 期间，若文本/reasoning **停止增长超过阈值**（约 1.2-1.5s）且尚未出现 tool_call_start，则在光标处显示一个明确的"正在生成…"动态指示（带动效，区别于静止光标）。一旦 tool_call_start 到来→切到工具行（spinner）。
- 实现：agentChat store 记录 `lastStreamDeltaAt`（token_delta/reasoning_delta 时更新）；AgentMessageItem 流式气泡用 computed 判断"stalled"→渲染"正在生成…"指示（尊重 reduced-motion）。
- 这样长等待期有明确反馈，且只增一个信号（不与 spinner/caret 冲突：caret 期 stalled 才升级为"正在生成…"）。
- 补 vitest 覆盖 stalled 判定（可纯逻辑测）。

### 指示器最终态（问题5b 收口）
- 流式文本增长中：caret（动）。
- 流式但停滞（生成工具参数）：caret → "正在生成…"（FE5）。
- 工具执行中：工具行 spinner（删跳动点 FE3）。
- run 级 liveness：底部"处理中…"仅在真 running 时（FE4 修复终态复位）。
- 一个状态一个信号，不重复。

## S5 验证策略
- 后端 BE1：Go repro 测试（多文件收集 + 表格行内剥离 + 独占行卡片）永久回归 + `go test`/vet。
- 前端：FE3/FE4/FE5 纯逻辑 vitest（删 dots 断言更新 / terminal 后 isRunning=false / stalled 判定）；FE1/FE2 视觉 + type-check + eslint。
- 视觉端到端（卡片稳定、生成期提示、无重复指示器、纯附件无分割线、padding 均匀、结束后无"处理中"）→ S6 部署 dev 后用户/gstack 取证（per feedback_walkthrough_user_executes）。

## 实现方式
用 dynamic workflow：后端 BE1 ‖ 前端 FE1-FE5（跨仓库 Tier 2 并行，各自 worktree 串行+commit），每仓库双 Sonnet review（spec+quality 并行）。主 session 收口 review fixes + 门禁 + ndf-done + 部署 dev。
