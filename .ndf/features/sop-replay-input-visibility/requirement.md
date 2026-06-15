# SOP 回看页输入与上传可见

## 来源
- 提出人：用户（产品负责人，2026-06-16 设为 /goal autopilot 授权）
- 提出日期：2026-06-16

## 需求描述
> 客户原话："我需要你帮我把 SOP 回看的页面变成历史输入和上传可见的模式，现在看历史记录的时候不可见，要变成可见，方便用户回溯每一步的输入。UI 要好好设计一下，保持美观合理。"

结构化：SOP 运行由多个步骤（step / node）组成，每步用户会**输入文本**并可**上传文件/图片**。当用户进入历史运行回看页（`/sop/run?runId=X` 的 `done-history` / `done-current` 只读态）时，每一步只展示 AI 的「输出」，看不到当时**用户输入的文本**和**上传的文件**。需求是把这两类信息在回看态变为可见，让用户能完整回溯每一步的输入上下文。

## 业务目标
- 用户回看历史 SOP 运行时能完整理解"当时我给了什么 → AI 产出了什么"，提升对历史结果的可追溯性与信任。
- 是 IP 孵化陪跑场景的核心诉求：教练/学员复盘历史运行需要看到当时喂给每一步的素材（文案、截图、参考资料）。

## 优先级
高（用户设为 goal，要求一口气跑到 dev 部署）

## Triage
- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：**否**（`sop_node_run.input` + `sop_file` 表数据均已持久化，无需新表/新列）
  2. 新增 API 端点：**否**（在现有 `GET /v1/sop/runs/:id/status` 响应里补 `files` 字段，不新增路由）
  3. 新外部服务集成：否
  4. 影响文件数：**>3**（后端 DTO + biz + controller 映射 3 个 + 前端 api 类型/store/回看组件/新建只读输入卡 3+ 个，跨 numind-server + numind-web-v3 两仓库，约 6-8 个）
  5. 高风险业务逻辑（支付/权限）：否
- 人类决定：用户 2026-06-16 通过 /goal 授权 AI 自主推进 Standard 直至部署 dev（不部署 prod）。AI 据第 4 条（文件数 >3 + 跨仓库 + UI 设计需求走 S2）自主判定 Standard，规则强制不可降级。

## 现状取证（已读代码确认，非静态推理）
- 回看页 `numind-web-v3/src/views/sop/SOPRunView.vue` → `StepCanvas` → `components/SopStepView.vue`。`done-history`/`done-current` 态只渲染 `OutputCard`（输出 + thinking + 元信息），**完全不渲染 input，也无 files**。
- 数据来源：`GET /v1/sop/runs/:id/status` → `fetchRunStatusDetail()` → `RunStatusResponse.completed_nodes[]`（`CompletedNodeInfo`）。该结构**有 `input` 字段、无 `files` 字段**。store `sopRun.ts` 已把 `input` 存进 `nodeRuns[].input`，只是前端没渲染。
- 后端：`sop_node_run.input`（longtext）已持久化；但它是**合并 blob**——执行时把上传文件的提取文本以 `=== 文件名 ===\n内容` 追加到用户文本后（`controller/v1/sop/sop.go:717-748`）。上传文件本体记录在 `sop_file` 表（`file_url`/`file_name`/`file_type`/`file_size`/`content`/`object_key`，含 `run_id`+`node_id`）。store 已有 `ListFilesByRun(runID)` / `ListFilesByNodeRuns(nodeIDs)`。
- 另一个接口 `GET /v1/sop/templates/:id/runs`（`TemplateNodeRunInfo`）已含 `files`，但回看页不走它，走的是 `/status`。

## 备注
- `input` 是合并 blob 这一点是设计关键：直接整块渲染会把文件提取文本和用户文本混在一起、且与文件卡重复。UI 设计需把"用户文本"与"上传文件"分离呈现。
- 上传文件 URL 是 COS 链接直存（非临时签名），回看时是否需重签由 S2 评估（参考已存在的 `cos-url-lazy-resign` 惰性重签机制）。
