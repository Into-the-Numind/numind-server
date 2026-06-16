# 会议副驾 (Meeting Copilot) — 技术契约 SPEC

> 这是 `meeting-copilot` feature 的**权威集成契约**。后端、前端两条实现流水线都以本文件为唯一事实源。
> 任何接口字段、事件类型、表结构都以此处定义为准；实现细节（具体算法、文件内部结构）实现者可自主决定，但**对外契约不可偏离**。
> Feature 定位：全新独立模式，内部试用先行，代码高度自包含、可整体删除。

---

## 0. 产品行为（已与用户拍板）

1. **开场**：用户填写「角色定位 + 反馈规则」(`role_prompt`，自由文本)，可选从预设模板载入；可把当前配置存成预设复用。可设 `auto_interval_seconds`（自动反馈最小间隔，默认 60）。
2. **进行中**：浏览器持续录音 → 近实时分段转写 → 滚动显示转录稿。反馈两种触发方式：
   - **自动**：前端每 `auto_interval_seconds` 秒（且自上次反馈以来有足量新转写）调用反馈接口，trigger=`auto`；**服务端 LLM 判官**依据 `role_prompt` 决定「现在是否该给反馈、给什么」。判官认为不必要 → 返回 `skip`，前端静默。
   - **手动**：用户随时点「现在给我反馈」→ 调反馈接口 trigger=`manual`，**总是**生成反馈。
3. **结束**：标记结束、统计时长、生成结构化 **AI 纪要**（要点 / 决议 / 待办，markdown），保留录音（分段音频列表）+ 完整转写稿。可导出。
4. **历史**：可查看过往会议（列表 + 详情：纪要 + 转写 + 录音回放）。

---

## 1. 关键架构决策（不可偏离）

- **ASR 仅批量**：项目 `aiservice.ASR` 走自托管 FunASR，是「整段音频→整段文本」批量接口，**无流式**。因此「实时」=**分段近实时**：前端用 Web Audio 持续采集 PCM，每 ~10s 编码成 **16kHz 单声道 16-bit WAV** blob 上传，后端逐段 ASR + 拼接。WAV/16k/mono 是为兼容 FunASR；**禁止上传 webm/opus**（FunASR 不一定解码）。
- **反馈走「客户端轮询触发 + 服务端判官」**：**不**在服务端起常驻 goroutine 或长连接 SSE 监听。前端定时器驱动「自动」触发；服务端每次按 `role_prompt` 用一次 LLM 调用判断+生成。这样会话无状态、易隔离、易删除。
- **反馈响应用 SSE**：复用本仓库已验证的 SSE + callback 模式（见 §6 参考）。事件 `data: {"type":...,"data":...}\n\n`。
- **计费=记录不扣费**：所有 LLM/ASR 调用走 `aiservice` 统一入口（保证 Langfuse + UsageRecord 自动记录），但**不**调用 credits 的 `Reserve`/`Reconcile`，**不**做会员门槛校验。内部试用阶段「仅记录用量、不扣积分、不拦截」。实现者须阅读现有计费中间件确认 plain `aiservice.Chat/ASR` 调用只记 UsageRecord 不直接扣三池；若发现网关层有自动扣费，使用「bill-only/record-only」模式（参考 chatbot 之外的内部调用配方）确保不扣费。
- **复用现有 task profile，不新注册**：judge/feedback/summary 的 LLM 调用复用已注册的 task profile 常量（实现者在 `internal/pkg/aiservice` 的 profile 定义里挑一个通用 chat profile，如 chatbot 用的那个）。**禁止**硬编码 `ModelOverride`，**禁止**新增未在 DB 注册的 task profile。
- **Feature flag 双开关**：后端 `features.meeting_copilot.enabled`（config，默认 false），路由中间件/控制器在关闭时返回 404 或 403；前端 `VITE_ENABLE_MEETING_COPILOT` 控制导航入口与路由可见性。参考通知中心 `features.notification_center.enabled` 的现成 flag 模式。

---

