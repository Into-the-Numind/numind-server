# 小红书选题采集插件 (xhs-collector) — S3 实施计划 (v2, 已过原子性 review)

> 依据 design.md v2。每 task 可独立构建+验证。后端(numind-server)先于前端(numind-web-v3),插件最后。
> v2 据 S3 reviewer 修 4 个 P1:拆 T6→T6a/T6b、ext-token 并入 T7、enrichOne wiring 归 T4/T5、补计费验收点 + T5 repro commit 顺序。

## T1 — 数据模型 + migration
- **文件**:`internal/pkg/model/xhs_topic.go`(新)、`migrations/<ts>_create_xhs_topic_note.sql`(新)
- **内容**:`XhsTopicNote`(design §2 全字段含 `content_hash`/`collected_at`/`crawled_at`/`enrich_status`/`*string VideoTranscript`);`TableName()`;索引 uk_xtn_user_note、idx_xtn_user_crawled、idx_xtn_enrich、idx_xtn_hash、idx_xtn_published;migration `CREATE TABLE IF NOT EXISTS`。
- **验收**:`go build` 过;字段/tag/索引与 spec 一致;migration 幂等。

## T2 — store 层
- **文件**:`internal/numind/store/xhs.go`(新)+ store 接口注册
- **内容**:`IXhsStore`:`UpsertNote→(hashChanged bool)`、`ListNotes(userID,filter,offset,limit)`(WHERE user_id)、`GetNote(userID,id)`、`DeleteNote(userID,id)`、`UpdateEnrichStatus`、`UpdateEnrichResult`、`GetByIDs(userID,ids)`。
- **依赖**:T1
- **验收**:store 单测(in-memory sqlite):upsert 新建/同 hash 标志 false/变 hash true;list 分页+user 隔离;get/delete 跨 user 取不到。

## T3a — biz ingest + 字段校验
- **文件**:`internal/numind/biz/xhs/xhs.go`(新)、`biz/xhs/ingest.go`(新)
- **内容**:`Ingest(userID, []NotePayload)`:校验(≤50、xhs_note_id 必填、**content/video_transcript 等 text >64KB→ErrBind**、comments≤10/单条 text≤200 截断)→ `content_hash=SHA256(title+content+video_url)` → `store.UpsertNote` → **hash 变/新增才置 pending+投递富化;hash 不变不投递不扣分**。
- **依赖**:T2
- **验收**:单测——重复 ingest 同笔记(hash 不变)不重置 enrich_status、不投递(**防重复扣分回归**);hash 变则投递;**text >64KB 被拒(ErrBind)**;comments 截断。

## T3b — 富化框架(worker pool,不含 AI/ASR 本体)
- **文件**:`biz/xhs/enrich.go`(新)
- **内容**:`enrichQueue chan enrichJob`(buffered)+ N worker(`viper xhs.enrich_workers` 默认5);独立 `ffmpegSem`(默认2);每 job:`defer recover()`(panic→log+UpdateEnrichStatus failed)、**detached ctx** `context.WithTimeout(context.Background(),5m)`(抽 userID 透传)、**执行前确认 enrich_status==pending(并发重复扣分二次保护)**;调 `enrichOne`(本 task stub:enriching→done)。
- **依赖**:T3a
- **验收**:单测——worker 数受限;job 内 panic 不崩进程且置 failed;detached ctx 在原 ctx cancel 后仍写库;**并发两次投递同 id 只富化一次(enrich_status==pending 保护)**。

