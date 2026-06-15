# SOP 回看页输入与上传可见 — 技术设计 (S2)

> 状态：S2 设计锁定。本 spec 是跨仓库（numind-server ↔ numind-web-v3）并行实现的 **API 契约权威**。

## §0 目标与范围

让历史 SOP 运行回看页（`SOPRunView` 的只读步骤态 `done-history` / `done-current`）在每步 AI 输出之上，**可见**当时的：
1. 用户输入文本（剥离合并进来的文件提取文本，干净呈现）；
2. 上传的文件 —— 图片缩略图（可放大）、文档卡片（可打开/下载）、可展开查看提取文本。

**不做**：不改 DB schema、不新增 API 端点、不动正在执行的 input 态、不碰管理端、不部署 prod。

## §1 数据现状（取证锁定）

| 数据 | 位置 | 说明 |
|------|------|------|
| 用户输入（合并） | `sop_node_run.input` (longtext) | 执行时 = `用户文本` + `\n\n=== <文件名> ===\n<提取内容>`(每文件) 或 `\n\n用户已上传以下文件：<names>...`(无法提取时)。见 `controller/v1/sop/sop.go:717-748` |
| 上传文件本体 | `sop_file` 表 | 字段：`id` `user_id` `run_id *uint` `node_id *uint` `file_name` `file_url`(COS base 非签名) `file_type`(MIME) `file_size int64` `file_ext` `content`(提取文本,可空,已截断) `status` `object_key` |
| store 查询 | `Sop().ListFilesByRun(runID uint) ([]model.SopFile, error)` | 已存在，返回该 run 全部文件 |
| COS 签名 | `util.GenerateSignedURL(ctx, objectKey, expirySeconds) (string, error)` | GET inline presign；`util.IsCOSEnabled()` 判可用 |

**关键约束**：COS 桶私有，`file_url`（base 非签名）匿名 GET → 403（`cos-url-lazy-resign` hotfix 已证）。**回看必须用 `object_key` 实时重签**，否则文件卡是死链。

## §2 后端契约（numind-server）

### 2.1 DTO 变更 — `pkg/api/numind/v1/sop.go`

`RunStatusResponse.CompletedNodes[]`（`CompletedNodeInfo`）新增字段：

```go
// CompletedNodeInfo 已完成节点信息
type CompletedNodeInfo struct {
    NodeRunID    uint   `json:"node_run_id"`
    // ... 既有字段不变 ...
    TotalTokens  int    `json:"total_tokens"`
    Files        []CompletedNodeFileInfo `json:"files,omitempty"` // 新增：该步上传文件（已签名 URL）
}

// CompletedNodeFileInfo 回看态单个上传文件（URL 已实时签名）
type CompletedNodeFileInfo struct {
    ID       uint   `json:"id"`
    FileName string `json:"file_name"`
    FileURL  string `json:"file_url"`           // 实时签名 URL（约 2h）。图片=inline GET 签名(可 <img> 渲染)；非图片=download 签名(attachment+RFC5987原名,避免 Chrome 跨站下载告警)。签名失败/无 object_key/COS 未启用 → 回退原 base url
    FileType string `json:"file_type"`          // MIME（前端判 image/ 前缀决定渲染图 vs 文件卡）
    FileSize int64  `json:"file_size"`
    FileExt  string `json:"file_ext,omitempty"` // 扩展名（前端选文件图标的辅助）
    Content  string `json:"content,omitempty"`  // 提取文本（可空、已截断，单文件上限 MaxTextContentLength=100KB）；前端默认折叠
}
```

> 不复用 `TemplateFileInfo`：它无 `content`/`file_ext`，且 `/templates/:id/runs` 返回多 run，附 content 会膨胀。status 接口仅单 run，content 上限 = 文件数 × 100KB（每步通常 1–3 文件，体量可接受）。
> **单 file_url 双签名策略（S2 reviewer P1 采纳）**：不分裂出 `download_url` 第二字段——biz 按 image-ness 选签名 flavor，前端只认一个 `file_url`（图片塞 `<img>` src、文档点击 `window.open`）。每文件仅一次 presign。

