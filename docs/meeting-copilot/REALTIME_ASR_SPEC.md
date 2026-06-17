# 会议副驾 · 实时流式 ASR 重构 — 技术契约 SPEC

> 在已合入 develop 的 meeting-copilot 模块基础上，**把转写链路从"分段批量 ASR"换成"真·实时流式 ASR（阿里 Paraformer-realtime WebSocket）"**。
> 本文件是后端/前端两条实现流水线的**唯一集成契约**。对外协议/事件/字段不可偏离；内部实现细节实现者自定。
> 背景：原 meeting-copilot 用 `aiservice.ASR`(FunASR 批量)，dev 实测 0 转写（FunASR 已弃用 + dev 机器扛不动自托管）。用户拍板上云端真流式。

---

## 0. 目标效果
前端边说 → 转录稿**逐字/逐句近实时**滚动（中间结果灰显，句末定稿）→ 反馈判官读到的转写延迟 ~1-2s → "及时递纸条"。会后：整场录音回放 + 完整转写 + AI 纪要（沿用现有）。

---

## 1. 阿里 Paraformer-realtime WebSocket 协议（后端中继必须照此实现）

- **Endpoint**：`wss://dashscope.aliyuncs.com/api-ws/v1/inference`
- **鉴权（握手请求头）**：`Authorization: Bearer <DASHSCOPE_API_KEY>`。**key 复用 registry 里 `llm_provider.name='ali-dashscope'` 的 `api_key`**（同账号同 key；**禁止硬编码**，从 registry/config 读）。可选 `X-DashScope-DataInspection: disable`。
- **消息时序**：
  1. 连接后发 **run-task**（JSON 文本帧）：
     ```json
     {
       "header": { "action": "run-task", "task_id": "<UUID>", "streaming": "duplex" },
       "payload": {
         "task_group": "audio", "task": "asr", "function": "recognition",
         "model": "paraformer-realtime-v2",
         "parameters": {
           "format": "pcm", "sample_rate": 16000,
           "semantic_punctuation_enabled": true,
           "punctuation_prediction_enabled": true,
           "max_sentence_silence": 800,
           "language_hints": ["zh","en"]
         },
         "input": {}
       }
     }
     ```
  2. 收到 **task-started**（`header.event=="task-started"`）后，开始发**二进制音频帧**（raw PCM 16bit LE 16kHz 单声道，建议每帧 ~100ms = 3200 bytes）。
  3. 持续收 **result-generated**（`header.event=="result-generated"`）：`payload.output.sentence` = `{ text, begin_time, end_time, sentence_end(bool), words[] }`。`sentence_end=false`→中间结果（同一句会不断更新）；`sentence_end=true`→该句最终结果。
  4. 用户停止 → 发 **finish-task**：`{"header":{"action":"finish-task","task_id":"<同上>","streaming":"duplex"},"payload":{"input":{}}}`。
  5. 收 **task-finished**（`header.event=="task-finished"`）→ 关闭。
  - 错误：`header.event=="task-failed"` 含 `header.error_code/error_message` → 上报并关闭。
- **音频时长统计**：累计发送的 PCM 字节 / (16000*2) = 秒数，用于 UsageRecord 计量。

> 实现 Go 端用 `github.com/gorilla/websocket` 作 ws 客户端（若 go.mod 没有则 `go get` 加入）。task_id 用 `github.com/google/uuid`（项目应已有）。

---

## 2. 我方「前端 ↔ 后端」WebSocket 契约

新增后端 WS 端点：**`GET /v1/meetings/:id/asr-stream`**（websocket 升级；注册到 router.go）。

