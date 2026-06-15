# 通知系统（Notification Center）— 技术设计 Spec

> 版本 1.0 · 2026-06-16 · 本 spec 为权威。代码必须实现 spec 全部内容。多仓库 API 契约（§3）锁定后前后端可并行。

## §0 命名与落点总表

| 仓库 | 路径 | 内容 |
|------|------|------|
| server | `internal/pkg/model/announcement.go` | 5 个 GORM model |
| server | `internal/numind/store/announcement.go` | `IAnnouncementStore` + 实现 |
| server | `internal/numind/biz/announcement/` | biz 服务（announcement.go + survey.go） |
| server | `internal/numind/controller/v1/announcement/` | user + admin controller |
| server | `internal/pkg/errno/notification.go` | 错误码 |
| server | `internal/pkg/middleware/feature_flag.go` | feature flag guard 中间件 |
| server | `migrations/20260616_*_create_notification_center.sql` | 建表 + FK + 索引 |
| server | `config_local.yaml` / `config_dev.yaml` | `features.notification_center.enabled: true`（**不碰 config_prod.yaml**）|
| web-v3 | `src/api/announcements.ts` / `src/stores/announcements.ts` | 用户端 API + store |
| web-v3 | 铃铛入口（全局布局/Sidebar）+ `src/views/NotificationsView.vue` 或抽屉面板 | 通知中心 UI |
| web-v3 | `.env.development` 加 `VITE_ENABLE_NOTIFICATIONS=true` | 前端 flag（prod env 默认不设=隐藏）|
| admin-web | `src/api/announcements.ts` / `src/stores/announcement.ts` | 发布端 API + store |
| admin-web | `src/views/announcement/{List,Form,Stats}View.vue` + `components/announcement/{SurveyQuestionBuilder,SurveyResultChart}.vue` | 管理 UI |

## §1 数据模型（MySQL，utf8mb4_unicode_ci）

> **建表策略**：GORM model 全字段带 tag → 注册进 AutoMigrate 列表（boot 时建表/列/索引，匹配本项目"列靠 AutoMigrate 建"惯例）。FK 约束 + 复合唯一约束写进 migration SQL（AutoMigrate 不可靠处），需上线前手工 SSH 执行（参考 dev-deploy-migration-gap）。

### 1.1 `announcement` — 公告/问卷主表
| 列 | 类型 | 说明 |
|----|------|------|
| id | BIGINT UNSIGNED PK AI | |
| type | VARCHAR(16) NOT NULL DEFAULT 'plain' | `plain` / `survey` |
| title | VARCHAR(200) NOT NULL | |
| content | LONGTEXT NOT NULL | Markdown |
| is_important | TINYINT(1) NOT NULL DEFAULT 0 | 预留（铃铛+可选弹窗），V1 不用弹窗 |
| audience | VARCHAR(32) NOT NULL DEFAULT 'all' | 受众扩展位，V1 只用 'all' |
| status | VARCHAR(16) NOT NULL DEFAULT 'draft' | `draft` / `published` / `archived` |
| published_at | DATETIME NULL | |
| expires_at | DATETIME NULL | NULL=永不过期 |
| created_by | INT UNSIGNED NOT NULL | admin user id |
| created_at / updated_at | DATETIME | |
| deleted_at | DATETIME NULL | 软删（gorm.DeletedAt，带 index）|

索引：`idx_ann_status_pub (status, published_at)`、`idx_ann_type (type)`、`idx_ann_deleted (deleted_at)`

### 1.2 `announcement_read` — 已读回执
| 列 | 类型 | 说明 |
|----|------|------|
| id | BIGINT UNSIGNED PK AI | |
| announcement_id | BIGINT UNSIGNED NOT NULL | |
| user_id | INT UNSIGNED NOT NULL | |
| read_at | DATETIME NOT NULL | 首次已读时间（幂等保留）|
| created_at | DATETIME | |

