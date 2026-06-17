# 会议副驾 · 反馈引擎 v2 — 技术契约 SPEC

> 在已合入 develop 的 meeting-copilot + meeting-realtime-asr 基础上做增量增强。三块：①触发升级 ②上下文不幻觉(滚动摘要) ③修复总结无法生成的 bug。
> 本文件是后端/前端两条流水线的**唯一集成契约**。对外行为/字段不可偏离；内部实现自定。**这是在现有模块上改，先读现有代码。**

---

## 0. 三块改动总览
1. **触发更聪明**：用户自设 5-60s 间隔 + 内容闸 + 停顿感知 + 手动兜底（**去档位、去冷却**）。
2. **上下文不幻觉**：滚动结构化摘要(running memory) + 最近 **5 分钟**逐字 + 已给反馈清单（去重），替换原"最近 2000 字"窗口。
3. **修复总结 bug**：`/end` 异步化（秒回 + 后台生成 + 前端轮询）+ 前端 `doEnd` 硬化。

---

## 1. 触发升级（前端为主）

现状：前端定时器每 `auto_interval_seconds` 秒、且"自上次反馈有新转写"(`canFeedback`/`lastFeedbackSeq`) 才触发 `requestFeedback('auto')`；间隔在前端被 clamp 到最小 15s。

改为：
- **基础间隔用户自设 5-60 秒**：`auto_interval_seconds` 已是 `meeting_session`/`CreateSessionReq` 字段。前端 Setup 页输入框改成允许 **5-60**（number 或 slider，默认 15），clamp 下限从 15 改 **5**、上限 60。**不要做"克制/适中/积极"档位**——就一个数字。后端 `CreateSessionReq.AutoIntervalSeconds` 校验放宽到 5-60（若有校验）。
- **内容闸**：定时器到点时，**只有"自上次检查以来新增转写 ≥ 2 个 final 段 或 ≥ ~100 字"才真正调 `requestFeedback('auto')`**；否则这一拍**静默跳过、不发请求**。（细化现有 `canFeedback`：从"有任何新 seq"升级为"新内容达到阈值"。）
- **停顿感知**：到点时若**正在出 interim**（`interimText` 非空、说明有人在说），**推迟这次触发**，等到 interim 落定（一个自然停顿，约 1 秒无新 interim）再发；避免把话打断到一半。实现可用"上次 interim 更新时间，到点时若距今 <1s 则延后到下一拍"。
- **去掉任何冷却**：给完反馈后**不要**加 25s（或任何）冷却窗口；下一拍照常按上述规则判断。
- **手动按钮永远在**（不变）。

> 自动/手动都走现有 `POST /v1/meetings/:id/feedback`(SSE) + 判官 NO_FEEDBACK sentinel，**不动**那套机制。

---

## 2. 上下文不幻觉（后端为主）—— 滚动摘要 running memory

现状：`feedback.go` 的 `buildTranscriptWindow` 只取**最近 2000 字**喂判官 → 长会议看不到全局 → 幻觉。`summary.go` 结束时一次性读全稿。

改为 **滚动结构化摘要 + 最近 5 分钟逐字 + 已给反馈清单**：

### 2.1 滚动摘要存储
- `meeting_session` **新增列 `running_summary MEDIUMTEXT NULL`**（migration `migrations/<ts>_add_meeting_running_summary.sql` + model 字段 + helper.go AutoMigrate 已含 meeting 表，加新列由 AutoMigrate 自动补，但 migration 文件仍要写以备非 AutoMigrate 环境）。

### 2.2 滚动摘要异步更新
- 在 realtime 中继落 final 段的路径（`biz/meeting/realtime.go` 的 `handleFinal`）**节流触发**异步更新：每场会话维护"距上次摘要的新增字数/段数"，**累计达 ~1500 字 或 ~90 秒**就 spawn 一个**后台 goroutine**（独立 `context.Background()` + `recover()` 防 panic + 每会话 mutex/flag 防并发重入）把新增转写折进 `running_summary`：
  - cheap LLM 调用：输入 `[已有 running_summary] + [自上次摘要以来的新转写]` → 输出更新后的结构化摘要（字段见 §2.4）。复用已注册 profile（`profile.ChatbotStream` 或更便宜的已注册 chat profile；**禁新注册**）。`internalCallCtx` 计费剥离 + Langfuse，同现有约定。
  - **绝不阻塞中继/录音**：纯后台；失败仅 log，不影响转写与反馈。
