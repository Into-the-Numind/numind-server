# 文档系统（Document System）v1 — 技术设计 Spec

> 关联：requirement.md（S0）、proposal.md（S1）
> 轨道：Standard | 仓库：numind-server + numind-web-v3 | 隔离：feature flag 休眠
> 本 spec 是权威。S4 编码必须实现 spec 全部内容。

---

## §0 S2 决策结论（6 项待决全部拍板）

| # | 决策项 | 结论 | 理由 |
|---|--------|------|------|
| 1 | md→docx/pdf 导出 | **复用 `sandbox.Pool` + pandoc**（新建 DocumentExportService） | 沙箱已解耦可独立调用；pandoc 一工具搞定 md→docx + md→pdf；skill 镜像独立 CI/CD 不碰主应用部署 |
| 2 | WYSIWYG 库 | **Milkdown（@milkdown/crepe + @milkdown/vue）** | markdown 原生序列化，开箱 WYSIWYG 工具栏；无头库不违反"禁 UI 框架" |
| 3 | docx→md 解析链 | **direct（md/txt）→ html-to-markdown（html）→ DocumentParser/MarkItDown（docx）→ qwen-long（兜底）** | 复用现有 parser；qwen-long 贵仅兜底 |
| 4 | document 表 schema | 见 §2 | 按 `(user_id, source_object_key)` 唯一键懒建档 |
| 5 | 自动保存 | **debounce 1.5s + 失焦 + 关闭前；last-write-wins（updated_at）** | 单用户场景足够；协作是 v2 |
| 6 | content_md 上限 | **2MB**（MEDIUMTEXT），超限 400 ErrDocumentTooLarge | 防滥用 |

**新增依赖**：
- 沙箱 skill 镜像 requirements/apt 增 `pandoc`（独立 CI/CD，`scripts/docker/skill-image/`，**不动主应用 Dockerfile**）。
- 前端增 `@milkdown/crepe` `@milkdown/vue` `@milkdown/kit`（懒加载编辑器模态，控 bundle）。

---

## §1 架构总览

```
┌─ agent 对话 (现状, 不改生成链路) ─────────────────────────┐
│  AI 生成文件 → uploadGeneratedFile → COS agent-outputs/  │
│  URL 嵌入 final answer markdown                          │
└──────────────────────────────────────────────────────────┘
            │ 前端 extractArtifacts() 抠出 {filename,url,mime}
            ▼
┌─ 新增: 文档系统 (纯附加, feature flag 休眠) ──────────────┐
│  AgentArtifactItem 卡片                                   │
│   └─[打开编辑]──▶ POST /v1/documents/open                 │
│                    │ 派生 object_key(限 agent-outputs/)   │
│                    │ 查 (user_id, source_object_key)      │
│                    │  ├ 命中 → 返回(含上次编辑 content_md) │
│                    │  └ 未命中 → 拉 COS → 解析 md → 建档   │
│                    ▼                                       │
│  DocumentEditorModal(Teleport 全屏) + Milkdown WYSIWYG    │
│   ├ 编辑 → debounce → PUT /v1/documents/:id (自动保存)    │
│   └ 下载 → GET /v1/documents/:id/export?format=md|pdf|docx│
│              md: 直接返回 | pdf/docx: sandbox+pandoc      │
└──────────────────────────────────────────────────────────┘
```

**隔离三层防护**：
1. 后端：`/v1/documents/*` 整组套 `middleware.FeatureFlag("features.document_system.enabled")`（viper 默认 false）。
2. 前端：`VITE_ENABLE_DOCUMENT_SYSTEM === 'true'` 控"打开编辑"按钮渲染。
3. DB：新表独立，flag OFF 时表存在但无路由可达；migration 手动跑（遵 dev-deploy-migration-gap）。

---

## §2 数据库设计（numind-server）

