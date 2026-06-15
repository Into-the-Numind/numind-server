# SOP 回看页输入与上传可见 — 实施计划 (S3)

> 依据 S2 spec（API 契约权威）。多仓库：后端 task 先于前端。Tier：T1(server) ⟂ T2(web-v3) 跨仓库可并行(Tier 2，契约已锁)；T3 依赖 T2。每个 code task 完成后并行双 reviewer。

worktrees：
- numind-server: `/private/tmp/wt-sop-replay-input-visibility-numind-server`
- numind-web-v3: `/private/tmp/wt-sop-replay-input-visibility-numind-web-v3`

进度计数：`total_tasks=3`（T1/T2/T3 三个 code task；T4 是验证策略规划项，不计 progress）。

---

## T1 — 后端：status 响应返回每步上传文件（含 NodeRunID 修复）
**仓库**：numind-server
**涉及文件**：
- `pkg/api/numind/v1/sop.go`（DTO）
- `internal/numind/biz/sop/sop.go`（biz 结构 + GetRunStatus 装配 + helper）
- `internal/numind/controller/v1/sop/sop.go`（biz→v1 映射）
- `internal/numind/biz/sop/replay_files_test.go`（新增单测）

**步骤与提交（2 commit）**：
1. **commit 1（独立 bugfix）** `fix(sop): map NodeRunID in run status response`
   - controller `GetRunStatus` 映射块（~1974）补 `NodeRunID: node.NodeRunID`。
   - 无单测（controller 层按 testing.md 由 E2E 覆盖；1 行字段拷贝）。
2. **commit 2（feature）** `feat(sop): expose per-step uploaded files in run status response`
   - `pkg/api/numind/v1/sop.go`：新增 `CompletedNodeFileInfo`（id/file_name/file_url/file_type/file_size/file_ext/content，按 spec §2.1）；`CompletedNodeInfo` 加 `Files []CompletedNodeFileInfo json:"files,omitempty"`。
   - `biz/sop/sop.go`：
     - biz 侧 `CompletedNodeFileInfo` 同构 + biz `CompletedNodeInfo` 加 `Files`。
     - 新 helper `isImageExt(ext string) bool`（集合 `.jpg/.jpeg/.png/.gif/.webp/.bmp/.svg`，lower-trim）。
     - 新**可测试 seam** `attachNodeFiles(ctx, nodes []CompletedNodeInfo, files []model.SopFile, signImage func(context.Context, string) (string, error), signDownload func(context.Context, string, string) (string, error))`——签名锁定：`signImage(ctx, objectKey)`、`signDownload(ctx, objectKey, fileName)`。函数内按 `*NodeID` 分组、判 image-ness、签名（失败/空 objectKey/signer 返回 ""→回退 base url）、回填 `nodes[i].Files`。签名函数注入便于单测不依赖 COS。
     - 生产调用：GetRunStatus 传入闭包——`signImage := func(ctx,k){ if !util.IsCOSEnabled(){return "",nil}; return util.GenerateSignedURL(ctx,k,7200) }`，`signDownload := func(ctx,k,n){ if !util.IsCOSEnabled(){return "",nil}; return util.GenerateSignedDownloadURL(ctx,k,n,7200) }`（COS 未启用返回空 → attachNodeFiles 回退 base，graceful）。
     - `GetRunStatus`：构建完 completedNodes 后，`files, err := b.ds.Sop().ListFilesByRun(runID)`（err 非致命，log warn 视为空），调 `attachNodeFiles(ctx, completedNodes, files, util.GenerateSignedURL-适配, util.GenerateSignedDownloadURL-适配)`。注意签名仅在 `util.IsCOSEnabled()` 时尝试（在注入的闭包里判，或 attachNodeFiles 内传入的 signer 在 disabled 时返回空触发回退）。
   - `controller/v1/sop/sop.go`：映射块补 `Files`（biz `node.Files` → `v1.CompletedNodeFileInfo` 逐字段）。
   - **单测** `replay_files_test.go`：直接测 `attachNodeFiles` + `isImageExt`：
     - 多 node 多 file 按 NodeID 正确分组；
     - 图片走 signImage、非图片走 signDownload（用 fake signer 断言被调 + 返回值进 FileURL）；
     - objectKey 空 → 回退 base FileURL（signer 不被调）；
     - signer 返回 err 或 "" → 回退 base FileURL；
     - `isImageExt`：显式含大写变体（`.JPG`/`.PNG`/`.JPEG` → true）+ 各小写扩展名 + 未知扩展名(.pdf/.docx → false) + 空串。
   - **NodeRunID 下游核对**（reviewer P1）：commit 1 修复后，确认前端 store 用 `cn.node_run_id` 构建 `SopNodeRun.id` 无依赖"恒为 0"的 workaround（在 T2 review 时 grep 前端对 `nodeRun.id` 的使用确认行为一致；当前预判：id 仅作标识、无 ===0 分支，修复纯增益）。