### 2.2 biz 变更 — `internal/numind/biz/sop/sop.go`

1. biz 侧 `CompletedNodeInfo`（约 line 348）同样新增 `Files []CompletedNodeFileInfo`（biz 自有结构），并定义 biz 版 `CompletedNodeFileInfo`（字段同上）。
2. `GetRunStatus(ctx, runID)`（line 1058）在构建完 `completedNodes` 后、返回前，新增装配：
   - `files, err := b.ds.Sop().ListFilesByRun(runID)`（err 非致命：log warn 后视为空，主流程不阻断）；
   - 按 `file.NodeID`（`*uint`，解引用）分组到 `map[uint][]model.SopFile`；
   - 遍历 `completedNodes[i]`，取本 node 的文件，逐个：
     - `signed := f.FileURL`（默认回退原 base url）；
     - 若 `f.ObjectKey != "" && util.IsCOSEnabled()`：
       - 判 image-ness：`isImage := strings.HasPrefix(f.FileType, "image/") || isImageExt(f.FileExt)`（biz/sop 内写小 helper `isImageExt`，集合 `.jpg/.jpeg/.png/.gif/.webp/.bmp/.svg`；不跨包调 biz/agent 私有的 cosIsImageName）；
       - image → `u, e := util.GenerateSignedURL(ctx, f.ObjectKey, 7200)`；非 image → `u, e := util.GenerateSignedDownloadURL(ctx, f.ObjectKey, f.FileName, 7200)`；`e==nil && u!="" { signed = u }`（失败保留 base，graceful）。
     - append `CompletedNodeFileInfo{ID, FileName, FileURL: signed, FileType, FileSize, FileExt, Content}`。
   - `content` 直接带上（单 run 体量可接受，支撑 AC5）。

### 2.3 controller 变更 — `internal/numind/controller/v1/sop/sop.go`

`GetRunStatus` handler 的 biz→v1 映射（line 1974）新增 `Files` 字段映射：把 `node.Files`（biz）逐个映射为 `v1.CompletedNodeFileInfo`。纯字段搬运，无逻辑。

> **顺带修复（S2 reviewer P1，独立 commit）**：该映射块（1974-1987）现状**漏拷贝 `NodeRunID`**——`v1.CompletedNodeInfo.NodeRunID` 恒为 0（既有 bug，前端 store 用 `cn.node_run_id` 当 `SopNodeRun.id`）。本次顺手加一行 `NodeRunID: node.NodeRunID`，但**单独成 commit**（`fix(sop): map NodeRunID in run status response`），不与 Files feature 混在同一 commit（CLAUDE.md 硬规则）。本 feature 不依赖该字段，故即使不修也不阻断；修它是零风险正确性补全。

### 2.4 后端不变量
- 不改 `sop_node_run.input` 的写入/语义（其它消费方不受影响）。
- 不改路由、不改鉴权（沿用 `/sop/runs/:id/status` 的 user_token + 归属校验）。
- `files` 为 `omitempty`：无文件的步骤不输出该字段，老前端忽略未知字段，向后兼容。

## §3 前端契约（numind-web-v3）

### 3.1 API 类型 — `src/api/sop.ts`
`StatusCompletedNodeInfo` 新增 `files?: StatusNodeFileInfo[]`；新增 interface：
```ts
export interface StatusNodeFileInfo {
  id: number
  file_name: string
  file_url: string      // 已签名
  file_type: string
  file_size: number
  file_ext?: string
  content?: string
}
```

### 3.2 类型 — `src/views/sop/types.ts`
`SopNodeRun` 新增 `files?: StatusNodeFileInfo[]`（import 自 `@/api/sop`，或在 types.ts 重声明一致结构 —— 实现时择一，避免循环引用，倾向在 types.ts 定义 `SopReplayFile` 并让 api/sop.ts 复用）。