约束：`UNIQUE uk_annread (announcement_id, user_id)`；FK `announcement_id→announcement(id) ON DELETE CASCADE`、`user_id→user(id) ON DELETE CASCADE`。索引：`idx_annread_user (user_id)`。

### 1.3 `survey_question` — 问卷题目
| 列 | 类型 | 说明 |
|----|------|------|
| id | BIGINT UNSIGNED PK AI | |
| announcement_id | BIGINT UNSIGNED NOT NULL | |
| order_index | INT NOT NULL DEFAULT 0 | 题序 |
| question_type | VARCHAR(16) NOT NULL | `single`/`multi`/`rating`/`text` |
| title | VARCHAR(500) NOT NULL | 题干 |
| options | JSON NULL | single/multi 的选项数组 `["A","B"]`；rating/text 为 NULL |
| rating_max | INT NULL | rating 题：最大分值（2-10）|
| rating_style | VARCHAR(10) NULL | rating 题：`star`/`nps` |
| required | TINYINT(1) NOT NULL DEFAULT 1 | |
| created_at | DATETIME | |

FK `announcement_id→announcement(id) ON DELETE CASCADE`。索引：`idx_sq_ann (announcement_id)`。

### 1.4 `survey_response` — 答卷（一人一份）
| 列 | 类型 | 说明 |
|----|------|------|
| id | BIGINT UNSIGNED PK AI | |
| announcement_id | BIGINT UNSIGNED NOT NULL | |
| user_id | INT UNSIGNED NOT NULL | |
| submitted_at | DATETIME NOT NULL | |
| created_at | DATETIME | |

约束：`UNIQUE uk_sr (announcement_id, user_id)`；FK `announcement_id→announcement(id) ON DELETE CASCADE`、`user_id→user(id) ON DELETE CASCADE`。索引：`idx_sr_ann (announcement_id)`。

### 1.5 `survey_answer` — 单题答案
| 列 | 类型 | 说明 |
|----|------|------|
| id | BIGINT UNSIGNED PK AI | |
| response_id | BIGINT UNSIGNED NOT NULL | |
| question_id | BIGINT UNSIGNED NOT NULL | |
| answer_options | JSON NULL | 选中的选项值数组（single 1 个 / multi N 个）|
| answer_rating | INT NULL | rating 值 |
| answer_text | TEXT NULL | 开放文本 |
| created_at | DATETIME | |

FK `response_id→survey_response(id) ON DELETE CASCADE`、`question_id→survey_question(id) ON DELETE CASCADE`。索引：`idx_sa_response (response_id)`、`idx_sa_question (question_id)`。

## §2 Feature Flag 隔离

- **后端**：config key `features.notification_center.enabled` (bool)。代码默认 OFF（`viper.GetBool` 未设=false）。`config_local.yaml`/`config_dev.yaml` 设 true；**不写 `config_prod.yaml`** → prod 自动 OFF（遵守"不改 config_prod"硬规则；用户后续要上线时自行加一行 + 重启）。
- **中间件**：`middleware.FeatureFlag("features.notification_center.enabled")` — flag off 时 `core.WriteResponse(c, errno.ErrFeatureDisabled, nil)` + `c.Abort()`。挂在 user 与 admin 两个 announcement route group 上。路由保持静态注册（可测）。
- **前端**：web-v3 用 `import.meta.env.VITE_ENABLE_NOTIFICATIONS === 'true'` 控制铃铛显隐。`.env.development` 设 true；`.env.production` 不设 → prod 构建隐藏。admin-web 同理可选（admin 内部用，V1 可常开，但 stats 依赖后端 flag）。

## §3 API 契约（锁定）

> 全部走 `core.WriteResponse` → `{code,message,data}`，前端 interceptor 解出 `data`。所有端点在 feature-flag guard 之后。错误用 §4 errno。

### 3.1 用户端（`/v1`，AuthMiddleware）

