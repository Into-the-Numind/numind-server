# 会议副驾 · 说话人分离（实时 + 离线）设计定稿

> NDF Standard `meeting-speaker-diarization`。本文为建设权威蓝图：合并 dynamic-workflow `wf_772bbc30-b3a` 的 S0-S3 设计 + 双路对抗审查（均 go-with-changes）的 P0/P1 修正 + spike 实测结论。延续 `docs/meeting-copilot/`（SPEC / REALTIME_ASR_SPEC / FEEDBACK_V2_SPEC）。

## 0. 一句话

实时 ASR 管"说什么"；自建轻量声纹引擎管"谁说"。**同一套 CAM++ embedding 同时喂在线增量聚类（会中临时标签）+ 离线全局重聚类（会后精修）**。国产、纯 CPU、数据不出境、feature flag 可整体删。

## 1. 关键事实（定选型）

- **DashScope 所有 realtime ASR 模型（paraformer-realtime / qwen3-asr-flash-realtime / fun-asr-realtime）都不支持 speaker diarization**——diarization 仅文件/非实时模式支持。⇒ 换实时 ASR 模型拿不到说话人区分，**必须自建声纹引擎**。
- 不用 Azure（跨境）；不自建 GPU 流式（重，dev/构建机无 GPU）。选**自建逐段 embedding + 聚类**（国产 CAM++、CPU、不出境）。
- 引擎 = 3D-Speaker **CAM++ `iic/speech_campplus_sv_zh-cn_16k-common`**（达摩院，中文 16k，**192 维**），导出 ONNX → onnxruntime CPU。

## 2. SPIKE 实测结论（2026-06-18，构建机 4 核纯 CPU，ONNX）

| 场景 | 单段 embedding p95 | 判定 |
|------|------|------|
| 单会议（空载）| **89ms**（mean 69）| ✅ 5.6x 余量 |
| 3 会议背靠背并发（最坏）| ~480ms（max 602）| ⚠️ 贴线 |

延迟与音频内容无关。现实负载（1-2 并发、分段稀疏~每 5s 一段）很舒服；3+ 连续并发才贴线，有缓解（每 session 限 1 线程 / worker 池 / 独立机 / channel 满丢弃优雅降级）。**第一工程风险（CPU 延迟）= 清除。** 合成清晰音色 purity 1.0 是"假性优秀"，**非生产准确率**；准确率阈值在 dev 验收用真实会议校准。

## 3. 总体数据流

```
浏览器(16k PCM) ──ws──> Go relay(realtime.go) ──> DashScope ASR ──> 分段[text,start,end] 落库 meeting_segment
                            │
              (1) per-session PCM ring buffer（每帧立即 copy！见 P0-1）
                            │
              (2) 分段就绪 → 按[start,end]切片 PCM → 非阻塞 channel（满即丢弃，永不反压 ASR）
                            │ 独立 worker goroutine
                            ▼
              声纹服务(构建机 CPU) POST /embed → 192-d embedding（顺手存 meeting_segment_embedding）
                            │
              (3) 在线增量聚类（relay 内存 per-session 质心，双阈值迟滞）→ online_speaker_id → UPDATE + ws 回推
    ─────────────────────────────────────────────────────────────────────────────
录音上传完成(UpdateRecordingURL 成功) ──(4 异步, 见 P1-触发时序)──>
              离线精修 worker：**主路径 = 拉本场已存逐段 embedding 全局 AHC 重聚类（见 P0-2）**
              （兜底：full.webm → /diarize；但不依赖录音时间轴）
                            │ → final_speaker_id + meeting_speaker(出场序稳定编号+色)
                            ▼
              前端下次加载/回推：标签从 A/B/C(临时) 切到 1/2/3(精修)；纪要按 final 归并
```

## 4. 审查 P0/P1 修正（**必须吸收，相对原始 synthesis 的 delta**）