### 3.3 store 映射 — `src/stores/sopRun.ts`（3 处映射点全覆盖）
- `loadRunSnapshot`（line 308）：`files: cn.files ?? []`（**主路径**，回看必经）。
- `recoverNodeRunFromServer`（line 572）：构造时 `files: info.files ?? []`。
- `refreshNodeRun`（line 537）：合并时 `files: info.files ?? prev.files ?? []`（让刚执行完的 done-current 步也能尽快显示上传）。

### 3.4 输入文本剥离 util — `src/views/sop/utils/replayInput.ts`（新建小工具 + 单测）

> **S2 reviewer P0 消解**：改用**结构分隔符剥离**，不再做"文件名 marker 匹配"——后端合并 marker 用 `file.Filename`(原始名) 而 `sop_file.file_name` 可能是 sanitize 后名，名字匹配会因特殊字符失效。分隔符法不依赖文件名，且不需改执行热路径(controller:718)。

后端合并格式（取证锁定，controller sop.go:730-746）：
- 有提取内容：`<userText>` + `\n\n` + `join(["=== <name> ===\n<content>", ...], "\n\n")`；userText 为空时直接以 `=== ` 开头。
- 无提取内容但有文件：`<userText>` + `\n\n用户已上传以下文件：...`；userText 为空时直接以该提示开头。

`stripMergedFileBlocks(input: string, hasFiles: boolean): string`：
- `!hasFiles || !input` → 原样返回 `input`（无文件时绝不剥离，避免误伤含 `=== ` 的用户文本）。
- 否则取 `\n\n=== ` 与 `\n\n用户已上传以下文件：` 两个分隔符在 input 中的**最早出现位置**，命中则返回其前缀 `input.slice(0, idx).trim()`。
- 若未命中 `\n\n`-前缀分隔符，但 input 以 `=== ` 或 `用户已上传以下文件：` 开头（userText 为空场景）→ 返回 `''`。
- 都不命中（有文件但无可识别块，防御）→ 返回 `input.trim()`（保守全显）。
- 纯函数，便于单测（AC4 回归保护）。残留边界：用户文本中若出现 `\n\n=== ` 且该步又有文件，会被误截——概率极低且优于展示整块合并 blob，接受。

### 3.4b 文件大小格式化 util — 同 `replayInput.ts`
`formatFileSize(bytes: number): string`：`<1024 → "{n} B"`；`<1024² → "{n} KB"`；else `"{n.x} MB"`。纯函数 + 单测。

### 3.5 新组件 — `src/views/sop/components/ReplayInputCard.vue`
只读「回看输入卡」。详见 §4 UI 设计。Props：
```ts
{ input: string; files?: StatusNodeFileInfo[] }
```
无 emits（除图片放大内部状态）。无 input 且无 files → 整卡不渲染（返回空）。

### 3.6 接入 — `src/views/sop/components/SopStepView.vue`
在只读分支（`isDoneCurrent || isDoneHistory`）的 `<OutputCard>` **之前**插入：
```vue
<ReplayInputCard
  v-if="(isDoneCurrent || isDoneHistory) && currentNodeRun"
  :input="currentNodeRun.input"
  :files="currentNodeRun.files"
/>
```
注意：该插入在 `<Transition name="sop-fade" mode="out-in">` 内只能有**单一根节点**切换。**锁定方案（S2 reviewer P1）**：把只读分支的 `v-else-if="isDoneCurrent || isDoneHistory"` 从 `<OutputCard>` 移到一个包裹 `<div key="readonly" class="readonly-stack">`，div 内依次放 `<ReplayInputCard>`（带自身 v-if）+ `<OutputCard>`。这样 ReplayInputCard 与 OutputCard 作为一个过渡单元一起入场（联动动画），不破坏既有 key/transition 逻辑。**不采用**"提到 Transition 外"的备选（会让两卡各自独立入场、动画割裂）。`readonly-stack` 用 `display:flex; flex-direction:column; gap` 控制两卡间距。

