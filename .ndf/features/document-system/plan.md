# 文档系统（Document System）v1 — 实施计划 Plan

> 关联：spec.md（S2，权威）。轨道 Standard。仓库 numind-server + numind-web-v3。
> 执行模型：主 session 按依赖顺序实现；每个 task 完成后**并行**双 Sonnet review（spec-compliance + code-quality），P0/P1 修复后才进下一 task。后端(T1-T5)与前端(T7-T9) 因 API 契约已在 spec §4 锁定，可按 Tier 2（跨仓库 disjoint）并行。

---

## 任务总览（10 个 task；代码 task = T1-T9，T10 为 S5 验证策略）

| Task | 标题 | 仓库 | 依赖 | 可并行 Tier |
|------|------|------|------|------------|
| T1 | DB 地基：migration + model + 条件 AutoMigrate + store | server | — | Tier 2 vs 前端 |
| T2 | COS 下载 helper（DownloadFromCOS，404 判别）| server | — | Tier 2 vs 前端 |
| T3 | document biz 核心 + errno + 单测 | server | T1,T2 | — |
| T4 | 导出 service + biz 装配 + 并发守卫 + 单测 | server | T1,T3 | — |
| T5 | controller + router(FeatureFlag) + config flag | server | T3,T4 | — |
| T6 | 沙箱 skill 镜像加 pandoc | skill-image(infra) | — | 独立并行 |
| T7 | 前端 API + store + flag env + isEditable + vitest | web | (契约 spec§4) | Tier 2 vs 后端 |
| T8 | Milkdown 编辑器组件 + 依赖 | web | — | Tier 2 vs 后端 |
| T9 | 编辑器模态 + AgentArtifactItem 入口 + vitest | web | T7,T8 | — |
| T10 | S5 验证策略（规划，非代码）| — | 全部 | — |

依赖图无环：T1→T3,T4；T2→T3；T3→T4,T5；T4→T5；T7→T9；T8→T9；T6 独立。

---

## T1 — DB 地基（server）
**描述**：建 document 表 + GORM model + 条件 AutoMigrate + store 接口与实现。
**文件**：
- `migrations/{YYYYMMDD_HHMMSS}_create_document.sql`（spec §2.1，含 `ROW_FORMAT=DYNAMIC`）
- `internal/pkg/model/document.go`（spec §2.2）
- `internal/numind/store/document.go`（spec §2.3，IDocumentStore + 实现；GetByUserAndSource 显式处理 ErrRecordNotFound；UpdateContent 用 map 形式或显式列，**禁 struct Updates**）
- `internal/numind/helper.go`（AutoMigrate：**仅 `viper.GetBool("features.document_system.enabled")` 时**加 `&model.Document{}`）
- `internal/numind/store/store.go`（或等价聚合处）注册 Document() store 访问器
**验收**：`go build ./...` 过；`go test ./internal/numind/store/...`（如有）过；flag off 时 AutoMigrate 不含 Document（单测 helper 逻辑或代码审查确认）。**【S3-review P2】** flag=true 的 AutoMigrate 正向路径因 config_dev.yaml 在 T5 才落地 → T1 阶段以代码审查确认条件分支正确，T5 dev 启动时集成确认实际建表。
**原子性**：表+model+store 是一个不可分原子单元，完成后可编译。

## T2 — COS 下载 helper（server）
**描述**：`internal/pkg/util/cos_uploader.go` 加 `DownloadFromCOS(ctx, objectKey) ([]byte, error)`，**仅 404/NoSuchKey 返回可判别 NotFound 错误**（biz 映射 410 ErrDocumentSourceExpired），其它错误透传通用错误（spec §3.4）。
**文件**：`internal/pkg/util/cos_uploader.go`
**验收**：`go build` 过；**【S3-review P2 收紧】** 提供可注入的 httpClient 或 `httptest.Server` mock，至少断言 404/NoSuchKey → NotFound err、403 → 非 NotFound err（不接受"注释说明无法测"逃逸）。
**原子性**：单函数，独立可验。