- 维护"已折叠到哪个 seq"的游标（in-memory per realtime session 即可；重连重置可接受，折叠幂等性由"摘要+新增"prompt 容忍）。

### 2.3 判官/写手看到的上下文（feedback.go 改）
`GenerateFeedback` 拼 context 改为三段：
```
[会议滚动摘要]
<running_summary，没有则"（暂无）"。让模型据此了解整场脉络>

[最近 5 分钟对话]
<最近 5 分钟的逐字转写>

[你已经给过的反馈（不要重复）]
<本会话已落库的 meeting_feedback.content 列表，最多最近 ~10 条>
```
- **最近 5 分钟逐字**：把 `buildTranscriptWindow` 换成"按 `created_at` 取最近 5 分钟的 final 段"（加安全字数上限 ~8000 字兜底，超长只保留尾部）。
- **已给反馈清单**：`ListFeedbacksBySession` 取本会话反馈，注入 prompt 让判官避免重复（最近 ~10 条即可）。
- 系统提示(`buildFeedbackSystemPrompt`)在原 role_prompt 指令后追加一句"参考下面的会议滚动摘要了解全局；不要重复你已经给过的反馈"。

### 2.4 摘要结构（抗漂移）
running_summary 用结构化 markdown：
```
## 会议主题/目标
## 已确立的事实与决议
## 各方立场/诉求
## 未决问题/待办
```
（这份摘要也直接喂给 §3 的最终纪要生成。）

---

## 3. 修复总结无法生成的 bug

### 根因（已实证，写给实现者，别再排查）
- 后端 `/end` + `generateSummary` **本身正常**（直打 dev session 4 成功，5s 出有效纪要）。
- DB 所有 session 卡 `status=active`+`summary_status=none` → `EndSession` biz **从未在后端跑完**。
- 真因 = **前端 `doEnd` 先 `await recorder.stop()` → `meeting.finishAsrStream()` → `await meeting.waitForAsrClosed(5000)` → `uploadRecording` → 最后才 `endMeeting()`**；前面任一步卡住/抛错就到不了 `endMeeting`；且 `/end` **同步阻塞 ~5s 生成总结**，期间请求被取消则 EndSession 的 ctx(`c.Request.Context()`) 取消、`UpdateSession` 也失败 → 啥都没存。

### 3.1 后端：`/end` 异步化（核心修复）
`EndSession`（`biz/meeting/meeting.go`）改为**两段**：
1. **同步段（秒回）**：校验归属 → 设 `status=ended` + `ended_at` + `duration` + **`summary_status=generating`** → `UpdateSession` 持久化 → **立即返回** session DTO。
2. **异步段**：spawn 后台 goroutine（**独立 `context.Background()`**，不用请求 ctx；`recover()` 防 panic）：读 `running_summary` + 尾部未折叠转写 → 生成最终结构化纪要（基于滚动摘要 → 近乎瞬时；无 running_summary 时回退到读全稿，即现有 `generateSummary` 逻辑）→ `UpdateSession` 写 `summary` + `summary_status=done`；失败置 `failed` 并 log。
- 这样"结束"永远秒成功、不被请求取消打断；总结好了前端轮询拿到。
- 幂等：重复 `/end` 已 ended 的 session 直接返回（现有逻辑保留）。

### 3.2 前端：`doEnd` 硬化 + 总结轮询
- `doEnd`（`MeetingLiveView.vue`）**保证 `endMeeting()` 一定执行**：把 `recorder.stop()` 包超时（`Promise.race` ~3s，超时也继续）、`waitForAsrClosed`/`uploadRecording` 各自 try-catch（失败仅提示、不中断），**然后无论如何调 `endMeeting()` 并跳转 summary 页**。录音上传可移到 endMeeting 之后或并行（上传失败不该挡结束）。
- `endMeeting()` 现在秒回（summary_status=generating）。**Summary 页轮询**：`MeetingSummaryView.vue` 在 `summary_status==='generating'` 时每 ~2.5s `GET /v1/meetings/:id` 拉一次，直到 `done`(渲染纪要) 或 `failed`(显示失败+重试)；轮询要在组件卸载时清理。
- **回归测试（Rule 11，Bug-from-Customer）**：加一个前端测试模拟"`recorder.stop()` 抛错或超时" → 断言 `endMeeting()` 仍被调用、且跳转 summary。commit 链首个为失败复现测试。

---

## 4. 文件清单