## §4 UI 设计规格（美观合理 —— 硬要求）

### 4.1 设计意图
回看页的叙事是「**我给了什么 → AI 产出了什么**」。ReplayInputCard 是 OutputCard 的对照前序：视觉上**安静、退后**（它是上下文，不是主角），与 OutputCard（AI 主角，翠绿 accent）形成 quiet↔accent 的层次。复用 `.sop-run-view-v2` scope 既有 token，零新增全局样式。

### 4.2 结构与视觉
```
┌ replay-input ───────────────────────────────────────┐
│  ✎  你的输入                          (head: 中性灰)   │
│ ┌─────────────────────────────────────────────────┐ │
│ │ <用户文本，pre-wrap，read-only>                    │ │ ← 文本块：--surface-tint 底, --radius-md, 内边距 lg
│ │ 长文本 max-height 卡 + 展开/收起                    │ │
│ └─────────────────────────────────────────────────┘ │
│                                                       │
│  📎 上传的素材 · 3                     (sub-label)    │
│  ┌────┐┌────┐┌────┐    图片：缩略图网格 88×88 圆角    │
│  │img ││img ││img │    点击 → AgentImagePreview 放大  │
│  └────┘└────┘└────┘                                   │
│  ┌─ file-card ──────────────────────────┐ ▾          │
│  │ [📄] report.pdf        PDF · 240 KB   │            │ ← 非图片：文件卡, 点击文件名新标签打开
│  └──────────────────────────────────────┘            │
│     └ 展开「查看提取文本」→ mono 滚动块                 │
└───────────────────────────────────────────────────────┘
```

- **容器**：`max-width: 980px`（对齐 OutputCard）；进入 `slideUp` 动画；与下方 OutputCard 间距由 SopStepView `gap` 负责，必要时卡内 `margin-bottom`。
- **head**：`✎`(Lucide `PenLine`) 24px 圆形图标，底 `--surface`/`--border-light`（中性，**不用 accent**，与 AI 图标的 accent 圆区分）；标签「你的输入」13px / 600 / `letter-spacing .05em` / uppercase 风格 / `--text-secondary`。镜像 OutputCard `__head` 但中性色。
- **文本块**：`background: var(--surface-tint)`；`border: 1px solid var(--border-light)`；`border-radius: var(--radius-md)`；`padding: var(--space-lg)`；`white-space: pre-wrap`；`overflow-wrap: anywhere`；`color: var(--text)`；`font-size: 15px`；`line-height: 1.6`。超过约 14em 高度时 `max-height` + 底部渐隐 + 「展开 / 收起」文字按钮（`--accent-link`）。
- **上传 sub-label**：`📎`(Lucide `Paperclip`) + 「上传的素材 · {n}」，13px `--text-secondary`，上间距 `--space-lg`。
- **图片网格**：`display:flex; flex-wrap:wrap; gap: var(--space-sm)`；每缩略图 88×88（移动 64×64），`object-fit: cover`，`border-radius: var(--radius-md)`，`border: 1px solid var(--border-light)`，hover 轻微 `--accent-soft` 边 + scale(1.02)，cursor pointer；点击 → 复用 `AgentImagePreview`（全屏 + 下载）或 `useImagePreview` 状态；加载失败 → 占位（文件名 + 文件图标）。
- **文件卡**（非图片）：行卡，`background: var(--surface)`，`border: 1px solid var(--border-light)`，`border-radius: var(--radius-md)`，`padding: var(--space-sm) var(--space-md)`，hover `--surface-hover`。左：按 `file_ext`/`file_type` 选 Lucide 图标（`FileText`/`FileSpreadsheet`/`FileImage`/`File`）置于柔色方块；中：文件名（单行省略）+ 次行「{EXT} · {人类可读大小}」`--text-muted`；右：`chevron` 展开按钮（仅当 `content` 非空）。点击文件名/卡体 → `window.open(file_url, '_blank')`。
- **提取文本展开**：`content` 折叠区，展开后 `font-family: var(--font-mono)`，12.5px，`--text-secondary`，`background: var(--surface-tint)`，`max-height` + 滚动，`white-space: pre-wrap`。默认折叠。
- **大小格式化**：`< 1KB → "{n} B"`，`< 1MB → "{n} KB"`，else `"{n.x} MB"`（前端小工具）。
- **四态**（ui-ux.md 硬规则 2，只读展示态）：success=正常渲染；empty=无 input 且无 files → 整卡不渲染（AC6）；图片失败=占位兜底；无 loading/error 子请求（数据随 status 一次性到达，不额外发请求）。

