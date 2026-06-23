# 小红书选题采集插件 (xhs-collector) — S2 技术设计 (v2, 已过对抗式 review)

> 依据 requirement.md + proposal.md。v2 修掉 S2 review 的全部 P0 + 关键 P1（评审记录见 §12）。
> 范围:仅小红书 · 数据落有数累积选题库 · 不连飞书 · 不做抖音。

## §1 架构总览

```
[客户浏览器·已登录小红书] Chrome 插件 (numind-web-v3/extension/, MV3)
   ├ content.js  未登录检测 → 采集(改自 plugin3.2.1) → 归一化 payload
   ├ background.js  存 token / POST 有数 / 401→重新授权
   └ popup  授权状态 / 已采集数 / 打开选题库
        │ POST /v1/xhs/notes (Bearer ext-token)
        ▼
[numind-server] controller/v1/xhs → biz/xhs → store
   ├ Ingest(同步,不扣分): 校验 → upsert by (user_id, xhs_note_id) → 比对 content_hash
   │     hash 不变 → 保持 enrich_status,不重新富化(关键:防重复扣分)
   │     hash 变/新增 → enrich_status=pending → 投递富化队列
   ├ 富化(worker pool: buffered channel + N worker, 每 job: defer recover + detached ctx):
   │     ├ AI 分析(扣分): aiservice.Chat(profile.XhsNoteAnalyze) + billing.WithBilling
   │     └ 视频: downloadFromURL → ffmpeg(独立 sem) → aiservice.ASR → biz 层显式 Reserve/Reconcile 扣分
   └ List/Detail/Delete/Export(CSV) 查询(全部 WHERE user_id=<jwt>)
        ▼
[numind-web-v3] 选题库列表页(DataTable 4状态) + 安装引导页 + /connect-extension 授权页
```

**计费原则**:采集/入库/看列表/CSV导出 **不扣**;**仅 AI 分析(LLM) + 视频转写(ASR 云服务) 扣分**。富化异步 best-effort,失败不阻塞入库。

## §2 数据模型

新表 `xhs_topic_note`(model `XhsTopicNote`,`internal/pkg/model/xhs_topic.go`),照 `MonitorNote` 约定。

```go
type XhsTopicNote struct {
    ID        uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
    UserID    uint   `gorm:"not null;index:idx_xtn_user_crawled,priority:1;uniqueIndex:uk_xtn_user_note,priority:1" json:"user_id"`
    XhsNoteID string `gorm:"size:100;not null;uniqueIndex:uk_xtn_user_note,priority:2" json:"xhs_note_id"`
    ContentHash string `gorm:"size:64;index:idx_xtn_hash" json:"-"` // SHA256(title+content+video_url),防重复富化/扣分

    NoteType string `gorm:"size:20;default:'normal'" json:"note_type"` // normal/video
    Title string `gorm:"size:500" json:"title"`
    Content string `gorm:"type:text" json:"content"`
    Tags datatypes.JSON `json:"tags"`
    CoverURL string `gorm:"size:1000" json:"cover_url"`
    NoteURL string `gorm:"size:1000" json:"note_url"`
    PublishedAt *time.Time `gorm:"index:idx_xtn_published" json:"published_at"`
    VideoURL string `gorm:"size:1000" json:"video_url"`
    VideoTranscript *string `gorm:"type:text" json:"video_transcript"` // NULL=无转写(区分直链失效/未转)
    LikeCount int `gorm:"default:0" json:"like_count"`
    CollectCount int `gorm:"default:0" json:"collect_count"`
    CommentCount int `gorm:"default:0" json:"comment_count"`
    ShareCount int `gorm:"default:0" json:"share_count"`
    Comments datatypes.JSON `json:"comments"` // 热门前 ≤10 条,每条 text ≤200 字
    AuthorName string `gorm:"size:200" json:"author_name"`
    AuthorLink string `gorm:"size:500" json:"author_link"`
    AuthorFollowers int `gorm:"default:0" json:"author_followers"` // 取不到=0(已知限制)

    // 6 个 LLM 分析字段
    AITopicAngle string `gorm:"type:text" json:"ai_topic_angle"`
    AIViralReason string `gorm:"type:text" json:"ai_viral_reason"`
    AIBorrowable string `gorm:"type:text" json:"ai_borrowable"`
    AITargetAudience string `gorm:"type:text" json:"ai_target_audience"`
    AITitleFormula string `gorm:"type:text" json:"ai_title_formula"`
    AIOneLine string `gorm:"size:500" json:"ai_one_line"`

    EnrichStatus string `gorm:"size:24;default:'pending';index:idx_xtn_enrich" json:"enrich_status"`
    // pending / enriching / done / partial(视频直链失效或部分失败) / failed / insufficient_credits
    CollectedAt *time.Time `json:"collected_at"` // 客户端采集时刻(payload 传入)
    CrawledAt time.Time `gorm:"index:idx_xtn_user_crawled,priority:2" json:"crawled_at"` // 服务端入库时刻
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}
func (XhsTopicNote) TableName() string { return "xhs_topic_note" }
```