## T4 — AI 分析(+ 接入 enrichOne 分析段)
- **文件**:`internal/pkg/aiservice/profile/constants.go`(改:加 `XhsNoteAnalyze` **+ allTaskIDsList**)、`migrations/<ts>_seed_xhs_analyze.sql`(新:task_profile→deepseek-v4-flash + **pricing_rule token 价**)、`biz/xhs/analyze.go`(新)
- **内容**:`analyzeNote(ctx,userID,*note)`:`ctx=billing.WithBilling(ctx,userID,"xhs_note_analyze")`→`aiservice.Chat(profile.XhsNoteAnalyze,{Temp0.3,MaxTokens800})`;prompt(系统="小红书选题分析师",输入 title+content+transcript+tags,content≤4000/comments≤2000 截断)→JSON 6字段解析(剥md,失败降级 ai_one_line);写回。**在 enrichOne 中接入此分析段(wiring 归本 task,非 T6)**。
- **依赖**:T3b
- **验收**:单测(mock chatFn)——解析6字段;JSON 异常降级;**billing.WithBilling 已设**;**pricing_rule seed 行存在且 token 价非零(SQL assert 或 mock pricing 查询)**;Langfuse trace 建立。

## T5 — ASR 视频转写 + 扣费(高风险,**repro-first 强制 commit 顺序**)
- **文件**:`biz/xhs/transcribe.go`(新)、`config_dev.yaml`(加 `xhs.asr_credits_per_second`)
- **commit 顺序(规则11/计费纪律,强制)**:**commit1 = `test(repro): xhs asr charges actual seconds & monitor/meeting unchanged`(此时 FAIL)**;**commit2 = 实现**(测试转 PASS);两 commit 分开,测试永久留存。
- **内容**:`downloadVideoFromURL`(直接 HTTP GET,**不经 xhs-service**)→ ffmpeg 抽 wav(走 T3b ffmpegSem)→ `aiservice.ASR(profile.MonitorTranscribe)`;**计费在 biz 层**:前 `Reserve(min(估时,600s)×费率)`(不足→insufficient_credits 跳过),后 `Reconcile(ceil(DurationSeconds×费率))` call 级取整;**不动 gateway/context_budget**;直链 4xx→partial+transcript NULL。**在 enrichOne 接入 ASR 段(wiring 归本 task)**。
- **依赖**:T3b
- **验收**:repro 回归——① xhs 按实际秒扣;② **monitor/会议副驾 ASR 仍不扣(证明没碰共享 gateway)**;③ 直链失效→partial。
- ⚠️ credit_service Reserve/Reconcile 不支持显式额→退化为 ASR 后单阶段 credit_transaction 扣实际秒(原则不变)。

## T6a — controller CRUD + router
- **文件**:`controller/v1/xhs/notes.go`(新)、`router.go`(改:注册 `/v1/xhs/*`)
- **内容**:notes POST(调 biz.Ingest,**请求体禁含 user_id**,user_id 取 `c.GetUint("userID")`)、GET list、:id GET、:id DELETE;controller 只绑定+取 userID+调 biz;≤50 与 text 64KB 校验(复用 T3a 或 controller 层)。
- **依赖**:T3a(端点可用 stub enrichOne 构建/测;端到端富化需 T4/T5 完成,见 DAG)
- **验收**:端点注册;**user 隔离(A 取/删不到 B)**;≤50 与 64KB 校验返回 ErrBind;`go test ./... && task lint`。

## T6b — export CSV
- **文件**:`controller/v1/xhs/export.go`(新)、`biz/xhs/export.go`(新)
- **内容**:`POST /v1/xhs/notes/export {ids≤200}`→`GetByIDs(userID,ids)`→拼 CSV(源+6分析字段)→COS→`GenerateSignedDownloadURL`(1h)。
- **依赖**:T6a(router)+ T2(GetByIDs)
- **验收**:CSV 含选中记录字段;user 隔离;下载链接可用;ids>200 拒。