**`GET /v1/announcements?page=1&page_size=20`** → `data`:
```json
{ "list": [AnnouncementBrief], "total": 12, "unread_count": 3 }
```
- 仅 status=published 且 (expires_at 为空 或 > now)。按 published_at DESC。
- `AnnouncementBrief`: `{ id, type, title, content, is_important, published_at, expires_at, is_read, is_survey_submitted }`
- `unread_count` = 可见且未读的公告数。

**`GET /v1/announcements/unread-count`** → `{ "unread_count": 3 }`（铃铛轮询用，轻量）

**`GET /v1/announcements/:id`** → `AnnouncementDetail`:
```json
{ "id":1,"type":"survey","title":"...","content":"...","is_important":false,
  "published_at":"...","expires_at":null,"is_read":true,"is_survey_submitted":false,
  "questions":[ { "id":10,"order_index":0,"question_type":"single","title":"...",
                  "required":true,"options":["A","B"],"rating_max":null,"rating_style":null } ] }
```
- 非 survey 时 `questions` 为 `[]`。仅返回可见公告（published/未过期），否则 ErrAnnouncementNotFound。GET **不**改已读状态。

**`POST /v1/announcements/:id/read`** → `{ "unread_count": 2 }`
- upsert 已读回执（幂等，read_at 保留首次）。仅对可见公告有效。

**`POST /v1/announcements/:id/survey/submit`** body:
```json
{ "answers": [ { "question_id":10, "options":["A"], "rating":null, "text":null } ] }
```
→ `{ "submitted": true }`
- 校验：该公告为 survey 且可见；所有 required 题已答；答案形状匹配题型（single→options 恰 1 个；multi→options ≥1；rating→rating 在 [1,rating_max]；text→text 非空若 required）；选项值须属于题目 options。
- 成功：建 survey_response + survey_answer（事务），并顺带 upsert 已读。
- 已提交 → `ErrSurveyAlreadySubmitted` (409)。

### 3.2 管理端（`/v1/admin`，AdminAuthMiddleware）

**`POST /v1/admin/announcements`** body:
```json
{ "type":"survey","title":"...","content":"...","is_important":false,
  "expires_at":null,"status":"draft",
  "questions":[ { "order_index":0,"question_type":"single","title":"...","required":true,
                  "options":["A","B"],"rating_max":null,"rating_style":null } ] }
```
→ 创建的 `AdminAnnouncementDetail`。
- 校验：type=survey 必须含 ≥1 题；single/multi 必须 ≥2 个 options；rating 必须 rating_max∈[2,10] + rating_style∈{star,nps}；text 无 options。
- status 缺省 'draft'；='published' 时置 published_at=now。
- `created_by` = 当前 admin id。

**`GET /v1/admin/announcements?page&page_size&status&type`** →
```json
{ "list":[AdminAnnouncementBrief], "total": 30 }
```
- `AdminAnnouncementBrief`: `{ id,type,title,status,is_important,published_at,expires_at,created_at, read_count, target_count, response_count }`（read_count/response_count 用分组查询，target_count 复用 §5 口径）。

**`GET /v1/admin/announcements/:id`** → `AdminAnnouncementDetail`（含 questions）。

**`PUT /v1/admin/announcements/:id`** body: `{ title?,content?,is_important?,expires_at?, questions? }`
- title/content/is_important/expires_at 任意状态可改；`questions` 仅 status=draft 时可改（published survey 题目冻结，保护已收答卷一致性）。

**`POST /v1/admin/announcements/:id/publish`** → 更新后对象（draft→published, published_at=now；非 draft 报错）。

**`POST /v1/admin/announcements/:id/archive`** → 更新后对象（→archived；用户端不再展示）。

**`DELETE /v1/admin/announcements/:id`** → `{ "deleted": true }`（软删）。