- **去重防重复扣分(P0 修复)**:upsert 时算 `content_hash`。**hash 与已存记录相同 → 只刷新互动数等浅字段,enrich_status 保持原值,不投递富化、不扣分**;hash 不同或新记录 → enrich_status=pending → 投递。富化 worker 执行 LLM/ASR 前再次确认 `enrich_status==pending`(双重保护,防并发重复扣)。
- **collected_at vs crawled_at(P1 修复)**:`collected_at` 客户端传(ISO8601,可空);`crawled_at` 服务端入库时刻(server now)。
- **Migration**:`migrations/<ts>_create_xhs_topic_note.sql`,`CREATE TABLE IF NOT EXISTS`。**dev 需手工 SSH 执行**(CI 不跑 migration)。

## §3 API 契约

用户端,`user_token` 中间件,`/v1/xhs/*`,注册 `router.go`,统一 `core.WriteResponse`。
**全部 user 隔离(P1 安全修复)**:`user_id` 一律从 JWT `c.GetUint("userID")` 取,**请求体禁含 user_id**;所有 store 查询/删除带 `WHERE user_id=<jwt>`。

| 端点 | 方法 | 入参 | 出参 | 计费 |
|---|---|---|---|---|
| `/v1/xhs/notes` | POST | `{notes:[NotePayload]}`(单次 ≤50;见下校验) | `{ingested, ids:[]}` | 不扣 |
| `/v1/xhs/notes` | GET | `page,page_size(≤100),note_type?,keyword?,enrich_status?,sort?` | `{list:[NoteItem],total}` | 不扣 |
| `/v1/xhs/notes/:id` | GET | path id | NoteItem 全字段 | 不扣 |
| `/v1/xhs/notes/:id` | DELETE | path id | ok | 不扣 |
| `/v1/xhs/notes/export` | POST | `{ids:[](≤200)}` | `{download_url}` | 不扣 |
| `/v1/xhs/ext-token` | GET | (Bearer 现有 web JWT) | `{token, expires_at}` | 不扣 |

- **NotePayload 字段**:xhs_note_id(必填)、note_type、title、content、tags[]、cover_url、note_url、published_at、video_url、like/collect/comment/share_count、comments[](≤10,每条 {nick,text(≤200字),likes})、author_name、author_link、author_followers、collected_at。**不含 user_id**。
- **校验(controller 层,P2 安全)**:notes 数 >50 → ErrBind;content/video_transcript 等 text 字段 >64KB → ErrBind;comments >10 截断、单条 text >200 截断。
- **NoteItem 出参(P1 完整性)**:全部展示字段 + `enrich_status` 枚举(pending/enriching/done/partial/failed/insufficient_credits)。
- ingest 同步入库返回,**富化异步投递**(§4)。
- **export = CSV(P0 修复)**:v1 导出为 CSV(纯内存拼,无沙箱依赖),列=源字段+6分析字段;存 COS + `GenerateSignedDownloadURL`(有效期 1h)。**docx 报告推迟到 v2**(create_docx 依赖 agent 沙箱 session,非 agent biz 调用会 softToolError,当前路径不可用)。

## §4 富化与计费(高风险区,repro-first + 双审)