**验收**：`go build ./...` 通过；`go test ./internal/numind/biz/sop/...` 通过（含 attachNodeFiles/isImageExt 单测，签名走注入桩，**不依赖真实 COS**）；`task lint` 退出 0。端到端"有文件时 completed_nodes[].files 为可访问签名 URL" 留 T4 dev gstack QA 验证（真实 COS + 真实历史 run）。
**Tier**：与 T2 跨仓库并行（Tier 2）。

---

## T2 — 前端：数据层（类型 + api + store 映射 + 纯函数工具）
**仓库**：numind-web-v3
**涉及文件**：
- `src/api/sop.ts`（类型）
- `src/views/sop/types.ts`（SopNodeRun）
- `src/stores/sopRun.ts`（3 映射点）
- `src/views/sop/utils/replayInput.ts`（新建）
- `src/views/sop/utils/__tests__/replayInput.spec.ts`（新建单测）

**步骤（1 commit）** `feat(sop): carry per-step files + input-strip utils for replay`：
- `api/sop.ts`：新增 `StatusNodeFileInfo`（id/file_name/file_url/file_type/file_size/file_ext?/content?）；`StatusCompletedNodeInfo` 加 `files?: StatusNodeFileInfo[]`。
- `types.ts`：定义 `SopReplayFile`（id/file_name/file_url/file_type/file_size/file_ext?/content?）；`SopNodeRun` 加 `files?: SopReplayFile[]`。**锁定（reviewer P2）**：`types.ts` 为单一真相，`api/sop.ts` `export type StatusNodeFileInfo = SopReplayFile` re-export（不重声明，杜绝 drift）。注意检查 import 方向无循环（types.ts 不 import api/sop.ts）。
- `stores/sopRun.ts`：
  - `loadRunSnapshot`（~308）：`files: cn.files ?? []`；
  - `recoverNodeRunFromServer`（~572）：`files: info.files ?? []`；
  - `refreshNodeRun`（~537）：`files: info.files ?? prev.files ?? []`。
- `replayInput.ts`：`stripMergedFileBlocks(input, hasFiles)` + `formatFileSize(bytes)`（逻辑见 spec §3.4/§3.4b），纯函数、有 JSDoc。
- **单测** `replayInput.spec.ts`：覆盖 spec §6 列的 (a)-(g) + formatFileSize 三档边界。

**验收**：`npm run type-check` 退出 0；`npm run lint` 退出 0；`npx vitest run src/views/sop/utils` 通过；**显式确认 store 3 个映射点 `loadRunSnapshot` / `recoverNodeRunFromServer` / `refreshNodeRun` 均已补 `files`**（reviewer P2，refresh 点关系到 done-current 步即时显示上传）；store/api 改动不破坏现有类型。
**Tier**：与 T1 跨仓库并行（Tier 2）。

---

## T3 — 前端：回看输入卡 UI + 接入回看页
**仓库**：numind-web-v3 **依赖**：T2（类型/utils/store）
**涉及文件**：
- `src/views/sop/components/ReplayInputCard.vue`（新建）
- `src/views/sop/components/SopStepView.vue`（接入只读分支）