- **鉴权**：浏览器 ws 不能设 Authorization 头 → 用 **query 参数 `?token=<user_jwt>`**，升级前用现有 user_token 校验逻辑验证（复用 AuthMiddleware 的 token 解析；自行从 query 取 token 校验，校验通过取 userID）。再校验 session 属于该 user 且 status=active；feature flag `features.meeting_copilot.enabled` 关则拒绝升级。
- **前端 → 后端**：二进制帧 = raw PCM 16bit LE 16kHz 单声道（~100ms/帧）。文本帧 `{"action":"finish"}` = 用户结束（后端转 finish-task）。
- **后端 → 前端**（JSON 文本帧）：
  - `{"type":"ready"}` — Ali task-started，可以开始送音频
  - `{"type":"interim","text":"<当前句中间文本>"}` — 灰显，覆盖式更新
  - `{"type":"final","segment":{ id, seq, text, start_ms, duration_seconds, created_at }}` — 句末定稿，已落 meeting_segment
  - `{"type":"error","message":"..."}`
  - `{"type":"closed"}` — task-finished
- **后端职责**：升级后为本连接开一条 dashscope ws（§1），双向中继：前端 PCM 帧 → 转发 dashscope 二进制；dashscope result-generated → `sentence_end=false` 转 `interim`、`sentence_end=true` 时 **insert meeting_segment**(seq=当前 max+1, text, start_ms=begin_time, duration_seconds=(end_time-begin_time)/1000) 再回 `final`。任一侧关闭/出错 → 优雅收尾（前端关→给 dashscope 发 finish-task 收尾；dashscope 错→给前端发 error 并关）。goroutine 生命周期与 close 必须干净，无泄漏。

---

## 3. 录音持久化（改动）
流式不再产生离散 WAV 段。录音改为：**前端并行开一个整场 MediaRecorder**（webm/opus 即可，仅供回放），用户结束时把整段 blob 经 **`POST /v1/meetings/:id/recording`**(multipart, 字段 `audio`) 上传 → COS(key `meeting-recordings/<userID>/<sessionID>/full.webm`) → 写 `meeting_session.recording_url`。会后页用单个 `<audio>` 播 `recording_url`。
> `meeting_segment.audio_url` 在流式路径置空即可（不再逐段存音频）。

---

## 4. 计费 / 观测（沿用现有纪律）
- ASR：流式结束后按累计音频秒数记 **UsageRecord**（service_type=asr, model=paraformer-realtime-v2, userID=0 内部试用不扣费）。走 aiservice/计费约定，**不**做 Reserve/Reconcile/会员门。**禁止硬编码 key**（从 registry ali-dashscope 读）。
- Langfuse：给本场 ASR 建 trace+generation（`if tc != nil` 优雅降级），沿用 meeting `internalCallCtx` 模式。
- 反馈/纪要 LLM 调用：**完全不动**（已在 develop，复用现有 biz/meeting/feedback.go、summary.go 与 internalCallCtx）。

---

## 5. 文件清单

### 后端 `numind-server`（worktree `/private/tmp/wt-meeting-realtime-asr-numind-server`）
| 文件 | 职责 |
|---|---|
| `internal/pkg/aiservice/adapter/dashscope_asr_stream.go`（或 `internal/numind/biz/meeting/asr_stream_client.go`）| Ali Paraformer-realtime ws 客户端（§1）：连接/run-task/送PCM/解析 result-generated/finish-task/错误。key 从 registry ali-dashscope 读。暴露：`StartStream(ctx, opts, onInterim func(text), onFinal func(text,beginMs,endMs), onError) (sendPCM func([]byte), finish func(), error)` 之类的接口 |
| `internal/numind/biz/meeting/realtime.go` | 编排：开 dashscope 流、句末落 meeting_segment（复用现有 store）、累计时长记 UsageRecord+Langfuse |
| `internal/numind/controller/v1/meeting/asr_stream.go` | WS 升级端点 `/v1/meetings/:id/asr-stream`：query token 鉴权+session 归属校验+flag 守卫；双向中继；按 §2 协议收发 |
| `internal/numind/controller/v1/meeting/meeting.go`（改）| 加 `POST /v1/meetings/:id/recording` 上传整场录音 |
| `internal/numind/router.go`（改）| 注册 `/v1/meetings/:id/asr-stream`(GET, ws) 与 `/v1/meetings/:id/recording`(POST)，同 flag 守卫组 |
| `go.mod`/`go.sum`（改）| 若缺则 `go get github.com/gorilla/websocket` |