**`GET /v1/admin/announcements/:id/stats`** →
```json
{ "target_count":120, "read_count":80, "read_rate":0.667,
  "response_count":45, "response_rate":0.375 }
```
（response_* 仅 survey）

**`GET /v1/admin/announcements/:id/readers?page&page_size&status=read|unread`** →
```json
{ "list":[ { "user_id":7,"nickname":"...","phone":"...","read_at":"..."|null } ], "total":80 }
```
- status=read：有回执的用户；status=unread：目标用户集合中无回执者（反连接）。

**`GET /v1/admin/announcements/:id/survey-results`** →
```json
{ "response_count":45,
  "questions":[
    { "question_id":10,"title":"...","question_type":"single",
      "option_counts":[ {"option":"A","count":30},{"option":"B","count":15} ] },
    { "question_id":11,"title":"...","question_type":"rating",
      "distribution":[ {"value":1,"count":2},... ], "average":4.2 },
    { "question_id":12,"title":"...","question_type":"text",
      "answers":[ {"user_id":7,"nickname":"...","text":"...","submitted_at":"..."} ] } ] }
```

**`GET /v1/admin/announcements/:id/responses?page&page_size`** →（按用户下钻答卷）
```json
{ "list":[ { "user_id":7,"nickname":"...","submitted_at":"...",
             "answers":[ {"question_id":10,"options":["A"],"rating":null,"text":null} ] } ],
  "total":45 }
```

## §4 错误码（`internal/pkg/errno/notification.go`）
| 名称 | HTTP | Code | 用途 |
|------|------|------|------|
| ErrFeatureDisabled | 404 | `ResourceNotFound.FeatureDisabled` | flag 关闭时所有路由 |
| ErrAnnouncementNotFound | 404 | `ResourceNotFound.AnnouncementNotFound` | |
| ErrAnnouncementNotSurvey | 400 | `InvalidParameter.NotASurvey` | 对非 survey 提交答卷 |
| ErrSurveyAlreadySubmitted | 409 | `FailedOperation.SurveyAlreadySubmitted` | 重复提交 |
| ErrSurveyValidation | 400 | `InvalidParameter.SurveyValidation` | 答案/题目校验失败（SetMessage 附细节）|
| ErrAnnouncementStatus | 400 | `FailedOperation.InvalidAnnouncementStatus` | publish 非 draft / 改已发布问卷题目 等 |

## §5 关键业务规则

- **目标用户口径（target_count）**：`COUNT(user WHERE is_admin = false AND deleted_at IS NULL)`（含子账户；V1 audience='all'）。stats / read_rate / response_rate 实时查。read_rate = read_count/target_count（target_count=0 时 0）。
- **可见性**：用户端可见 = status='published' AND (expires_at IS NULL OR expires_at > now) AND deleted_at IS NULL。
- **已读幂等**：upsert（ON DUPLICATE KEY 或 FirstOrCreate），read_at 保留首次。
- **一人一答**：UNIQUE(announcement_id,user_id) 兜底；biz 先查后插，竞态由唯一键拦。
- **答卷事务**：survey_response + 多条 survey_answer 在一个事务里写。
- **题目冻结**：published 后题目不可改（PUT questions 仅 draft）。
- **GORM default bool 坑**（`.claude/rules/database.md §6`）：`is_important`/`required` 用 `*bool` 入参或 Create 后 fixup，避免 false 被吞。

## §6 前端规格