## 2. 数据模型（migration + GORM models）

迁移文件：`numind-server/migrations/<YYYYMMDD_HHMMSS>_create_meeting_copilot.sql`，全部 `CREATE TABLE IF NOT EXISTS`，外键列建索引，幂等。
GORM models：`numind-server/internal/pkg/model/meeting.go`（每个都显式 `TableName()`；遵守 `.claude/rules/database.md`，注意 `default:true` bool 的 Create 陷阱——本 feature 无该类字段则无需处理）。

### 2.1 `meeting_session`
| 列 | 类型 | 说明 |
|----|----|----|
| id | uint64 PK auto | |
| user_id | uint, NOT NULL, index | 归属用户 |
| title | varchar(255) | 标题（默认「未命名会议 + 时间」，可后续由首段转写/纪要生成）|
| role_prompt | text, NOT NULL | 角色定位 + 反馈规则 |
| preset_id | uint64, NULL | 若从预设载入 |
| status | varchar(20), default 'active' | `active` / `ended` |
| auto_interval_seconds | int, default 60 | 自动反馈最小间隔 |
| recording_url | varchar(1024), NULL | 预留（MVP 录音=分段列表，可空）|
| duration_seconds | int, default 0 | 结束时统计 |
| summary | mediumtext, NULL | AI 纪要（markdown）|
| summary_status | varchar(20), default 'none' | `none`/`generating`/`done`/`failed` |
| started_at | datetime, NULL | |
| ended_at | datetime, NULL | |
| created_at / updated_at | datetime | |

### 2.2 `meeting_segment`（转写分段）
| 列 | 类型 | 说明 |
|----|----|----|
| id | uint64 PK auto | |
| session_id | uint64, NOT NULL, index | |
| seq | int, NOT NULL | 顺序（前端给或后端自增）|
| text | text | 该段转写文本（可空字符串=静音段）|
| start_ms | int, default 0 | 相对会议开始的毫秒偏移（best-effort）|
| duration_seconds | double, default 0 | ASR 返回的音频时长（也用于用量参考）|
| audio_url | varchar(1024), NULL | 该段音频在 COS 的地址（录音回放用）|
| created_at | datetime | |

复合索引 `idx_mseg_session_seq (session_id, seq)`。

### 2.3 `meeting_feedback`（反馈事件）
| 列 | 类型 | 说明 |
|----|----|----|
| id | uint64 PK auto | |
| session_id | uint64, NOT NULL, index | |
| trigger | varchar(10), NOT NULL | `auto` / `manual` |
| anchor_seq | int, default 0 | 生成时转写进度锚点 |
| content | text | 反馈正文（markdown）|
| created_at | datetime | |

### 2.4 `meeting_preset`（角色预设）
| 列 | 类型 | 说明 |
|----|----|----|
| id | uint64 PK auto | |
| user_id | uint, NOT NULL, index | 0 = 系统内置模板 |
| name | varchar(100), NOT NULL | |
| role_prompt | text, NOT NULL | |
| auto_interval_seconds | int, default 60 | |
| is_builtin | tinyint(1), default 0 | 系统内置不可删 |
| created_at / updated_at | datetime | |

迁移末尾 seed 3 个内置预设（user_id=0, is_builtin=1）：
- **辩论陪练**：「你是我的辩论陪练。实时听我和对手的论辩，当我出现逻辑漏洞、举证不足或被对方抓住把柄时立刻提醒我，并给出一句可立即使用的反驳或补强。其他时候保持沉默。」
- **客户访谈记录员**：「你是资深用户研究员。我在做客户访谈。当客户透露关键痛点、预算、决策链或竞品信息时，提示我该追问的下一个问题；当我问了引导性/封闭式问题时提醒我改开放式提问。」
- **头脑风暴催化剂**：「你是头脑风暴催化剂。当讨论卡壳或重复绕圈时，抛出一个新角度或一个'如果……会怎样'的发散问题；当出现好点子时帮我一句话凝练它。不要打断流畅的发散。」

---