### 2.1 migration: `migrations/{YYYYMMDD_HHMMSS}_create_document.sql`
```sql
-- 文档系统 v1：AI 生成产物的可编辑文档（feature flag: features.document_system.enabled）
CREATE TABLE IF NOT EXISTS `document` (
  `id`                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`           INT UNSIGNED    NOT NULL,
  `parent_user_id`    INT UNSIGNED    NULL,                 -- B2B2C 上下文快照, v1 不用于共享
  `source_object_key` VARCHAR(512)    NOT NULL,             -- COS object key (限 agent-outputs/ 前缀), 稳定标识
  `source_run_id`     BIGINT UNSIGNED NULL,                 -- 弱关联 agent_run, 无 FK(避免耦合)
  `source_mime`       VARCHAR(128)    NULL,
  `title`             VARCHAR(255)    NOT NULL,
  `content_md`        MEDIUMTEXT      NOT NULL,             -- 可编辑 markdown, <=2MB
  `parse_method`      VARCHAR(32)     NOT NULL DEFAULT 'direct', -- direct|html|markitdown|qwen_long
  `created_at`        DATETIME(3)     NOT NULL,
  `updated_at`        DATETIME(3)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_doc_user_source` (`user_id`, `source_object_key`),
  KEY `idx_doc_user_updated` (`user_id`, `updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;
