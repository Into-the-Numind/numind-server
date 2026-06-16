# 文档系统（Document System）v1 — 提案 + PRD

> 关联：`.ndf/features/document-system/requirement.md`（S0 需求卡）
> 轨道：Standard（S0→S7） | 仓库：numind-server + numind-web-v3 | 隔离：feature flag 休眠

---

## §1 方案概述 [客户可见]

把 **agent mode 在对话中生成的交付物**（报告、方案、Word 文档、纯文本等）从"只能下载的死文件"变成"能在平台内打开、微调、再下载的活文档"。

用户体验闭环（v1）：
1. 在 agent 对话里，AI 生成了一个文件（如一份 Word 报告），像现在一样显示成一张文件卡片。
2. 卡片上除了"下载"，多一个 **"打开编辑"** 按钮。
3. 点开 → 在对话内弹出一个**所见即所得（WYSIWYG）编辑器**，文件内容已转成可编辑的富文本（标题、加粗、列表、表格都正常显示）。
4. 用户直接改 → **自动保存**（无需手动"存为文档"）。
5. 改完点下载 → 导出为 **Markdown / Word(docx) / PDF**。

整套系统通过一个**休眠开关**与平台其它部分隔离：开关默认关闭，合并到 develop 不影响线上；prod 打 tag 部署时不开开关 = 等于未上线，零风险。

**v1 边界**：只服务 AI 生成的文本类产物。用户上传自己文档的在线编辑放 v2。图表 PNG / Excel / PPT / PDF 等只预览+下载，不在线编辑。

---

## §2 工作量与周期 [内部]

> 一人公司自研产品，无对外报价。此处为内部工作量估算。

- **预估工作量**：约 6–9 个工作日（跨两仓库）
  - 后端（numind-server）：~3–4.5 天（document 表+model+store+biz+controller+router、解析打开、自动保存、导出下载、feature flag、导出沙箱对接）
  - 前端（numind-web-v3）：~3–4.5 天（Milkdown WYSIWYG 编辑器组件、对话内打开入口、文档 store+API、自动保存、导出下载 UI、feature flag）
- **关键不确定性**：md→docx/pdf 导出方案（见 §3 风险 R1）。若选"复用 run_python 沙箱"路径，+0.5~1 天；若 v1 先只导 md+pdf，-1 天。
- **交付里程碑**：S2 设计锁定 → S3 任务拆分 → S4 编码（按 task 双 review）→ S5 本地验收 → S6 合 develop + dev 部署验收 →（S7 prod 由你单独授权打 tag）。

---

## §3 技术可行性 [内部]

### 现有功能复用（已 explore 验证，附文件锚点）

| 能力 | 复用资产 | 备注 |
|------|---------|------|
| 对象存储 + 预签名下载 | `internal/pkg/util/cos_uploader.go`（`UploadBytesToCOS` / `GenerateSignedDownloadURL`） | 文档无关，直接用 |
| 文件解析 docx→文本/md | `internal/pkg/parser/document_parser.go`（MarkItDown + go-fitz） | docx/pdf/txt/md/html |
| 高保真 docx/pdf 解析（备选） | qwen-long 三步流（`controller/v1/sop/sop.go` extractPlainTextWithQwenLong） | 走 aiservice，计费/trace |
| 生成沙箱（导出复用候选） | `internal/numind/biz/agent/tool_run_python.go`（Docker 沙箱借用）+ `skills/docx-author`、`skills/pdf-from-html` | **目前仅 agent LLM 循环可达，无独立 Go 入口**（风险 R1）|
| 用户产物来源 | agent final answer markdown 里的 COS `agent-outputs/` URL（前端 `src/utils/agentArtifacts.ts` extractArtifacts 抠出 {filename,url,mime}）| **无 DB 行、无 ID、URL 24h 过期**（风险 R2）|
| 对话内打开入口 | `src/components/agent/AgentArtifactItem.vue:134-143`（下载卡片）+ html-preview-overlay Teleport 模态（同文件 156-208）| 加"打开编辑"按钮 + 全屏编辑器模态 |
| 编辑器组件范式 | `src/views/config/skills/components/CodeMirrorEditor.vue`（v-model/readonly/mount/unmount 范式）| 范式可借鉴，但 CodeMirror 是源码编辑非 WYSIWYG（风险 R3）|
| feature flag 休眠 | `internal/pkg/middleware/feature_flag.go` `FeatureFlag(key)` + viper 默认 false + 前端 `import.meta.env.VITE_ENABLE_*`（NotificationBell.vue:53 范式）| 直接照搬 notification-center |

### 技术风险