### 4.1 富化编排(P0 修复:有界 + 防崩 + detached)
- **worker pool**:biz/xhs 内 `enrichQueue chan enrichJob`(buffered)+ N 个 worker(N from `viper xhs.enrich_workers`,默认 5)。ingest 只投递 job,不直接起 goroutine。
- **独立 ffmpeg 信号量**:biz/xhs 内声明 `ffmpegSem`(默认 2),不引用 monitor 的 package-private sem。
- **每个 job 执行**:
  - `defer func(){ if r:=recover(); r!=nil { log.Errorw(...); store.UpdateEnrichStatus(id, "failed") } }()` —— 防 panic 崩进程(P0)。
  - **detached context**:`ctx := context.WithTimeout(context.Background(), 5*time.Minute)` —— 不继承 HTTP req ctx(HTTP 返回后不被 cancel,P0)。从原 ctx 显式抽 userID 透传。
  - 执行前确认 `enrich_status==pending`(防重复扣分二次保护)。
- 流程:置 enriching → AI 分析(扣分)→ 若视频:ASR(扣分)→ 置 done/partial。任一步失败记 partial/failed,不阻塞入库。

### 4.2 AI 分析(扣分)
- 新 task:`profile.XhsNoteAnalyze = "xhs.note_analyze"`。**三步注册(P1)**:(1) `profile/constants.go` 加常量 **+ 加入 allTaskIDsList**;(2) migration seed `task_profile` 行(指向便宜模型 **deepseek-v4-flash**,非零价 → 普通 Reserve/Reconcile 扣分,不走 IsFreeModel 豁免);(3) migration seed `pricing_rule`(该 model 的 token 价)。
- 调用:`ctx = billing.WithBilling(ctx, userID, "xhs_note_analyze")` → `aiservice.Chat(ctx, profile.XhsNoteAnalyze, ChatRequest{Messages, Temperature:0.3, MaxTokens:800})`。JSON 输出解析(剥 markdown,失败降级存 ai_one_line=原文截断)。
- prompt 输入做截断保护:content ≤4000 字、comments 合并 ≤2000 字。
- Langfuse:biz 入口建 trace(tags ["xhs-collector"], userID, metadata note_id);Chat 的 generation 由 Tracing 中间件自动记。

### 4.3 ASR 视频转写(扣分 — 全新计费路径,**不动 gateway**)
- **下载(P1 修复)**:新写 `downloadVideoFromURL(ctx, videoURL, dest)` 直接 HTTP GET 小红书 CDN 直链(**不经 monitor 的 xhs-service**);ffmpeg 抽 16kHz 单声道 wav(走 §4.1 ffmpegSem);`aiservice.ASR(profile.MonitorTranscribe, {AudioBytes,"wav","zh"})`(沿用现有 ASR 任务,不改 gateway 行为)。
- **计费(P0 修复 — 关键设计抉择)**:**不在 gateway/ContextBudgetCredits 改**(那里对非 Chat 透传,改动会误伤 monitor/会议副驾)。改为 **xhs biz 层用 credit 服务显式两阶段**:
  1. ASR 前 `Reserve(userID, 保守额)` —— 保守额 = min(已知/估算时长, 上限 600s) × 费率;若完全未知用 600s。费率 `viper xhs.asr_credits_per_second`(默认 0.008 积分/秒 = 0.00008元/秒 ÷ 0.01元/积分)。Reserve 失败(余额不足)→ enrich_status=insufficient_credits,跳过 ASR,不阻塞。
  2. ASR 后读 `ASRResponse.DurationSeconds`,`Reconcile` 到实际 = `ceil(DurationSeconds × 费率)`(**call 级取整,非按秒取整**,P2),多退少补。
  - 若 credit_service 的 Reserve/Reconcile 不支持显式积分额(S4 核实):退化为"ASR 后按实际秒数直接 `credit_transaction` 扣减"(单阶段后付),原则不变(扣实际秒数、gateway 不动)。
- **三套 ASR 保护机制并存(必须在代码注释 + 测试固化,P0 隔离)**:① monitor ASR = ContextBudgetCredits 对非 Chat 透传 → 不扣;② 会议副驾 = 绕 gateway,manual UsageRecord userID=0 → 不扣;③ xhs = biz 层显式 Reserve/Reconcile → 扣。**改本路径不得碰 ①②**。
- **视频直链失效(P2)**:HTTP 4xx → enrich_status=partial、video_transcript=NULL、错误记 Langfuse span error;不扣 ASR 费(没调成)。