```
> 无 FK 到 agent_run（隔离 + 弱关联）；无 `default:true` bool（避开 GORM 坑）。
> **【P2 修复】`ROW_FORMAT=DYNAMIC`**：`source_object_key VARCHAR(512)` utf8mb4 最长 2048 字节，进 unique 索引会撞 InnoDB COMPACT 行格式的 767 字节索引限。DYNAMIC（MySQL 8 默认）限 3072 字节，容纳无虞。显式声明以防旧实例默认 COMPACT。

### 2.2 GORM model: `internal/pkg/model/document.go`
```go
type Document struct {
    ID              uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    UserID          uint      `gorm:"not null;uniqueIndex:uniq_doc_user_source,priority:1;index:idx_doc_user_updated,priority:1" json:"user_id"`
    ParentUserID    *uint     `gorm:"column:parent_user_id" json:"parent_user_id,omitempty"`
    SourceObjectKey string    `gorm:"size:512;not null;uniqueIndex:uniq_doc_user_source,priority:2" json:"source_object_key"`
    SourceRunID     *uint64   `gorm:"column:source_run_id" json:"source_run_id,omitempty"`
    SourceMime      string    `gorm:"size:128" json:"source_mime,omitempty"`
    Title           string    `gorm:"size:255;not null" json:"title"`
    ContentMD       string    `gorm:"type:mediumtext;not null" json:"content_md"`
    ParseMethod     string    `gorm:"size:32;not null;default:direct" json:"parse_method"`
    CreatedAt       time.Time `gorm:"index:idx_doc_user_updated,priority:2" json:"created_at"`
    UpdatedAt       time.Time `json:"updated_at"`
}
func (Document) TableName() string { return "document" }
```
- **【P0-AutoMigrate 修复】条件 AutoMigrate**：`helper.go:initDatabase()` 的 AutoMigrate 在**所有环境（含 prod）无条件执行**。若把 `model.Document{}` 直接加进全局 AutoMigrate 列表，prod 下次部署即建表 → 破坏"prod 零 schema 影响"。因此 **仅当 `viper.GetBool("features.document_system.enabled")` 为 true 时才把 Document 加入 AutoMigrate**（flag 与建表对齐：prod flag off → 表永不在 prod 出现）。本地/dev flag on → 自动建表。
- dev/prod 启用本功能时仍手动跑 `create_document.sql`（遵 dev-deploy-migration-gap；CI 不跑 migration）。两条路径产出同一 schema（注意 migration 的 ROW_FORMAT=DYNAMIC）。

### 2.3 Store: `internal/numind/store/document.go`
```go
type IDocumentStore interface {
    GetByUserAndSource(ctx context.Context, userID uint, objectKey string) (*model.Document, error) // ErrRecordNotFound 显式处理
    GetByID(ctx context.Context, id uint64) (*model.Document, error)
    Create(ctx context.Context, d *model.Document) error
    UpdateContent(ctx context.Context, id uint64, contentMD, title string) error // 用 map 形式 Updates 或 Update 显式列
}
```

---

## §3 后端 biz/controller/router（numind-server）

### 3.1 biz: `internal/numind/biz/document/`
- `service.go` — `DocumentService`：
  - `OpenFromArtifact(ctx, userID, parentUserID, req OpenReq) (*DocumentDTO, error)`
    1. `objectKey := deriveObjectKey(req.SourceURL)`（解析 URL path，去 query）。**【P0-IDOR 修复】校验 key 前缀必须严格等于 `agent-outputs/{callerUserID}/`**（COS key 格式 = `agent-outputs/<userID>/<unixnano>-<filename>`，见 tool_create_helpers.go:92）。前缀不匹配 → `ErrDocumentSourceForbidden`。**只校验 `agent-outputs/` 前缀不够**——会让用户 A 传用户 B 的 key 打开 B 的文件（COS GetObject 按 bucket 不按调用者身份）。必须把 key 的第二段 userID 与 caller 比对。这是首次打开路径唯一的越权防线（`GetByUserAndSource` 只保护重开）。
    2. `GetByUserAndSource` 命中 → 返回（含上次编辑 content_md）【US5】
    3. 未命中 → `bytes := cos.DownloadFromCOS(objectKey)`（**仅 404/NoSuchKey → ErrDocumentSourceExpired**；其它错误如 403 → 通用内部错误，不可误判为过期，见 §3.4）
    4. `contentMD, method := parse(bytes, req.Filename, req.Mime)`（见 §3.2）
    5. 校验 `len(contentMD) <= 2MB`，**超限直接返回 ErrDocumentTooLarge（不截断——静默截断会产出残缺文档）**
    6. `Create` document（title=去扩展名的 filename）→ 返回 DTO
  - `Get(ctx, userID, id)` — 含 **ownership 校验**（d.UserID==userID，否则 ErrDocumentNotFound 不泄露存在性）【US6/AC6】
  - `Save(ctx, userID, id, contentMD, title)` — ownership 校验 + 大小校验 + UpdateContent（last-write-wins）【US3/AC4】
- `parse.go` — 解析链（§3.2）
- `export.go` — `DocumentExportService`（§3.3）
- `objectkey.go` — `deriveObjectKey` + 前缀校验 + 可编辑性判定 `IsEditableMime(mime, filename)`

### 3.2 解析链 `parse(bytes, filename, mime) -> (md, method)`
| 类型判定（mime 优先, filename 扩展名兜底） | 方法 | parse_method |
|---|---|---|
| .md/.markdown, text/markdown, text/plain, .txt | 直接当 markdown/文本 | `direct` |
| .html/.htm, text/html | `html-to-markdown`（go.mod 已有 JohannesKaufmann/html-to-markdown） | `html` |
| .docx, ...wordprocessingml.document | `DocumentParser`（MarkItDown 主 + go-fitz/正则兜底，写 temp file→解析） | `markitdown` |
| docx 且 DocumentParser 失败/返回空 | **v1: 直接 ErrDocumentParseFailed**（保留 `docxFallback` 接口 seam，v1 注入 nil） | — |
| 其他（不可编辑类）| —— | open 返回 ErrDocumentNotEditable（前端本不该让点） |

> **【S4-T3 scope 决策 2026-06-16】qwen-long 兜底 v1 推迟到 v2。** 理由：(1) v1 输入全是 agent 生成的干净 docx，DocumentParser(MarkItDown 主 + go-fitz/正则兜底)足够；(2) qwen-long 跨 controller 层、耦合 DashScope 文件 API、是"兜底的兜底"实际永不触发；(3) 砍掉后 **document-system v1 全程无 LLM 调用**，隔离更干净无计费/trace 负担。代码保留 `docxFallback` 接口 seam，v2 接用户上传脏 docx 时注入 qwen-long 实现（含 §6 trace）。

> **【P1 修复】运行时依赖**：`DocumentParser.Parse()` 内部 `exec.Command("python3", "internal/pkg/parser/document_parser.py", tmpFile)` —— 依赖容器内有 `python3` + `document_parser.py` 脚本随二进制就位。新 document biz 复用它即继承此依赖。S4 须在 CI/构建镜像里验证 `python3 document_parser.py` 可跑；MarkItDown 不可用时降级 qwen-long（仍可工作）。

### 3.3 导出 `DocumentExportService`（注入 `sandbox.Pool` + COS util）
- `Export(ctx, userID, id, format) (filename string, contentType string, data []byte, err error)`
  - ownership 校验。
  - `format=md`：`data=[]byte(content_md)`，contentType `text/markdown; charset=utf-8`，filename `{title}.md`。**不走沙箱**（始终可用）。
  - `format=docx`：sandbox 借用 → 写 `/workdir/input/doc.md` → exec `pandoc /workdir/input/doc.md -o /workdir/output/out.docx` → CopyFrom → bytes。contentType `...wordprocessingml.document`。pandoc 的 md→docx 是业界标准路径。
  - `format=pdf`：**【P1 修复 — pandoc+weasyprint 调用方式】** weasyprint 吃 HTML 不吃 md。两种实现，S4 二选一并**运行时实测**：
    - 优先：`pandoc doc.md -o out.pdf --pdf-engine=weasyprint`（现代 pandoc 内部先 md→html 再交 weasyprint；多数版本可用）。
    - 兜底（更可控）：`pandoc doc.md -o out.html` 再 `weasyprint out.html out.pdf`（或 Python 直接 weasyprint）。
    - **已核实**：skill 镜像**确含 `weasyprint 62.3` + CJK 字体 `fonts-wqy-zenhei`**（研究 agent 读 requirements.txt 确认），中文渲染无虞。缺的只是 `pandoc`（§7 加）。
  - **优雅降级**：`pool.Borrow` 返回 `ErrSandboxDisabled` → pdf/docx 返回 ErrExportUnavailable（友好提示"当前环境暂不支持该格式导出，可先导出 Markdown"）；md 不受影响。
  - **【P1 修复 — pool 争用】** 导出与 agent run 共享同一 sandbox pool（PoolMin=5），并发下有耗尽风险。v1 加**每用户单并发导出守卫**（同用户同时只允许 1 个导出在跑，第 2 个返回 429/稍后重试）；务必 `defer pool.Return`，设导出专用较短超时（≤30s）。后续可评估给导出独立小 pool。
  - 复制 `tool_run_python.go` 的 collectOutput/超时/清理范式。

### 3.4 COS 下载 helper（`internal/pkg/util/cos_uploader.go` 增，现无此函数）
- `DownloadFromCOS(ctx, objectKey) ([]byte, error)`：封装 COS SDK GetObject 或对 `GenerateSignedURLForMethod(GET)` 做一次 http GET。
- **【P1 修复 — 错误判别】** 必须区分错误类型：**仅** HTTP 404 / `NoSuchKey`（用 `cos.IsNotFoundError(err)` 或检查 status 404）→ 返回可判别的 NotFound 错误（biz 映射 ErrDocumentSourceExpired）；403/凭证/网络等其它错误 → 通用内部错误，**不可**误判为"过期"。

### 3.5 controller: `internal/numind/controller/v1/document/document.go`
- 只做绑定/auth 提取/调 biz/格式化（遵 controller 职责边界）。
- `userID := c.GetUint("userID")`；parentUserID 从 ctx 取。
- export 用 `c.Data(200, contentType, data)` + `c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+urlencode(filename))`。

### 3.5b 装配（biz.go）【P0-PoolWiring 修复】
- `sandboxPool` 当前是 `NewBiz()` 的**局部变量**（biz.go:268），未存为 biz 字段。S4 必须：
  1. 在 `biz` struct 加字段 `sandboxPool sandbox.Pool`，`NewBiz` 内 `b.sandboxPool = sandboxPool`。
  2. `DocumentExportService` 构造函数注入 `sandboxPool`（或经 IBiz 传入），**不走** agent 的 PreToolCall hook（那是 agent 专属的预借用机制）。
  3. **在 `IBiz` 接口 + `biz` struct 加 `Document() document.IDocumentService` 方法**（controller 经 `b.Document()` 取）。

### 3.6 router 注册: `internal/numind/router.go`
```go
{
  docCtrl := documentcontroller.NewController(b.Document())
  docGroup := authGroup.Group("/documents")
  docGroup.Use(importMw.FeatureFlag("features.document_system.enabled"))
  {
    docGroup.POST("/open",            docCtrl.Open)    // 打开/懒建档
    docGroup.GET("/:id",             docCtrl.Get)      // 取文档(重开)
    docGroup.PUT("/:id",             docCtrl.Save)     // 自动保存
    docGroup.GET("/:id/export",      docCtrl.Export)   // 导出下载 ?format=md|pdf|docx
  }
}
```
> v1 不做 `GET /v1/documents`（列表）—— 无独立工作区，US5 由 open 的 source_key 命中解决。

### 3.7 errno: `internal/pkg/errno/document.go`
ErrDocumentNotFound / ErrDocumentSourceForbidden / ErrDocumentSourceExpired / ErrDocumentNotEditable / ErrDocumentTooLarge / ErrExportUnavailable / ErrFeatureDisabled(复用)。用预定义码段，不臆造。

### 3.8 config: `features.document_system.enabled`
- `config_dev.yaml` / `config_local.yaml`：`features.document_system.enabled: true`（与 notification_center 同 features 段）。
- `config_qa.yaml` / `config_prod.yaml`：**不加**（viper 默认 false = 休眠）。**禁止改 config_prod.yaml**（硬规则）。

---

## §4 API 契约（双仓库锁定，前端按此 mock/联调）

### POST /v1/documents/open
Req: `{ "source_url": "https://...cos.../agent-outputs/...", "filename": "季度报告.docx", "mime": "application/vnd...document", "run_id": 123 }`
Resp(data): `{ "id": 88, "title": "季度报告", "content_md": "# ...", "source_mime": "...", "source_object_key": "agent-outputs/...", "parse_method": "markitdown", "created_at": "...", "updated_at": "..." }`
错误：403 ErrDocumentSourceForbidden（key 前缀非 `agent-outputs/{自己userID}/`）、**410 ErrDocumentSourceExpired**（源对象 GC/不存在，统一用 410 Gone，不混 404）、422 ErrDocumentNotEditable、400 ErrDocumentTooLarge。

### GET /v1/documents/:id
Resp(data): 同 open 的 document DTO。错误：404 ErrDocumentNotFound（含跨用户）。

### PUT /v1/documents/:id
Req: `{ "content_md": "...", "title": "可选" }`
Resp(data): `{ "id": 88, "updated_at": "..." }`。错误：404、400 ErrDocumentTooLarge。

### GET /v1/documents/:id/export?format=md|pdf|docx
Resp: 二进制流 + `Content-Disposition: attachment; filename*=UTF-8''<urlencoded>`。错误：404、400(format 非法)、503 ErrExportUnavailable（sandbox off 且 format∈{pdf,docx}）。

> 统一响应：错误走 `core.WriteResponse(c, errno.ErrXxx, nil)`；export 成功直接写二进制（非 JSON 信封）。

---

## §5 前端设计（numind-web-v3）

### 5.1 API 层 `src/api/documents.ts`
```ts
export const openDocument = (p: OpenDocReq) => request.post<DocumentDTO>('/v1/documents/open', p)
export const getDocument = (id: number) => request.get<DocumentDTO>(`/v1/documents/${id}`)
export const saveDocument = (id: number, p: SaveDocReq) => request.put(`/v1/documents/${id}`, p)
export const exportDocument = (id: number, format: 'md'|'pdf'|'docx') =>
  request.get(`/v1/documents/${id}/export`, { params: { format }, responseType: 'blob' })