**R1（最高）：md→docx/pdf 导出无独立后端路径。**
office 生成 skill（docx-author/pdf-from-html）只能通过 agent 的 `load_skill`+`run_python` Docker 沙箱跑，没有可被普通 biz 调用的 Go 函数。
- 缓解方案（S2 三选一）：
  - **(a) 复用沙箱借用机制**：新建非 LLM 的 biz 导出函数，直接借 `tool_run_python` 的沙箱基础设施，喂固定的 md→docx（python-docx / pandoc）转换脚本。复用度高，但要把沙箱借用从 agent 解耦出可独立调用的入口。
  - **(b) 容器内置 pandoc**：部署镜像里装 pandoc，后端 exec `pandoc md→docx/pdf`。最简单稳定，但要改 Dockerfile（影响部署链路，与"隔离"约束略有张力，需评估）。
  - **(c) v1 降级**：先只导 **Markdown**（源文件，0 成本）+ **PDF**（md→html→浏览器/wkhtmltopdf 或 pdf-from-html），docx 导出放 v1.1。
- **建议**：S2 优先评估 (a)；若解耦成本高则 (c) 先上线闭环，docx 紧随。

**R2：用户产物无持久化 DB 记录 + 预签名 URL 24h 过期 + COS 对象可能被清理。**
- 缓解：document 表用 `(user_id, source_object_key)` 稳定键（object key 从 markdown URL 路径解析，签名变但 key 不变）；**首次"打开"时懒 materialize** —— 拉 COS 内容 → 解析成 markdown → 快照存入 `document.content_md`。一旦建档，文档自包含，后续编辑/导出不依赖原 COS 对象。仅"生成后从未打开且对象已被 GC"的窗口会 open 失败 → 优雅报错。
- 此设计**完全附加**，不改 agent 生成/finalize 链路，最符合隔离约束。

**R3：无 WYSIWYG 编辑器，需新增无头库。**
- 缓解：选 **Milkdown**（基于 ProseMirror，markdown 原生序列化，与"底层存 markdown"契合）或 **TipTap**（生态大，需额外 markdown serializer）。两者均为无头编辑器库，**不违反"禁用外部 UI 框架"**（禁的是 Element/Ant/Vant 组件库）。S2 定选型，**倾向 Milkdown**。
- bundle 体积增量需在 S5 用 `/optimize` 视角核查（懒加载编辑器路由）。

**R4：docx→markdown 解析保真度有损。**
- 用户已接受（转换式，复杂排版/图表/批注会简化）。S2 定解析优先级：文本类直读 > MarkItDown > qwen-long（贵，仅复杂 docx 兜底）。

### 涉及仓库
- [x] numind-server（document 表+API+解析+导出+flag）
- [x] numind-web-v3（WYSIWYG 编辑器+对话内入口+store+flag）
- [ ] numind-admin-web（v1 不涉及）

### AI 可观测性（是否涉及 LLM 调用）
- [x] 涉及 LLM 调用：**条件性是**
- 仅当 docx→markdown 解析走 **qwen-long 兜底**时触发 LLM 调用；文本类直读 / MarkItDown 路径不涉及 LLM。
- Trace 起点：document open biz 函数（仅 qwen-long 分支）`CreateTrace("document-parse")`。
- Generation 点：qwen-long extract 调用记 generation（model=qwen-long，记 token usage）。
- 关键元数据：user_id、document_id、source_mime、parse_method（direct/markitdown/qwen-long）。
- 走 `aiservice` 统一入口（禁止裸调），自动计费+trace+降级。
- 若 R1 的 docx 导出最终走 LLM（不预期），需补 trace；预期导出是确定性转换不涉及 LLM。

---

## §4 产品需求定义 — PRD [内部 — 不为可读性简化]

### 用户故事
- US1：作为 C 端用户，我在 agent 对话里看到 AI 生成的文本类文件（md/docx/txt/html）时，希望卡片上有"打开编辑"按钮，以便不离开对话就能改它。
- US2：作为 C 端用户，我点"打开编辑"后，希望在对话内弹出的所见即所得编辑器里看到已格式化的内容（标题/加粗/列表/表格正常显示），以便像用 Word/Notion 一样直接改。
- US3：作为 C 端用户，我编辑文档时希望改动**自动保存**，不用手动点"存为文档"，以便不丢内容。
- US4：作为 C 端用户，我改完后希望能把文档下载为 **Markdown / Word(docx) / PDF**，以便拿去外部使用。
- US5：作为 C 端用户，我重新打开同一个 AI 生成的文件时，希望看到**我上次编辑后的版本**（而不是 AI 的原始版本），以便接着改。
- US6（隐性）：作为平台，文档必须严格按用户隔离（user_id / parent_user_id），任何用户不能打开/编辑别人的文档。