## §5 一键授权(复用 JWT + 加 scope,**不建 OAuth**)
1. 插件 popup「授权」→ 开有数 web `/connect-extension`(已登录态)。
2. 页面调 `GET /v1/xhs/ext-token`(带现有 web JWT)→ 返回 **带 `scope:"xhs"` claim 的 user_token**(TTL 7天)。
3. 页面经 `chrome.runtime.sendMessage(EXT_ID, {type:'AUTH', token})` 交给插件。
   - manifest `externally_connectable.matches` 写**精确 origin**(如 `https://youshu.asia/*`),禁通配子域(P1 安全)。
   - background `onMessageExternal` 校验 `sender.origin===精确域名`(P1)。
   - `/connect-extension` 页面上严格 CSP `script-src 'self'`(P1)。
4. 插件存 `chrome.storage.local`,后续 `Authorization: Bearer <token>`。
5. **最小权限(P1 安全)**:user_token 中间件对 `scope=="xhs"` 的 token,**仅放行 `/v1/xhs/*`**,其它 `/v1/*` 路由拒绝(加一个轻量 scope 检查)。
6. **401 UX 全路径(P2)**:background 收 401 → ① 清 storage token;② sendMessage 通知 content.js 浮标切「未授权」;③ popup 显示「授权已失效,请重新授权」高亮授权按钮。不静默失败。

## §6 浏览器插件(numind-web-v3/extension/, MV3)
目录:`manifest.json, content.js, background.js, popup.{html,js,css}, icons/`。
- **未登录检测(P1)**:content.js 先查 `__INITIAL_STATE__.user.userInfo`/登录态选择器;未登录 → 浮标采集按钮禁用 + 提示「请先登录小红书后再采集」,不上报。
- **采集**:移植 plugin3.2.1 手法(扒 DOM + 注入 main world 读 `__INITIAL_STATE__.noteDetailMap[noteId]` 取视频直链,多 fallback)。**作者粉丝数(P2)**:从 `__INITIAL_STATE__` 作者字段尝试读,读不到置 0,**不额外请求作者主页**。归一化成 §3 NotePayload。
- **background.js**(全新):token 管理、POST `/v1/xhs/notes`、401 处理、错误 toast。**删除**原插件飞书/卖家后端/抖音/识别码逻辑。
- **popup**:授权状态、已采集数、「打开选题库」(跳有数 web)。
- host_permissions:`*://*.xiaohongshu.com/*` + 有数 API 域名;`externally_connectable` 精确有数 web 域名。
- API base url 占位,构建按环境注入(dev:$DEV_API_URL)。合规:仅采用户自己浏览器已加载数据、人工触发。

## §7 前端选题库(numind-web-v3)
- **列表页** `src/views/xhs/XhsTopicList.vue`:照 `MarketplaceSubscribed.vue`(服务端分页)。`DataTable`,列=标题/类型/互动数/选题角度/一句话总结/发布时间/enrich_status/操作;筛选(类型/关键词/enrich_status)+排序+分页;4 状态(loading/empty含安装引导/error/success)。
  - **enrich_status 行级 UI(P1)**:enriching=转圈"分析中";partial=tooltip 区分「视频已过期」vs「部分失败」;insufficient_credits=角标 + 点击弹 `InsufficientCreditsDialog`。
  - 行点击看详情(抽屉:全部源字段 + 6 分析字段 + 评论)。
  - **导出按钮交互(P2)**:未选中禁用;最多选 200 条;导出中 loading + 防重复提交;提示下载链接 1h 有效。
- **安装引导页** `src/views/xhs/XhsInstall.vue`:**插件安装包托管(P1)**=COS 静态 URL(构建产出 zip 上传 COS);图文步骤(下载→解压→开发者模式→拖入)+ **视频引导**(外链/内嵌)+「授权」入口。
- **授权页** `/connect-extension`(§5)。
- API `src/api/xhs.ts`、Store `src/stores/xhs.ts`(照 monitor 模式);路由 + 菜单注册。

## §8 S5 验证策略(NDF 规则 10)
- **方式**:后端 Go 单测(biz,持久化回归)+ gstack `/qa`(前端列表/授权页,本地)+ 插件手动加载验证。
- **理由**:ASR 扣费是高风险计费,必须 Go 单测做**持久化回归**;插件靠真实小红书登录态无法 headless E2E → 对**采集解析函数**写单测(喂保存的小红书页 HTML fixture),插件整体手动/gstack 加载验证(诚实声明:插件无自动回归保护)。
- **关键路径**:① ingest upsert + **content_hash 不变不重复扣分(repro 回归)** ② AI 分析传 billing 扣分、JSON 解析 ③ **ASR 扣实际秒数 + monitor/会议 ASR 仍不扣(repro 回归)** ④ user 隔离(A 不能读/删 B) ⑤ 列表 4 状态 ⑥ 授权 scope 限制(scope=xhs token 打非 xhs 路由被拒)。