- **P0-1（数据损坏，必修）**：relay PCM ring buffer 写入必须对 gorilla `conn.ReadMessage()` 返回的每帧**立即 `make+copy`**——gorilla 下次读复用同一底层 buffer。`asr_stream_client.go:SendPCM` 已踩过同坑。**T6 单测专门构造"复用底层 buffer 的连续帧"断言切片字节正确。**
- **P0-2（离线对齐错位，必修）**：dashscope `begin_time` 每次重连/暂停恢复从 0 重算，但 full.webm 是整场连续录音 ⇒ 按录音绝对时间重叠对齐**必错位**。**修法：离线精修主路径 = 本场已存逐段 embedding 全局重聚类**（embedding 按 segment 抽、天然对齐，绕开录音时间轴）；full.webm → /diarize 降级为兜底，且兜底也不靠 start_ms（靠服务端自切 + 文本/时长就近）。**D5（embedding 落库）从"可选低优先"提为离线主路径前提。**
- **P1-relay 非阻塞**：ring buffer 操作持锁 O(1)；channel push 一律 `select{ case ch<-x: default: drop+metric }`；切片+embedding 在独立 worker goroutine。read loop 与 handleFinal 回调两 goroutine 都 touch ring buffer → 明确锁、持锁 O(1)。T7 单测：channel 满时 ASR 转发无回归。
- **P1-录音格式**：full.webm 是 webm/opus。VP 容器**装 ffmpeg**；/diarize 收到 audio_url 先 ffmpeg 解码 16k mono PCM。VOICEPRINT_SPEC 标注输入容器格式 + 服务端转码责任。
- **P1-离线触发时序**：录音上传（`UpdateRecordingURL`）是前端在 EndSession **之后**单独 POST /recording 完成的（录音常在结束后才到）⇒ 离线精修触发点放**"录音上传成功回写之后"**，而非 EndSession。但因 P0-2 主路径走已存 embedding（不依赖录音），离线其实可在 EndSession 后即触发（embedding 已在会中逐段攒好），full.webm 兜底路径才等录音。
- **P1-资源争用**：构建机 4 核与 crawl4ai 共享、且是 docker build 目标（build 时 CPU 打满）。onnxruntime 限 intra 线程（spike 用 //2=2）；给容器设 cpu quota；spike 报告已含并发对照。**S2 决策声纹服务落点**（构建机 vs dev/prod 本机）随上线评估。
- **P1-flag**：在线 diarization 复用现有 ws 端点（不新增路由），flag 守卫在 biz 层 3 个挂载点（ring buffer init / channel push / 离线触发）显式判，`effective = meeting_copilot.enabled && meeting_diarization.enabled`。
- **P2-store 并发**：T7(在线)/T8(离线) 都 UPDATE meeting_segment → store 方法拆到 **diarize 专属 store 文件**再 `ndf-check-disjoint`，否则默认**串行**。
- **P2-隐私措辞**：甲=数据留自有国内基建（音频已从 dev/prod 本机到构建机，非"不离宿主机"）；乙（阿里云文件 diarization）=给第三方。甲优于乙成立但勿夸大隔离度。乙仅调试期交叉校验，不进热路径。
- **ONNX 同源**：spike 用 CosyVoice2-0.5B 内置 `campplus.onnx`（27MB/192维，证据强指同源）。**生产投产前用 3D-Speaker 官方 `export_speaker_embedding_onnx.py` 重导出（零疑虑）+ 把 .onnx checksum 固定进构建产物**，不每次联网拉。

## 5. S2 关键决策摘要