## 3. 后端 API 契约（用户端，`router.go` 注册，`user_token` 中间件）

Base 路径 `/v1/meetings`。所有响应走 `core.WriteResponse`（`{code,message,data}`），错误用 `internal/pkg/errno`。控制器只做绑定/取 userID/调 biz/格式化（业务逻辑全在 `biz/meeting`）。`userID := c.GetUint("userID")`。所有接口须校验 session 属于当前 user。

| 方法 | 路径 | 说明 |
|----|----|----|
| POST | `/v1/meetings` | 创建会话。body `{role_prompt, preset_id?, auto_interval_seconds?, title?}` → 返回 session DTO（含 id, status='active', started_at）|
| GET | `/v1/meetings` | 列表（分页 page/page_size）→ `{list:[sessionDTO...], total}` |
| GET | `/v1/meetings/:id` | 详情 → `{session, segments:[...], feedbacks:[...]}`（summary 在 session 里）|
| POST | `/v1/meetings/:id/segments` | **分段转写**。multipart/form-data：`audio`(文件, wav) + `seq`(int, 可选) + `start_ms`(int, 可选)。后端：上传音频到 COS（key `meeting-recordings/<userID>/<sessionID>/<seq>.wav`）→ `aiservice.ASR`（传 AudioBytes, format=wav）→ 写 meeting_segment → 返回 `{segment: {id,seq,text,duration_seconds,audio_url}}`。**空转写也要落库**（保留时间轴）。|
| POST | `/v1/meetings/:id/feedback` | **反馈（SSE）**。body `{trigger:"auto"\|"manual"}`。见 §3.1。|
| POST | `/v1/meetings/:id/end` | 结束会话：status='ended', 算 duration, **同步生成 summary** → 返回 `{session}`（含 summary, summary_status='done'）。转写过长时实现者自行 window/分块。|
| GET | `/v1/meetings/presets` | 当前用户预设 + 内置（user_id=0）→ `{list:[presetDTO...]}` |
| POST | `/v1/meetings/presets` | 存预设。body `{name, role_prompt, auto_interval_seconds?}` → presetDTO |
| DELETE | `/v1/meetings/presets/:id` | 删预设（仅本人、非 builtin）|

> DTO 用 json camelCase 或与现有模块一致的风格（参考 chatbot DTO 命名）。时间字段 ISO8601。

### 3.1 反馈 SSE 协议
- 响应头：`Content-Type: text/event-stream`，`Cache-Control: no-cache`，`X-Accel-Buffering: no`。
- 帧格式：`data: {"type":"<t>","data":<payload>}\n\n`，每帧后 flush。
- 事件类型：
  - `token`：`data` = 文本增量字符串（反馈正文流式输出）
  - `skip`：`data` = `{reason}`（仅 auto：判官认为此刻无需反馈；前端静默，不渲染气泡）
  - `done`：`data` = 完整 feedback DTO `{id, trigger, content, anchor_seq, created_at}`（已落库）
  - `error`：`data` = `{message}`
- **生成逻辑（单次 LLM 调用兼判官+生成）**：
  - 拼系统提示：`role_prompt` + 指令「依据上述角色，阅读最近的会议转写。判断此刻是否应给出反馈。若应当，直接输出反馈正文（简洁、可立即使用）；若此刻不需要，仅输出 `NO_FEEDBACK` 这一个标记，不要有其他内容。」
  - 输入：拼接最近的转写文本（实现者定窗口，如最近 ~2000 字 + 总览）。
  - `trigger=auto`：若模型输出以 `NO_FEEDBACK` 开头 → 发 `skip`，不落库。否则把内容作为反馈（流式或一次性）→ 落库 → `done`。
  - `trigger=manual`：系统提示改为「**必须**给出反馈」（不提供 NO_FEEDBACK 选项），总是生成 → 落库 → `done`。
  - 用 `aiservice.ChatStream` 流式输出 `token`；用 sentinel 缓冲首段判断是否 `skip`。或 manual 流式、auto 非流式——实现者择优，但 SSE 事件契约不变。