```
类型化 DocumentDTO/OpenDocReq/SaveDocReq。

### 5.2 store `src/stores/documents.ts`（Pinia setup 语法）
- state: `current: DocumentDTO|null`, `loading/saving/error`, `saveState: 'idle'|'saving'|'saved'|'error'`
- actions: `open(req)`、`save(contentMd)`（debounce 1.5s，内部 inflight 守卫 + last-write-wins）、`flushSave()`（关闭/卸载前**用 `navigator.sendBeacon` 或 `fetch({keepalive:true})` 立刻提交未存改动**——普通 XHR 在 tab 关闭时会被中止，丢内容）、`exportAs(format)`（blob→触发下载）、`reset()`
- 错误处理：4 状态；401 由 axios 拦截器处理；业务错误读 message toast（遵 frontend-state 规则）。

### 5.3 编辑器组件
- `src/components/document/MilkdownEditor.vue` — Milkdown(@milkdown/crepe) 封装，`v-model`(markdown 字符串) + `readonly`，mount/unmount 范式参考 CodeMirrorEditor.vue（internalUpdate 守卫防 emit↔watch 环）。
- `src/components/document/DocumentEditorModal.vue` — `Teleport to="body"` 全屏模态（范式借 AgentArtifactItem html-preview-overlay）：顶部 bar（文档名 + 保存状态"已保存/保存中…/保存失败重试" + 下载下拉 md/pdf/docx + 关闭）；主体 MilkdownEditor；4 状态（loading skeleton / empty / error+retry / success）；Esc/遮罩关闭（关前 flush 保存）。
- **懒加载**：模态与 Milkdown 用 `defineAsyncComponent` / 动态 import，避免进 agent 主 bundle。

### 5.4 对话内入口 `src/components/agent/AgentArtifactItem.vue`
- 在文件卡片（134-143 行 file-row）download 按钮旁加 **"打开编辑"** 按钮。
- 显隐条件：`docEnabled && isEditable(artifact.mime, artifact.filename)`，其中 `docEnabled = import.meta.env.VITE_ENABLE_DOCUMENT_SYSTEM === 'true'`，`isEditable` 判定 mime/扩展名 ∈ 可编辑集（md/txt/html/docx）。
- 点击 → 打开 DocumentEditorModal（自包含于 AgentArtifactItem，与现有 html-preview 模态一致），调用 `documentsStore.open({source_url: artifact.url, filename, mime, run_id})`。
- **【P1 修复】** `AgentArtifactItem` 的 `artifact.id` 是 `AgentFinalAnswer.vue:94` 的**渲染循环下标（i），不是后端文档 ID**，禁止当标识用。打开必须传 `source_url`（后端派生 object_key），文档 ID 由 open 响应返回后用于后续 save/export。
- 图片/csv/xlsx/pptx/pdf：**不显示**"打开编辑"，维持现有下载/预览。

### 5.5 feature flag 声明
- `env.d.ts` 增 `readonly VITE_ENABLE_DOCUMENT_SYSTEM?: string`
- `.env.development` 增 `VITE_ENABLE_DOCUMENT_SYSTEM=true`
- `.env.production` **不加**（休眠）。

---

## §6 AI 可观测性（trace 拓扑）
> **【S4-T3 决策更新】v1 全程无 LLM 调用** —— qwen-long 兜底推迟到 v2（见 §3.2 注），解析(direct/html/markitdown)与导出(pandoc)均确定性。故 v1 **N/A**，无需 trace/billing。下文为 v2 接入 qwen-long 时的 trace 拓扑预案：

仅 **qwen-long 兜底解析**分支涉及 LLM（v2）：
- Trace 起点：`DocumentService.OpenFromArtifact` 的 qwen-long 分支 → `langfuse.CreateTrace("document-parse")`，`WithUserID`、`WithTraceInput({document_filename, source_mime})`、tag `document-system`。
- Generation：qwen-long extract → `CreateGeneration`（model=qwen-long，记 prompt/completion tokens）。
- 关键元数据：user_id、source_mime、parse_method、filename。
- 优雅降级：`if tc != nil` 守卫。direct/html/markitdown 路径**不**涉及 LLM（N/A）。
- 走 `aiservice` 统一入口（禁裸调），自动计费+trace+降级。导出 pandoc 是确定性转换，**不涉及 LLM**，无 trace。

---

## §7 部署 / 隔离影响
1. **主应用**（numind-server / numind-web-v3）：纯附加。新表 + 新路由(flag 守) + 新前端组件(flag 守)。合 develop 安全。prod 打 tag 时 features 段无 + VITE 变量无 = 完全休眠，零影响。【AC7/AC8】
2. **沙箱 skill 镜像**：加 `pandoc`（`scripts/docker/skill-image/` 的 Dockerfile/apt，独立 build.sh + 独立 TCR tag），**不碰主应用 Dockerfile/部署链路**。重建一次即可。dev 用即生效。
3. **migration**：手动 SSH 跑 `create_document.sql`（遵 dev-deploy-migration-gap；CI 不自动跑）。flag OFF 时表空置无害。
4. **sandbox 依赖**：导出 pdf/docx 需该环境 sandbox 启用。因文档源自 agent 沙箱产物，能打开即说明沙箱已启用 → 导出可用；若 sandbox off，md 仍可导，pdf/docx 优雅降级。

---

## §8 PRD 覆盖映射（逐条对照，S2 gate 自检）
| PRD | 实现位置 |
|---|---|
| US1 打开编辑按钮 | §5.4 AgentArtifactItem + §5.5 flag |
| US2 WYSIWYG 格式化 | §5.3 Milkdown + §3.2 解析链 |
| US3 自动保存 | §5.2 store debounce + §3.1 Save |
| US4 导出 md/docx/pdf | §3.3 ExportService + §4 export API + §5.1 |
| US5 重开见上次编辑 | §3.1 OpenFromArtifact 命中分支 + §2 唯一键 |
| US6 用户隔离 | §3.1 ownership 校验 + §3.6 user_token |
| AC1 可编辑性判定 | §3.1 IsEditableMime + §5.4 |
| AC2 富文本可编 | §5.3 |
| AC3 懒建档去重 | §3.1 + §2 uniq_doc_user_source |
| AC4 自动保存持久 | §5.2 + §3.1 Save |
| AC5 导出 md+pdf+docx | §3.3 |
| AC6 跨用户隔离 | §3.1 Get/Save ownership |
| AC7 flag 守 | §3.6 + §5.5 |
| AC8 纯附加零回归 | §7.1 |
| 边界(源过期/超大/解析失败/并发/mime缺失) | §3.1/§3.2/§3.3 错误处理 + §2 |

## §9 测试策略前瞻（S3 将细化为独立 task）
- 后端单测（biz）：deriveObjectKey 前缀校验/可编辑性判定/解析链分支/ownership 校验/大小上限；export md 路径（sandbox 用 mock 或 dev 集成）。
- 前端 vitest：documents store（open/save debounce/exportAs blob）、isEditable 判定、AgentArtifactItem 按钮显隐（flag×mime）。
- S5：本地 + dev 浏览器 QA 走 US1-US5 真链路 + 跨用户隔离探针（独立测试账号，征同意）。
- **回归保护**：逻辑层 vitest/go test 持久化；视觉/编辑体验一次性 dev 确认。

---

## §10 S2 独立 Sonnet Review 修复记录（CONDITIONAL_PASS → 已全部并入）
reviewer 逐文件核验代码现状，提 3 P0 + 6 P1 + 6 P2，全部有效，已并入上文：
- **P0-IDOR**（§3.1 步1）：`agent-outputs/` 前缀不足防越权 → 改为严格校验 `agent-outputs/{callerUserID}/`。
- **P0-AutoMigrate**（§2.2）：AutoMigrate 在 prod 也无条件建表 → 改为仅 flag on 才 AutoMigrate Document，保 prod 零 schema 影响。
- **P0-PoolWiring**（§3.5b 新增）：sandboxPool 是 biz.go 局部变量 → 须存 biz 字段 + 注入 ExportService + IBiz 加 Document()。
- **P1**：DocumentParser 的 python3+脚本运行时依赖（§3.2 注）；pandoc+weasyprint 正确调用方式+二选一实测、weasyprint/CJK 已确认在镜像（§3.3）；pool 争用 → 每用户单并发导出守卫（§3.3）；artifact.id 是渲染下标非文档ID（§5.4）；DownloadFromCOS 仅 404→过期（§3.4）。
- **P2**：超限不截断只报错（§3.1 步5）；ErrDocumentSourceExpired 统一 410（§4）；IBiz.Document() 显式（§3.5b）；migration ROW_FORMAT=DYNAMIC 防 767 索引限（§2.1）；sendBeacon/keepalive 关闭前 flush（§5.2）；sandbox 解耦表述精确化（§3.5b）。
- PRD 覆盖：reviewer 确认 US1-6 / AC1-8 全覆盖，无 gap。
- 代码事实核验：middleware.FeatureFlag / sandbox.Pool / html-to-markdown(go.mod) / DocumentParser / 前端 artifact 无稳定 id —— 全部如 spec 所述属实。