- **D1 引擎**：CAM++ + 直接 onnxruntime CPU（不引 torch/modelscope/funasr 框架，生产用 kaldi-native-fbank 或自实现 80-mel+CMN）；无状态 HTTP 微服务跑构建机新端口（如 11236），iptables/安全组复用 crawl4ai harden 模板；Go 侧 `internal/pkg/voiceprint/` 客户端，非 LLM 不经 aiservice。
- **D3 在线聚类放 relay 内存**（声纹服务无状态）；质心 rawSum 累加、取用时归一化；只非 provisional 段更新质心；灰区 [TAU_NEW,TAU_MATCH) 挂 best 标 provisional 不更新质心。
- **D6 计费**：声纹 userID=0 不计费（非 LLM 不经 gateway，沿用 internalCallCtx）。
- **D8 离线**：自建全局 AHC 重聚类（复用同一 embedding 空间，零对齐税、近零成本、不出境）为**唯一生产路径**；阿里云文件 diarization 仅调试期交叉校验。
- **D9 参数初值**（dev 验收用真实会议校准）：TAU_MATCH 0.55 / TAU_NEW 0.45 / STICKY_MARGIN 0.04 / MIN_DUR_MS 700 / MIN_RMS -45dBFS / MAX_SPEAKERS 8 / AHC 余弦距离阈值 0.55 / dim 192。

## 6. DB 变更（需 migration，dev/prod 手工 SSH 执行）

```sql
ALTER TABLE meeting_segment
  ADD COLUMN online_speaker_id  INT NULL,
  ADD COLUMN online_provisional TINYINT(1) NOT NULL DEFAULT 0,
  ADD COLUMN final_speaker_id   INT NULL,
  ADD COLUMN speaker_confidence FLOAT NULL;

CREATE TABLE IF NOT EXISTS meeting_speaker (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  meeting_id BIGINT NOT NULL,
  cluster_id INT NOT NULL,
  display_label VARCHAR(32) NOT NULL,
  color_index INT NOT NULL,
  created_at DATETIME NOT NULL,
  UNIQUE KEY uk_ms_meeting_cluster (meeting_id, cluster_id),
  KEY idx_ms_meeting (meeting_id)
);

CREATE TABLE IF NOT EXISTS meeting_segment_embedding (   -- 离线主路径前提（P0-2）
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  meeting_id BIGINT NOT NULL,
  segment_id BIGINT NOT NULL,
  embedding  BLOB NOT NULL,        -- float32×192 packed
  created_at DATETIME NOT NULL,
  UNIQUE KEY uk_mse_segment (segment_id),
  KEY idx_mse_meeting (meeting_id)
);

ALTER TABLE meeting_session
  ADD COLUMN speaker_count INT NULL,
  ADD COLUMN diarization_status VARCHAR(20) NOT NULL DEFAULT 'none';  -- none/online/refining/done/failed
```
GORM 用 `*int`/`*float32` 对应 NULL。`online_provisional`/`diarization_status` 无 `default:true`，规避 §database.md 的 `default:true` bool Create 坑。
展示规则（前端）：`display = final_speaker_id(经 meeting_speaker 映射) ?? online_speaker_id(字母A/B/C临时) ?? '发言人?'`。

## 7. S3 原子任务清单（跨仓库 R=server / V=web / VP=声纹服务）

> 每 task 后并行双 Sonnet review（Spec + Quality）。非 bug-from-customer，不强制 repro-first；但聚类/对齐 biz **必须配 Go 单测**。

**阶段 A 声纹服务（VP，与 B 完全并行 Tier-2）**
- T1 [VP] 服务骨架：onnxruntime CPU 载 campplus.onnx，`/embed`(PCM→192-d，80-mel fbank+CMN，valid 短段过滤)+`/healthz`，FastAPI 单文件 + Dockerfile(CPU base，腾讯云 mirror，**装 ffmpeg**)。
- T2 [VP] `/diarize`：服务端 VAD+滑窗 embedding+全局 AHC（开集自动估簇数），ffmpeg 解码 webm/opus；audio_url 自取 COS + audio_b64 兜底。
- T3 [VP/部署] 构建机托管：iptables DOCKER-USER(仅 dev/prod /32 入站、出站放 COS、封内网/metadata，参 crawl4ai harden.sh)+安全组+entrypoint；config_dev/prod 加 `voiceprint.base_url`(prod 留空运维另配，禁改 config_prod.yaml)；cpu quota。