### 4.3 响应式
- ≤768px：缩略图 64×64；文本块 padding 降一档（`--space-md`）；文件卡保持单列；head 不变。

### 4.4 可访问性 / 只读纪律（AC7）
- 全部 `read-only`：无 `<textarea>`、无上传/执行按钮。
- 图片 `alt = file_name`；文件卡 `title = file_name`；展开按钮 `aria-expanded`。

## §5 边界与降级（汇总）
- COS 未启用 / `object_key` 空 / 签名失败 → 用 base `file_url`，不阻断（图片可能 403，但不报错，文件名仍可见）。
- 早期运行无 `sop_file` 记录 → `files` 空 → 不渲染上传区。
- input 含 `...(内容过长已截断)` → 文本块滚动/折叠承接。
- 文件名特殊字符 → 剥离按精确匹配，匹配不到保守保留。
- 单步多文件 → 网格换行 + 文件列表纵向堆叠。

## §6 测试策略（喂给 S3 的 S5 验证 task）
- **后端 Go 单测**（biz 层）：`GetRunStatus` 装配 files —— mock store `ListFilesByRun` 返回含/不含 ObjectKey 的文件，断言 Files 数组长度、按 node 分组正确、ObjectKey 空时回退 base url。COS 签名可注入/桩（IsCOSEnabled=false 路径走回退，便于无 COS 环境测）。
- **前端单测**（vitest）：`stripMergedFileBlocks` 纯函数 —— 用例覆盖：(a) `hasFiles=false` 即使含 `=== ` 也原样返回；(b) 文本 + 单文件 `\n\n=== ` 块 → 仅留文本；(c) 文本 + 多文件块 → 仅留文本；(d) userText 为空、input 以 `=== ` 开头 → 返回 ''；(e) `\n\n用户已上传以下文件：` 提示段分支 → 仅留文本；(f) userText 为空以 `用户已上传以下文件：` 开头 → ''；(g) hasFiles=true 但无可识别块 → trim 全显（防御）。`formatFileSize`：B/KB/MB 三档边界用例。
- **S5 浏览器验证**：因回看依赖真实历史 run + 真实 COS 文件，本地无完整数据；采用 **dev 部署后用 gstack `/qa`/browse 以 E2E 账号导航到一条含上传的历史 SOP run**，逐步验证：输入文本可见(AC1)、图片缩略图加载成功非 403(AC2)、文档卡可打开(AC3)、提取文本展开(AC5)、空步骤不报错(AC6)、只读无输入框(AC7)、视觉一致(AC9)。截图留档。
  - 诚实声明：`stripMergedFileBlocks` + 后端 files 装配有持久化单测做回归；端到端视觉验证走 dev（一次性 gstack QA，无持久 E2E 脚本）—— 本功能非支付/权限高风险，可接受。

## §7 AI 可观测性
N/A —— 本功能纯读取历史持久化数据 + 展示，无任何 LLM / aiservice 调用，无 trace/generation 需求。

## §8 验收标准映射（S1 AC → 实现点）
AC1/AC4→§3.4 剥离 + §3.5 文本块；AC2→§2.2 签名 + §4.2 图片网格；AC3→§4.2 文件卡 open；AC5→§2.1 content + §4.2 展开；AC6→§3.5/§4.2 empty；AC7→§4.4 只读；AC8→§2.1/§2.3 DTO+映射；AC9→§4 全节 token 复用。