### 验收标准
- [ ] AC1：agent 对话中，文本类生成产物（mime ∈ {text/markdown, text/plain, text/html, application/vnd.openxmlformats-officedocument.wordprocessingml.document}）的卡片显示"打开编辑"按钮；非文本类（png/csv/xlsx/pptx/pdf）**不显示**该按钮，仅下载/预览。
- [ ] AC2：点"打开编辑" → 弹出全屏 WYSIWYG 编辑器，docx/md/html 内容正确转成可编辑富文本（标题层级、加粗/斜体、有序/无序列表、表格、链接、代码块至少这几类可见且可编辑）。
- [ ] AC3：首次打开某产物 → 后端懒建 `document` 行（按 user_id+source_object_key 去重），content_md 为解析结果；二次打开同一产物 → 返回上次编辑后的 content_md（验证 US5）。
- [ ] AC4：编辑时自动保存（debounce ≤2s 或失焦即存），刷新页面后重新打开内容为最新编辑版。
- [ ] AC5：下载 → 至少支持导出 **Markdown**（必）+ **PDF**（必）；docx 导出按 R1 决策（(a)/(c)）—— 若 v1 含则验证 docx 可被 Word 正常打开。
- [ ] AC6：跨用户隔离 —— 用户 A 无法通过任何 document API 打开/读取/编辑/导出用户 B 的文档（返回 403/404，不泄露存在性）。
- [ ] AC7：feature flag OFF（prod 默认）时，所有 `/v1/documents/*` 返回 ErrFeatureDisabled(404)，前端"打开编辑"按钮不渲染；ON（dev）时功能可用。
- [ ] AC8：纯附加 —— flag OFF 时对现有 agent 对话/下载/生成行为零影响（回归：现有 agent E2E 不破）。

### 边界情况
- 源 COS 对象已被 GC / 不可达（生成后久未打开）：open 返回友好错误"原文件已过期，无法打开"，不 500。
- 空文档 / 超大文档（如 10MB md）：设上限（建议 content_md ≤ 2MB），超限提示。
- 解析失败（损坏 docx）：降级到纯文本或报错，不 500。
- 并发编辑（同一用户两个标签页开同一文档）：v1 用 last-write-wins + updated_at，不做协作锁（协作是 v2）。
- 自动保存与导出竞态：导出取当前已保存的 content_md。
- mime 缺失（uploadGeneratedFile 不一定回填 mime）：用 filename 扩展名兜底判定可编辑性。
- 同一文件被 AI 在多轮对话重复生成（不同 object_key）：视为不同 document（按 object_key 区分）。

### 权限规则
- 仅登录 C 端用户（user_token 中间件）。
- 严格 user_id 归属；B2B2C 下子账户文档归子账户自己（parent 不自动可见——v1 不做下发/共享）。
- 管理端 v1 无文档管理入口。
- 全部 `/v1/documents/*` 套 `FeatureFlag("features.document_system.enabled")`。

### UI 行为规格
- **位置**：agent 对话内（`AgentArtifactItem.vue` 文件卡片）。无独立"我的文档"页面（用户已确认）。
- **入口**：文本类卡片加"打开编辑"按钮（与下载按钮并列）；按钮显隐受 `VITE_ENABLE_DOCUMENT_SYSTEM` + mime 可编辑性双重控制。
- **编辑器容器**：`Teleport to="body"` 全屏模态（借鉴 AgentArtifactItem html-preview-overlay 范式）：顶部 bar（文档名 + 保存状态指示 + 下载下拉 + 关闭）+ 主体 Milkdown 编辑区。
- **交互**：所见即所得编辑；自动保存（顶部显示"已保存/保存中…"）；下载下拉选 md/pdf/(docx)；Esc/点遮罩关闭（关前确保已保存）。
- **状态处理**：
  - loading：打开时解析可能耗时（qwen-long 兜底）→ 编辑器区 skeleton + "正在打开文档…"。
  - empty：解析结果空 → 提示"文档无可编辑内容"。
  - error：解析/打开失败 → 错误态 + 重试按钮（AC 边界）。
  - success：正常编辑。
  - 自动保存失败：顶部红色"保存失败，重试中"，不静默吞错（遵 frontend-state 规则）。

---

## §5 待 S2 决策清单（不在本提案拍板，留给技术设计）
1. md→docx/pdf 导出方案：R1 (a) 沙箱复用 / (b) pandoc 容器 / (c) v1 先 md+pdf。
2. WYSIWYG 库选型：Milkdown（倾向）vs TipTap。
3. docx→md 解析优先级链：direct / MarkItDown / qwen-long 触发条件。
4. document 表 schema 细节（字段、索引、source_object_key 唯一约束、与 agent_run 的弱关联）。
5. 自动保存策略（debounce 时长 / 失焦存 / 冲突处理）。
6. content_md 大小上限与超限处理。