**阶段 B 后端数据层（R，与 A 完全并行 Tier-2）**
- T4 [R] Migration（§6 全部）+ GORM model 加字段/新 model/TableName/索引。
- T5 [R] `internal/pkg/voiceprint/` 客户端：`Embed`/`Diarize`，httpclient 连接池+超时+**软错误**(超时返回 valid=false 绝不 error 杀流程)；Go 单测 mock HTTP 覆盖软降级。

**阶段 C Relay 在线链路（R，串行于 B）**
- T6 [R] Per-session PCM ring buffer（**每帧 copy**，P0-1）+ 分段切片投非阻塞 channel（P1），flag 守卫。单测：复用底层 buffer 场景切片字节正确。
- T7 [R] 在线增量聚类 `biz/meeting/diarize/online.go`（双阈值迟滞+provisional+时长加权+sticky）；worker 消费 channel→T5 Embed→聚类→UPDATE online_speaker_id+存 embedding→ws 回推。单测：3 说话人序列分簇 + 软降级 speaker_id 留空。

**阶段 D 离线精修（R，依赖 B）**
- T8 [R] `biz/meeting/diarize/offline.go`：**主路径=拉已存 embedding 全局 AHC 重聚类**（P0-2）→ final_speaker_id + meeting_speaker（出场序稳定编号+color_index）；full.webm→/diarize 兜底；更新 diarization_status。触发：录音上传成功后（兜底路径）/ EndSession 后即可（主路径）。单测：embedding 集合→稳定编号 + 软降级。store 方法落 diarize 专属文件（与 T7 check-disjoint，否则串行）。
- T9 [R] Controller/查询：详情/转写返回有效 label(final→online→空)+meeting_speaker 映射；纪要按 final 归并；新端点注册 router.go。

**阶段 E 前端（V，契约定稿后与 C/D 并行起步 Tier-2）**
- T10 [V] 转写每段说话人色标（A/B/C 取色板+color_index，provisional/低 conf 弱化，空值灰标"发言人?"），4 状态，flag `VITE_ENABLE_MEETING_DIARIZATION`。
- T11 [V] 会后校正体验：`diarization_status=refining` 顶部"正在校正说话人…"；"初步识别，会后将自动校正"提示；final 到达后 A/B/C→1/2/3 自动切；纪要按说话人归并；可选会后手动重命名。

**阶段 F 验证（S5 策略）**
- T12 后端聚类/对齐=Go 单测（确定性回归保护）；端到端=gstack /qa 在 dev 走真实会议。关键路径：①实时转写带色标 ②多人轮流标签切换 ③**声纹服务停→转写不受影响+灰标兜底（软降级，最关键）** ④结束→纪要按发言人归并+标签精修到 final。不涉支付/权限/会员（userID=0+flag 休眠），按规则10 不强制 Playwright E2E；诚实声明 gstack /qa 无持久化回归。

**并行性**：A(VP) ∥ B(R 数据层) ∥（契约定稿后）E 前端脚手架 = Tier-2 跨仓库 disjoint，免 check-disjoint。同仓 B→C→D 串行。T7/T8 store 方法拆 diarize 专属文件后可 Tier-3（先 ndf-check-disjoint），否则串行。

## 8. 现实风险

重叠语音/抢话=单麦物理硬限（标低置信+provisional 不建模，产品话术明示"辅助参考、重叠场景准确率下降"）；中文短分段质量下降（MIN_DUR_MS 降权+离线更大窗重抽）；在线 ID≠离线 ID 是设计预期（A/B/C→1/2/3 一次性切）；声纹不可用=channel 丢+超时软降级，转写绝不受影响；离线重试幂等（meeting_speaker 出场序编号须幂等，重试不漂移）；domain shift（CN-Celeb 训练 vs 会议室近场单麦，0.55/0.45 是借来起点，真实语料校准）。

## 9. flag / 可删性

后端 `features.meeting_diarization.enabled`（config_*.yaml，默认 OFF）+ 前端 `VITE_ENABLE_MEETING_DIARIZATION`。整删=删 `internal/pkg/voiceprint/`+`biz/meeting/diarize/`+一个 migration+下声纹容器+删前端 speaker 子组件。