## §9 复用证据(代码核实,已被 review 校正)
- ASR: `transcriber.go`(download→ffmpeg→`aiservice.ASR` Paraformer);**download 依赖 xhs-service(8100),本功能须新写 URL 直下**;现状不扣(`context_budget.go:397` 透传非 Chat)。
- AI 分析: `analyzer.go`(`aiservice.Chat(profile.MonitorAnalyze)`+JSON)、`briefing.go:119`(`billing.WithBilling` 才扣)。
- task profile: `profile/constants.go` 有 `allTaskIDsList`,新 ID **必须同时加入**。
- docx: `tool_create_docx.go` 依赖 agent 沙箱 session,**非 agent biz 调不通** → export 改 CSV。
- 授权: `controller/v1/user/login.go` + `biz/user/user.go:345` generateWebToken(7天)+ `pkg/token`。
- 计费: `credit/credit_service.go` Reserve/Reconcile;`context_budget.go` 仅对 Chat 扣费;`monitor/transcriber.go` `WithSkipLegacyBilling`。
- 前端: `components/common/DataTable.vue`(4状态)+ `views/marketplace/MarketplaceSubscribed.vue`(服务端分页)+ `api/request.ts`(自动 token+402 派发)。

## §10 任务边界(S3 拆分参考,已按 review 重切)
后端先于前端,插件最后:
1. **model + migration**(`xhs_topic_note` 含 content_hash)
2. **store**(upsert+content_hash 去重 + 分页查询 + user 隔离)
3a. **biz ingest**(校验 + upsert + content_hash + enrich_status 状态机,不含富化本体)
3b. **富化框架**(worker pool + 独立 ffmpegSem + 每 job panic recover + detached ctx,不含 AI/ASR 本体)
4. **AI 分析**(profile 常量+allTaskIDsList + task_profile/pricing seed + prompt + 解析 + billing.WithBilling)
5. **ASR 扣费**(独立高风险 task:downloadVideoFromURL + 复用 ffmpeg/ASR + biz 层 Reserve/Reconcile + repro 回归测试"xhs 扣/monitor 不扣")
6. **controller + router**(notes CRUD + export-CSV + ext-token,user_id 从 JWT)
7. **ext-token scope + 中间件 scope 限制**
8. 前端(api+store+列表页+详情+安装/授权页+路由菜单)
9. 浏览器插件(content 移植采集+未登录检测 + background 接有数+401 + popup)
10. S5 验证执行

## §11 不做 / 边界
- 不做抖音、不连飞书、不做选题库以外产品面。
- **export v1=CSV;docx 推迟 v2**(沙箱依赖)。
- 评论只采热门前 ≤10 条、每条 ≤200 字;阅读数不采;作者粉丝数取不到=0。
- 商店上架不在本期(自托管+引导;同一份插件以后可提交)。
- ext-token v1 = web token + scope=xhs claim(7天);无 DB 吊销(注销/改密不主动失效)→ 登记 follow-up tech debt。

## §12 S2 Review 记录(对抗式 4 维)
- completeness 7/10、feasibility 5/10、billing 4/10、security 5/10。
- **已修 P0**:无界 goroutine→worker pool;异步无 recover→defer recover+detached ctx;重复采集重复扣分→content_hash;ASR 计费走不通 gateway→biz 层显式 Reserve/Reconcile 不动 gateway;docx 非 agent 调不通→改 CSV。
- **已修 P1**:collected_at/crawled_at 语义、enrich_status 出参+行级UI、未登录检测、export v1 scope、安装包托管+视频引导、ASR 下载不依赖 xhs-service、task profile 三步注册、ext-token scope 最小权限、越权防护(user_id from JWT + store WHERE user_id)、401 UX、视频直链失效区分。
- **P2 已纳入**:免费模型不适用(deepseek-v4-flash 非零价)、导出按钮状态、作者粉丝数策略、call 级取整、comments 上限、字段长度校验。
- **follow-up tech debt**:ext-token DB 吊销;docx 报告(v2);插件无自动回归保护。