### 6.1 web-v3（用户端）
- **铃铛**：放全局布局（确认 Sidebar 或 layout header 中始终可见的位置；实现者按实际 layout 组件定位）。带未读红点（unread_count>0 显示，>99 显示 99+）。`VITE_ENABLE_NOTIFICATIONS` 控制显隐。挂载后 + 定时（如 60s）拉 `unread-count`。
- **通知中心**：点击铃铛 → 列表（下拉面板或 `/notifications` 路由）。列表项显示 title + 未读高亮 + 时间 + survey 标记。4 态：loading skeleton / empty("暂无通知") / error(retry) / success。
- **详情**：点列表项 → 详情；进入即 `POST /read`（更新红点）。content 用 `useMarkdown().render()` 渲染（已 DOMPurify 消毒）。
- **问卷作答**：survey 类型详情渲染题目表单（single=radio / multi=checkbox / rating=星或 NPS 按钮 / text=textarea），blur 校验 required，提交 → submit；已提交则展示"已提交"只读态。
- **store**：`useAnnouncementsStore`（**勿与既有 toast `notifications.ts` 混淆**）。state: list/loading/error/unreadCount/current；actions: load/loadDetail/markRead/submitSurvey/refreshUnread。

### 6.2 admin-web（发布端）
- **菜单**：Sidebar 加"公告管理"（lucide Bell 图标）→ `/announcements`。
- **列表**：`AnnouncementListView` 用 **DataTable**（硬规则）。列：标题/类型/状态/发布时间/已读率(read_count/target_count)/操作。分页。行内 action：编辑/发布/归档/删除/查看统计。删除+归档走 **ConfirmModal**（danger）。4 态。
- **表单**：`AnnouncementFormView`（新建/编辑共用）。字段：标题(AppInput)、正文(textarea + "支持 Markdown" 提示)、类型切换(plain/survey)、过期时间(可选)、重要标记。survey 时显示 `SurveyQuestionBuilder`（增删题、选题型、single/multi 编辑选项、rating 设 max+style、text、required、拖动/序号排序）。blur 校验。提交走 store create/update。published 后题目区只读。
- **统计**：`AnnouncementStatsView`。已读率/回收率用 `StatsCard` + div 进度条（无图表库）。已读/未读用户分页列表（DataTable，read/unread tab）。survey 时 `SurveyResultChart`：单/多选用 div bar 显示 option_counts，rating 显示分布 + 平均，text 列出答案。可按用户下钻（responses 列表）。
- **store**：`useAnnouncementStore`（setup 语法，仿 complianceRule.ts）。

## §7 测试 / 验证策略（S5）

- **后端 Go 单测**（biz 层，mock store 或内存 sqlite，持久回归）：
  - 可见性过滤（draft/archived/过期 不可见）
  - 已读 upsert 幂等（重复 read 不增计数）
  - 一人一答（重复 submit 返回 ErrSurveyAlreadySubmitted）
  - 答卷校验（required 缺答 / 题型不匹配 / 选项越界 / rating 越界）
  - read_rate / response_rate 计算（含 target_count=0 边界）
  - feature flag guard（off→ErrFeatureDisabled）
  - GORM default bool（is_important=false / required=false 正确落库，回归 `.claude/rules/database.md §6`）
- **前端单测**（vitest）：announcements store actions（load/markRead 乐观更新/submit）；SurveyQuestionBuilder 增删题逻辑。
- **前端类型/lint**：两端 `npm run lint && npm run type-check` 退出 0。
- **E2E（Playwright，web-v3）**：写 spec 覆盖关键路径——铃铛红点→打开通知中心→读公告（红点减少）→打开问卷→作答提交→再次打开显示已提交。
  - 诚实声明：完整浏览器 E2E 需本地起 server+DB+前端。S5 在本地能起则跑；受并行任务/本地环境约束无法起栈时，E2E spec 文件随代码留存，部署 dev 后（flag 仅 dev 开）由 gstack /qa 验证，向用户说明。
- 这是新功能（非 bug-from-customer），无需先写失败复现测试（Rule 11 不适用）。回归保护靠 Go 单测 + 前端单测 + 留存的 E2E spec。

## §8 任务依赖（S3 细化）
后端 API 契约（本 §3）锁定 → 后端 task 先行（model→store→biz→controller/route）→ 前端两仓库按契约并行（Tier 2，跨仓库 disjoint）。