---

## 4. 后端文件清单（`numind-server`，worktree `/private/tmp/wt-meeting-copilot-numind-server`）

| 文件 | 职责 |
|----|----|
| `migrations/<ts>_create_meeting_copilot.sql` | 建 4 表 + 索引 + seed 内置预设 |
| `internal/pkg/model/meeting.go` | 4 个 GORM model + TableName |
| `internal/numind/store/meeting.go` | `IMeetingStore` 接口 + 实现（session/segment/feedback/preset CRUD + 分页列表 + 按 session 取 segments/feedbacks）。**并按本仓库 store 工厂模式注册**（参照 chatbot 的 store 怎样挂进聚合 store）|
| `internal/numind/biz/meeting/meeting.go` | 会话生命周期（Create/Get/List/End）+ 预设 CRUD + DTO 组装 |
| `internal/numind/biz/meeting/transcribe.go` | 分段 ingest：COS 上传 + `aiservice.ASR` + 落 segment |
| `internal/numind/biz/meeting/feedback.go` | 反馈 judge+生成（auto/manual），通过 callback 推 SSE 事件 |
| `internal/numind/biz/meeting/summary.go` | 结束时生成结构化纪要 |
| `internal/numind/controller/v1/meeting/meeting.go` | 控制器（含 SSE 端点） |
| `internal/numind/router.go` | **注册全部 /v1/meetings 路由**（user_token 组）+ feature flag 守卫 |
| config（`config_dev.yaml`/`config_local.yaml` 加 `features.meeting_copilot.enabled: true`；**不要动 config_prod.yaml**；若有 config struct 定义文件需加字段） | flag |

**计费纪律**：biz 层 LLM/ASR 调用直接走 `aiservice.*`，**不**写 `Reserve`/`Reconcile`/`CanPerformAIOperation`/会员校验。传真实 `userID` 给 aiservice（用量归属），但不扣费。

**Langfuse**：按 `.claude/rules/ai-service.md` 给会议反馈/纪要创建 trace + generation（优雅降级 `if tc != nil`）。

---

## 5. 前端文件清单（`numind-web-v3`，worktree `/private/tmp/wt-meeting-copilot-numind-web-v3`）

技术：Vue3 setup 语法 + Pinia setup store + axios 经 `src/api/request.ts`。遵守 `.claude/rules/frontend-state.md`、`.claude/rules/ui-ux.md`（4 状态：loading/empty/error/success；销毁性操作确认 dialog；自研组件、禁外部 UI 框架）。**前端禁止硬编码后端地址/凭据**。

| 文件 | 职责 |
|----|----|
| `src/types/meeting.ts` | TS 接口：MeetingSession / MeetingSegment / MeetingFeedback / MeetingPreset + 请求/响应类型（**字段与 §2/§3 DTO 完全对齐**）|
| `src/api/meeting.ts` | HTTP 封装（createSession/list/get/ingestSegment/end/presets CRUD）+ 反馈 SSE 消费（复用 agent-stream 的 SSE 读取/解析模式）|
| `src/stores/meeting.ts` | Pinia store：当前 session、segments、feedbacks、presets、录音状态、computed（transcript 拼接 / isRecording / canFeedback），actions |
| `src/composables/useMeetingRecorder.ts` | **核心录音引擎**：`navigator.mediaDevices.getUserMedia({audio})` + AudioContext 采集 PCM，缓冲，每 ~10s 编码 **16kHz 单声道 16-bit WAV** blob 通过回调吐出（用于上传）；支持 start/stop/pause。优先 AudioWorklet，ScriptProcessorNode 兜底。附最小 WAV 编码器（44 字节头 + PCM16；从 AudioContext sampleRate 下采样到 16000）。**保证窗口间无明显丢字（连续采集后切片，而非 stop/start 留缝）**。|
| `src/views/meeting/MeetingSetupView.vue` | 开场：role_prompt 文本域 + 预设下拉(载入/存为预设) + auto_interval 设置 + 麦克风授权 + 「开始会议」|
| `src/views/meeting/MeetingLiveView.vue` | 进行中：左=滚动转写稿，右=反馈流（卡片，区分 auto/manual）；录音控制（计时器/暂停/结束）；「现在给我反馈」按钮；自动反馈定时器（每 auto_interval 且有新转写时触发 trigger=auto）|
| `src/views/meeting/MeetingSummaryView.vue` | 会后：AI 纪要(markdown 渲染) + 完整转写 + 录音回放(按 segment 顺序播放 audio_url) + 导出(纪要/转写下载) |
| `src/views/meeting/MeetingHistoryView.vue` | 历史会话列表（4 状态 + 进入详情）|
| `src/router/index.ts` | 注册路由（`/meeting`、`/meeting/live/:id`、`/meeting/summary/:id`、`/meeting/history`），**用 `import.meta.env.VITE_ENABLE_MEETING_COPILOT` 守卫入口**|
| 导航入口 | 在合适的主导航/首页加「会议副驾」入口，受 VITE flag 控制 |
| `.env.development`（或对应 env 文件）加 `VITE_ENABLE_MEETING_COPILOT=true`；`.env.production` 设 false 或不加 | flag |