## T7 — ext-token endpoint + scope 最小权限 + tech debt 登记
- **文件**:`biz/user/user.go`(改:generateWebToken 加 scope claim 变体)、`controller/v1/xhs/ext_token.go`(新)、`pkg/token`/middleware(改:scope=xhs 仅放行 /v1/xhs/*)、`.ndf/decisions/xhs-collector/0001-ext-token-no-db-revocation.md`(新)
- **内容**:`GET /v1/xhs/ext-token`(带现有 web JWT)→换发带 `scope:"xhs"` 的 token(7天);中间件对 scope=xhs token 在非 `/v1/xhs/*` 路由 403;**登记 tech debt ADR(无 DB 吊销)**。
- **依赖**:T6a
- **验收**:单测——scope=xhs token 打 /v1/sop/* 被拒、打 /v1/xhs/* 放行;无 scope 旧 token 不受影响;ADR 文件存在。

## T8 — 前端选题库(numind-web-v3)
- **文件**:`src/api/xhs.ts`、`src/stores/xhs.ts`、`src/views/xhs/XhsTopicList.vue`、详情抽屉、`XhsInstall.vue`、`ConnectExtension.vue`、`router`+菜单(新/改)
- **内容**:列表页(DataTable 服务端分页+筛选+排序+4状态;enrich_status 行级:enriching转圈/partial tooltip 区分视频过期vs转写失败/insufficient_credits 角标+InsufficientCreditsDialog);详情抽屉(源+6分析+评论);导出按钮(未选禁用/≤200/loading/防重复/1h提示);安装页(COS下载+图文+视频引导+授权入口);/connect-extension(CSP script-src 'self')。
- **依赖**:T6a/T6b(API 契约 §3 已锁,**跨仓库 Tier2 可与后端并行**)
- **验收**:`npm run lint && npm run type-check`;gstack /qa 本地验 4 状态+授权页。

## T9 — 浏览器插件(numind-web-v3/extension/)
- **文件**:`extension/{manifest.json,content.js,background.js,popup.{html,js,css},icons/}`(新)
- **内容**:移植 plugin3.2.1 采集(DOM+`__INITIAL_STATE__` 视频直链,作者粉丝读不到置0不额外请求)+**未登录检测**(禁浮标+提示);background(token storage+POST /v1/xhs/notes+401处理);popup(授权状态/已采集数/打开选题库);manifest host_permissions+`externally_connectable` 精确域名;onMessageExternal 校验 sender.origin。
- **依赖**:T6a(API)+T7(授权)
- **验收**:加载 Chrome;笔记页采集锁定字段(解析函数单测+HTML fixture);未登录禁用;**401 三步(①清token ②content.js浮标切未授权 ③popup高亮重授权)各验收**。

## T10 — S5 验证策略执行(NDF 规则10)
- **方式**:后端 `go test ./... && task lint`(全绿,含 T3a/T3b/T4/T5/T7 回归单测)+ gstack /qa 本地(列表/授权页)+ 插件手动加载验证。
- **关键路径**:① content_hash 不重复扣分 ② **enrich_status==pending 并发保护** ③ AI 分析扣分+解析 ④ **ASR 扣实际秒+monitor/会议不扣回归** ⑤ user 隔离 ⑥ 列表4状态 ⑦ scope 限制 ⑧ 插件采集字段 ⑨ 401 三步。
- **诚实声明**:插件靠真实登录态无法 headless E2E,无自动回归保护(解析函数有单测)。

## 依赖 DAG
```
T1→T2→T3a→T3b→{T4, T5}
T3a→T6a ; {T4,T5}→T6a(端到端富化) ; T6a→T6b ; T6a→T7
T6a/T6b→T8(前端,跨仓库可Tier2并行) ; T6a+T7→T9(插件)
全部→T10
```
- **Tier**:numind-server 多 task 按依赖串行;高风险计费 T4/T5 单独串行做、双审、T5 repro-first;T8(web-v3)与后端跨仓库 Tier2 并行(API 已锁);T9 依赖 T6a+T7。

## S5 验证策略(S3 gate 已审)
见 T10。ASR 扣费高风险→Go 单测持久化回归(非一次性 /qa);插件无法 headless→解析函数单测+手动加载(诚实声明无自动回归)。关键路径全列于 T10。