### 后端 `numind-server`（worktree `/private/tmp/wt-meeting-feedback-v2-numind-server`）
| 文件 | 改动 |
|---|---|
| `migrations/<ts>_add_meeting_running_summary.sql` | `ALTER TABLE meeting_session ADD COLUMN running_summary MEDIUMTEXT NULL`（IF NOT EXISTS 风格/幂等）|
| `internal/pkg/model/meeting.go` | MeetingSession 加 `RunningSummary` 字段 |
| `internal/numind/biz/meeting/realtime.go` | handleFinal 节流触发异步滚动摘要更新（独立 ctx+recover+per-session 防重入）|
| `internal/numind/biz/meeting/summary.go` | 加"滚动摘要更新"函数 + 最终纪要改为基于 running_summary（无则回退全稿）|
| `internal/numind/biz/meeting/feedback.go` | context 改三段(滚动摘要+最近5min逐字+已给反馈)；`buildTranscriptWindow`→按 created_at 取最近5分钟 |
| `internal/numind/biz/meeting/meeting.go` | EndSession 改异步(秒回 generating + 后台 goroutine 生成)；CreateSessionReq 间隔校验放宽 5-60 |
| 单测 | realtime/summary/feedback 关键逻辑(滚动摘要折叠、5min窗口、异步end状态机) |

### 前端 `numind-web-v3`（worktree `/private/tmp/wt-meeting-feedback-v2-numind-web-v3`）
| 文件 | 改动 |
|---|---|
| `src/views/meeting/MeetingSetupView.vue` | 自动反馈间隔输入 5-60(去任何档位)；默认 15 |
| `src/views/meeting/MeetingLiveView.vue` | 内容闸(新内容达阈值才触发)+停顿感知(interim 活跃时延后)+去冷却；doEnd 硬化(超时/try-catch/保证 endMeeting+跳转) |
| `src/stores/meeting.ts` | canFeedback 升级为"新内容达阈值"；endMeeting 适配秒回；加轮询 summary 的支持(或在 view 内轮询) |
| `src/views/meeting/MeetingSummaryView.vue` | summary_status=generating 时轮询 GET /:id 直到 done/failed；卸载清理 |
| `src/api/meeting.ts` | getSession 已有(轮询复用)；如需 |
| 回归测试 | doEnd 在 recorder.stop 失败/超时下仍调 endMeeting+跳转 |

---

## 5. 必读参考（先读现有实现，增量改）
- 反馈：`internal/numind/biz/meeting/feedback.go`（buildTranscriptWindow / buildFeedbackSystemPrompt / GenerateFeedback / consumeFeedbackStream）
- 总结：`internal/numind/biz/meeting/summary.go`（generateSummary / joinTranscript）
- 会话/结束：`internal/numind/biz/meeting/meeting.go`（EndSession / CreateSessionReq / toSessionDTO）
- 实时中继：`internal/numind/biz/meeting/realtime.go`（handleFinal 落 final 段的位置）
- store：`internal/numind/store/meeting.go`（ListSegmentsBySession / ListFeedbacksBySession / UpdateSession / GetMaxSegmentSeq）
- model + AutoMigrate：`internal/pkg/model/meeting.go`、`internal/numind/helper.go`（meeting AutoMigrate 块）
- 前端：`src/views/meeting/{MeetingSetupView,MeetingLiveView,MeetingSummaryView}.vue`、`src/stores/meeting.ts`、`src/api/meeting.ts`
- 计费剥离：`internal/numind/biz/meeting/internal_call.go`（internalCallCtx，所有 LLM 调用沿用，userID=0 不扣费）

---

## 6. 不变 / 不做
- 不动：实时 ASR 链路(ws/dashscope/录音)、反馈 SSE 协议与 NO_FEEDBACK 机制、feature flag、计费纪律(userID=0 记录不扣费)。
- 不做：触发档位(用户明确不要)、反馈冷却(明确不要)、说话人分离、改 prod config、动其他 feature。

---

## 7. 验收（S5）
- 后端：`go build ./... && go vet ./... && go test ./internal/numind/biz/meeting/...` 全过(含异步 end 状态机、滚动摘要折叠、5min 窗口单测)。
- 前端：`npm run type-check` + scoped eslint 0 错；doEnd 回归测试通过。
- 端到端(dev 部署后)：开会说话→转写→反馈(看是否更跟手+不重复+不幻觉)→**结束秒回、summary 页轮询出纪要**(修复验证)。
- 重点回归：**结束会议必出纪要**(session 不再卡 active/none)。