## T3 — document biz 核心 + errno（server）[dep T1,T2]
**描述**：实现 DocumentService（OpenFromArtifact / Get / Save）、parse 链、objectkey 工具、DTO、errno。
**文件**：
- `internal/numind/biz/document/service.go`（OpenFromArtifact 含 **P0-IDOR**：校验 key 前缀严格 `agent-outputs/{callerUserID}/`；Get/Save 含 ownership 校验；超限 ErrDocumentTooLarge 不截断）
- `internal/numind/biz/document/parse.go`（direct→html-to-markdown→MarkItDown→qwen-long 兜底；qwen-long 分支走 aiservice + langfuse trace，spec §6）
- `internal/numind/biz/document/objectkey.go`（deriveObjectKey + 前缀/userID 校验 + IsEditableMime）
- `internal/numind/biz/document/dto.go`
- `internal/pkg/errno/document.go`（ErrDocumentNotFound/SourceForbidden/SourceExpired(410)/NotEditable/TooLarge/ExportUnavailable，用预定义码段）
- 单测：`objectkey_test.go`（IDOR 前缀/userID 校验、IsEditable 判定）、`parse_test.go`（direct/html/markitdown 分支选择）、`service_test.go`（ownership 越权返回 NotFound、超限报错、open 命中复用、源过期映射）
**验收**：`go test ./internal/numind/biz/document/...` 过；`task lint` 0；**【S3-review P1+P2】** IDOR 单测验证跨用户 key 返回**具体 `errno.ErrDocumentSourceForbidden`**（非 generic error）。**【S4-T3 决策】qwen-long 推迟 v2 → v1 无 LLM → Langfuse 验证 N/A**（保留 docxFallback 接口 seam 注入 nil）。
**原子性**：biz 核心逻辑 + 测试，完成后 Open/Get/Save 可被 controller 调；export 单列 T4。

## T4 — 导出 service + biz 装配 + 并发守卫（server）[dep T1,T3]
**描述**：DocumentExportService（md 直返；pdf/docx 借 sandbox.Pool + pandoc）；biz.go 把 sandboxPool 存为字段 + IBiz 加 Document()；每用户单并发导出守卫；sandbox off 优雅降级。
**文件**：
- `internal/numind/biz/document/export.go`（spec §3.3；pandoc docx；pdf 二选一实测；`defer pool.Return`；ErrSandboxDisabled→ErrExportUnavailable）
- `internal/numind/biz/biz.go`（**P0-PoolWiring**：`sandboxPool sandbox.Pool` 字段 + 注入 ExportService + IBiz/struct 加 `Document() document.IDocumentService`）
- 单测：`export_test.go`（md 路径不走沙箱必返回；format 非法报错；并发守卫；sandbox off 降级——sandbox 部分可 mock pool 或标注 dev 集成验证）
**验收**：`go test ./internal/numind/biz/document/...` 过；`task lint` 0；`go build` 过；**【S3-review P1】** `biz.go`/`biz_test.go` 加编译期断言 `var _ IBiz = (*biz)(nil)` 并验证 `b.Document()` 返回非 nil（证明 P0-PoolWiring 装配正确）。
**原子性**：导出闭环 + 装配，完成后后端导出可用（pdf/docx 真实产出待 T6 镜像 + dev 集成）。

## T5 — controller + router + config flag（server）[dep T3,T4]
**描述**：controller（仅绑定/auth/调 biz/格式化，export 写二进制 + Content-Disposition）；router 注册 4 路由套 FeatureFlag；config flag。
**文件**：
- `internal/numind/controller/v1/document/document.go`（Open/Get/Save/Export；userID 取自 ctx）
- `internal/numind/router.go`（spec §3.6，`/documents` 组套 `FeatureFlag("features.document_system.enabled")`）
- `config_dev.yaml` + `config_local.yaml`（`features.document_system.enabled: true`，并入现有 features 段）
- **禁改 config_prod.yaml / config_qa.yaml**（休眠）
**验收**：`go build` 过；`task lint` 0；启动后 flag on 路由可达、flag off 返回 ErrFeatureDisabled（可代码审查 + dev 验证）。
**原子性**：对外接口闭合，后端功能完整可联调。

## T6 — 沙箱 skill 镜像加 pandoc（infra）[独立]
**描述**：`scripts/docker/skill-image/` 的 Dockerfile.base 加 `apt-get install -y pandoc`（独立 CI/CD，**不动主应用 Dockerfile**）；重建并 push skill 镜像 tag；更新 sandbox.image_tag（如需）。
**文件**：`scripts/docker/skill-image/Dockerfile.base`（+ requirements 若用 markdown 库兜底）；可能 `config_dev.yaml` 的 `sandbox.image_tag`。
**验收**：镜像内 `pandoc --version` 可用；`pandoc x.md -o x.docx` 与 pdf 路径 dev 实测（S5/S6）。
**原子性**：纯 infra，与 Go 代码解耦；可独立先做。