> 旧 `POST /v1/meetings/:id/segments`(分段批量)**保留不动**（向后兼容，前端不再用）。biz/store 的 segment/session model 复用，不改表结构（meeting_session 已有 recording_url 列）。

### 前端 `numind-web-v3`（worktree `/private/tmp/wt-meeting-realtime-asr-numind-web-v3`）
| 文件 | 职责 |
|---|---|
| `src/composables/useMeetingRecorder.ts`（重构）| 不再切 WAV 段上传：①Web Audio 连续采集→下采样 16k 单声道 Int16 PCM→每 ~100ms 经 ws 发二进制帧；②并行开整场 MediaRecorder 收 webm（结束时拿到整段 blob 供上传）。保留 start/stop/pause、stop 时彻底释放 MediaStream/AudioContext/ws |
| `src/api/meeting.ts`（改）| 加 `openAsrStream(sessionId, token, handlers)` 建我方 ws（`/v1/meetings/:id/asr-stream?token=`，注意 ws(s):// 与当前 origin 协议匹配）；加 `uploadRecording(sessionId, blob)` 调 `POST /recording` |
| `src/stores/meeting.ts`（改）| 维护 interim 当前句 + finals 列表；ws 事件 ready/interim/final/error/closed 落 store；transcript = finals 拼接 + 末尾 interim 灰显；结束时 uploadRecording |
| `src/views/meeting/MeetingLiveView.vue`（改）| 用 ws 流式：开始会议→openAsrStream + recorder 流 PCM；转写区渲染 finals + interim(灰/斜体)；结束→recorder.stop→finish ws→uploadRecording。自动/手动反馈逻辑不变（现在转写更新） |
| `src/types/meeting.ts`（改）| 加 ws 消息类型 ready/interim/final/error/closed |
| `src/views/meeting/MeetingSummaryView.vue`（改）| 录音回放改为单个 `<audio :src="recording_url">`（不再按段播放）|

---

## 6. 必读参考
- 现有 meeting 模块（在 develop，直接读）：`internal/numind/biz/meeting/`(meeting.go/transcribe.go/feedback.go/summary.go/internal_call.go)、`internal/numind/store/meeting.go`、`internal/pkg/model/meeting.go`、`internal/numind/controller/v1/meeting/meeting.go`、前端 `src/{api,stores,types}/meeting.ts`、`src/composables/useMeetingRecorder.ts`、`src/views/meeting/*`
- 鉴权 token 解析：`internal/pkg/middleware/middleware.go`（`AuthMiddleware`/`GetCurrentUser`）——ws 端点要复用其 token 校验逻辑（从 query 取）
- registry 读 provider key：grep `llm_provider` 的读取处（store/registry），按现有方式取 `ali-dashscope` 的 api_key/base_url
- COS 上传：`util.UploadBytesToCOS`、`internal/numind/biz/attachment/upload.go`
- 路由注册：`internal/numind/router.go`（feature flag 守卫组 `importMw.FeatureFlag("features.meeting_copilot.enabled")`）
- 前端 ws/SSE 既有写法参考：`src/api/agent-stream.ts`
- Ali 协议权威：本 SPEC §1（已查官方 client-events/server-events 文档）

---

## 7. 验收（S5）
- 后端：`cd <server worktree> && go build ./... && go vet ./...` 0 错；ws 中继的句末落库/时长统计加 Go 单测（mock dashscope 事件流）。
- 前端：`npm run lint && npm run type-check` 0 错（worktree 已 symlink node_modules）。
- 端到端（dev 部署后由用户真机走查）：说话→看 interim 灰显逐字→句末定稿→反馈→结束→录音回放+纪要。
- 若 dashscope 握手 401/403 → 账号未开通实时 ASR/无配额，提示用户在百炼控制台开通（非代码问题）。

---

## 8. 不做（范围外）
- 说话人分离；多人远程接入；逐段音频回放（改整场单文件）；改 DB schema（复用现有表）；动 prod config。