**步骤（1 commit）** `feat(sop): show historical input & uploads in replay step view`：
- `ReplayInputCard.vue`（只读，props `{ input: string; files?: SopReplayFile[] }`）：
  - 计算 `cleanInput = stripMergedFileBlocks(input, (files?.length ?? 0) > 0)`；`imageFiles = files.filter(image/ 前缀 or isImage ext)`；`docFiles = 其余`。
  - 无 `cleanInput` 且无 `files` → 渲染空（`v-if` 整卡不挂载）。
  - 结构/视觉严格按 spec §4.2：head（中性 PenLine 圆标 + 「你的输入」大写标签）+ 文本块（surface-tint, pre-wrap, 长文展开/收起）+ 「上传的素材 · n」+ 图片网格（88px, 点击放大）+ 文件卡列表（图标/名/大小, 点击 window.open(file_url)，content 非空可展开 mono 块）。
  - 图片放大**锁定**复用 `@/components/agent/AgentImagePreview.vue`（已确认存在，props `url: string | null`）+ 本地 `previewUrl` ref：缩略图 @click 设 `previewUrl=file.file_url`，`<AgentImagePreview :url="previewUrl" @close="previewUrl=null" />`。若实现时发现其 props/事件不符则降级为本地 overlay（fixed 全屏 + 点击关闭）。
  - design token 全部取 `.sop-run-view-v2` scope 既有变量（§4.2 列表），`<style scoped>`，零全局污染。响应式 §4.3。a11y §4.4。
- `SopStepView.vue`：把只读分支 `v-else-if="isDoneCurrent || isDoneHistory"` 改为包裹 `<div key="readonly" class="readonly-stack">`，内含 `<ReplayInputCard v-if="currentNodeRun" :input="currentNodeRun.input" :files="currentNodeRun.files" />` + 原 `<OutputCard ...>`；保留 Transition 单根节点。`.readonly-stack { display:flex; flex-direction:column; gap }`。import ReplayInputCard。

**验收**：`npm run type-check` 退出 0；`npm run lint` 退出 0；`npm run build` 通过；只读态渲染 ReplayInputCard（视觉/只读纪律由 S5 浏览器 QA 验）。
**Tier**：单仓库单文件集，串行于 T2。

---

## T4 — S5 验证策略（Rule 10，规划项非 code task）
- **验证方式**：后端 Go 单测（T1 attachNodeFiles/isImageExt）+ 前端 vitest（T2 纯函数）做**持久化回归保护**；端到端视觉/真实数据验证走 **dev 部署后 gstack `/qa` / browse**（以 `$E2E_USERNAME`/`$E2E_PASSWORD` 登录 `$DEV_SITE_URL`，导航到一条含历史上传的 SOP run 回看页）。
- **理由**：回看强依赖真实历史 run + 真实 COS 私有桶签名链接，本地无完整数据且无法复现私有桶签名访问；最忠实的端到端验证只能在 dev（有真实数据 + 真实 COS）。本功能纯展示、非支付/权限高风险，可接受"一次性 gstack QA + 纯函数回归测试"的组合（无持久 Playwright E2E）。
- **关键用户路径（S5/S6 验证清单）**：
  1. 登录 → 进入「运行记录」→ 打开一条**有上传文件**的历史 run 回看页。
  2. 逐步点开已完成步骤：确认「你的输入」文本块出现且不含 `=== 文件名 ===` 提取文本（AC1/AC4）。
  3. 含图片的步骤：缩略图加载成功（**Network 200 非 403**）、点击放大（AC2）。
  4. 含文档的步骤：文件卡可见、点击下载/打开成功、提取文本可展开（AC3/AC5）。
  5a. 纯文本步骤（有文本、无上传）：显示「你的输入」文本块、无上传区，正常（AC6）。
  5b. 无任何输入步骤（无文本、无上传）：ReplayInputCard 整卡不渲染、不报错、不出空卡（AC6）。
  6. 只读纪律：无输入框、无上传/执行按钮，既有「复制/保存生成记录」不受影响（AC7）。
  7. 视觉：与现有 OutputCard/步骤卡体系一致、美观（AC9）。
- **回归保护诚实声明**：gstack `/qa` 一次性、不产持久测试代码；纯函数与后端装配逻辑由单测长期护栏，UI 视觉无自动回归（后续改动需手动重 QA）。

---

## 执行顺序
1. T1（backend，含 NodeRunID 独立 commit）→ 双 reviewer → 修 P0/P1 → reviewed_tasks+1
2. T2（frontend 数据层）→ 双 reviewer → 修 → reviewed_tasks+1
3. T3（frontend UI + 接入）→ 双 reviewer → 修 → reviewed_tasks+1
4. S4 gate：两仓库 lint/type-check/test 全绿 + reviewed==completed==3
5. S5：dev 部署 + gstack QA（T4 清单）
6. S6：ndf-done 合并 develop（两仓库）+ /deploy-dev（已在 S5 部署，S6 确认）— 止步 dev，不碰 prod