## T7 — 前端 API + store + flag + isEditable（web）[契约 spec§4]
**描述**：documents API 封装、Pinia store（open/save debounce1.5s/flushSave sendBeacon/exportAs blob）、类型、flag env、isEditable 工具 + vitest。
**文件**：
- `src/api/documents.ts`（spec §5.1，经 request.ts）
- `src/stores/documents.ts`（spec §5.2，setup 语法，4 状态，错误 toast）
- `src/types/document.ts`（DocumentDTO 等）
- `src/utils/editableArtifact.ts`（isEditable(mime,filename)）
- `env.d.ts` + `.env.development`（VITE_ENABLE_DOCUMENT_SYSTEM；`.env.production` 不加）
- vitest：`stores/__tests__/documents.test.ts`（open/save debounce/exportAs/inflight）、`utils/__tests__/editableArtifact.test.ts`
**验收**：`npm run lint` 0 error；`npm run type-check` 过；`vitest` 新增用例过。
**原子性**：前端数据层独立可测（mock 后端契约）。

## T8 — Milkdown 编辑器组件（web）[独立]
**描述**：MilkdownEditor.vue（@milkdown/crepe + @milkdown/vue，v-model markdown + readonly，internalUpdate 守卫），加依赖。
**文件**：`src/components/document/MilkdownEditor.vue`、`package.json`（+`@milkdown/crepe`/`@milkdown/vue`/`@milkdown/kit`）
**验收**：`npm install` 成功；`type-check`/`lint` 过；组件能挂载渲染一段 markdown（vitest 浅渲染或 dev 验证）。
**原子性**：独立编辑器组件。

## T9 — 编辑器模态 + 对话内入口（web）[dep T7,T8]
**描述**：DocumentEditorModal.vue（Teleport 全屏，顶部 bar 含保存状态+下载下拉+关闭，4 状态，Esc/遮罩关闭前 flush）；AgentArtifactItem.vue 加"打开编辑"按钮（`docEnabled && isEditable` 门控，传 source_url 非 artifact.id），懒加载模态。
**文件**：`src/components/document/DocumentEditorModal.vue`、`src/components/agent/AgentArtifactItem.vue`、vitest `AgentArtifactItem` 按钮显隐（flag×mime）
**验收**：`lint`/`type-check`/`vitest` 过；dev 浏览器 QA（S5）走通打开→编辑→保存→下载。
**原子性**：前端交互闭合。

## T10 — S5 验证策略（规划，非代码）
**验证方式**：
- 后端：`go test ./internal/numind/...`（biz 单测覆盖 IDOR/ownership/parse 分支/超限/export 守卫）+ `task lint`。
- 前端：`vitest`（store/isEditable/按钮显隐）+ `npm run lint && type-check`。
- 集成/浏览器 QA（dev，flag on）：gstack `/qa` 或 Playwright 走 **US1-US5 真链路** + **跨用户隔离探针**。
**理由**：核心逻辑（越权、解析、自动保存、导出降级）必须有**持久化回归**（go test + vitest），因涉及用户数据隔离（高风险）—— 遵 rule 10「高风险须 Playwright/持久测试」。WYSIWYG 编辑体验、pandoc 真实产出（docx 能被 Word 打开 / pdf 中文）属一次性 dev 集成确认（依赖 sandbox + T6 镜像）。
**关键用户路径**：
1. 在 dev agent 对话生成一个 docx/md 文件 → 卡片出现"打开编辑"。
2. 点开 → WYSIWYG 显示格式化内容（标题/加粗/列表/表格）。
3. 改一处 → 顶部"保存中→已保存" → 刷新重开内容为最新。
4. 下载 md / pdf / docx → 文件可正常打开（docx 用 Word，pdf 中文不乱码）。
5. 隔离探针：用独立测试账号 B 拿账号 A 的 agent-outputs URL 调 open → 403。
**【S3-review P2】T6 前置**：T6（沙箱镜像加 pandoc）push 是 pdf/docx S5 验证的前置条件；若 T6 未完成，S5 的 pdf/docx 真实产出路径可跳过并记录为 deferred（md 路径不受影响）。
**回归诚实声明**：浏览器 QA 中的 WYSIWYG 体验与 pandoc 产出无持久自动回归，未来改动需手动重跑；逻辑层有 go test + vitest 守护。

---

## 文件归属（Tier 校验用）
- **后端 server**：T1{migration,model/document.go,store/document.go,helper.go,store.go} / T2{util/cos_uploader.go} / T3{biz/document/*,errno/document.go} / T4{biz/document/export.go,biz.go} / T5{controller/v1/document/*,router.go,config_dev.yaml,config_local.yaml}
- **前端 web**：T7{api/documents.ts,stores/documents.ts,types/document.ts,utils/editableArtifact.ts,env.d.ts,.env.development} / T8{components/document/MilkdownEditor.vue,package.json} / T9{components/document/DocumentEditorModal.vue,components/agent/AgentArtifactItem.vue}
- **infra**：T6{scripts/docker/skill-image/*}
- 后端 vs 前端 vs infra 跨仓库/跨目录 disjoint → Tier 2 可并行。后端内部按依赖串行（共享 biz.go/router.go/helper.go 不并行写）。