> 视图可按实现者判断合并/拆分，但要覆盖上述全部用户路径，且组件自研、与现有设计 token 一致。

---

## 6. 必读参考（实现前先读，照搬约定，勿凭空发明）

- ASR：`internal/pkg/aiservice/ai.go`、`internal/pkg/aiservice/types.go`（ASRRequest/ASRResponse）、`internal/pkg/aiservice/adapter/funasr.go`
- aiservice 入口 + ChatStream channel：`internal/pkg/aiservice/ai.go`
- 计费中间件（确认 record-not-charge）：`internal/pkg/aiservice/middleware/billing.go`；对照 chatbot 的 Reserve 用法 `internal/numind/biz/chatbot/stream.go`（本 feature **不**照抄 Reserve）
- COS 上传：`internal/numind/biz/attachment/upload.go`、`util.UploadBytesToCOS`
- SSE + callback 模式：`internal/numind/controller/v1/salesrag/sales_rag.go`、`internal/numind/controller/v1/sop/sop.go`
- 路由注册：`internal/numind/router.go`
- 模块隔离样板（store/biz 工厂如何挂载、DTO 命名）：chatbot 全套（`biz/chatbot/`、`model/chatbot_session.go`）
- Feature flag 样板：通知中心 `features.notification_center.enabled`（grep `notification_center` 找 config struct + 守卫位置）
- task profile 常量：`internal/pkg/aiservice` 下 profile 定义（grep `profile.` 用法）
- 前端 SSE 读取/解析：`src/api/agent-stream.ts`（`readAgentSSEStream`/`parseAgentSseChunk`）
- 前端模块样板：`src/views/agent/`、`src/stores/agentChat.ts`、`src/api/chatbot.ts`

---

## 7. 验收（S5 策略）

- 后端：`cd <server worktree> && go build ./... && go vet ./...` 0 错；biz/meeting 关键逻辑（judge sentinel 解析、segment 落库、summary 生成）加 Go 单测（mock store + mock aiservice）。`task lint` 通过。
- 前端：`npm run lint && npm run type-check` 0 错（worktree 已 symlink node_modules）。
- 人工/浏览器：开场→录音→看到转写滚动→自动反馈 skip/出现→手动反馈→结束→纪要+回放。（dev 部署后由用户走查真实麦克风路径——AI 不能替用户说话。）
- Feature flag 关闭时：前端无入口、后端路由 404/403。

---

## 8. 不做（MVP 范围外，注明以防 scope creep）

- 说话人分离（谁说的）——单声道不分离。
- 服务端音频合并成单文件——录音=分段列表顺序播放。
- 流式逐字字幕——近实时分段即可。
- 会员门槛 / 扣积分 / Reserve-Reconcile——内部试用仅记用量。
- 多人远程接入同一会议——单设备单麦采集。
