# 文档系统（Document System）v1 — S5 自动验收报告

> 关联 spec/plan/T10 验证策略。验证日期：2026-06-16。环境：本地 worktree（feature/document-system）。

## §1 持久回归层（逻辑，已绿）

### 后端（numind-server worktree）
- `go test` 本 feature 全部包 **PASS**：
  - `internal/numind/biz/document`（IDOR/ownership/parse 链/源过期/懒建档→编辑→二次打开/超限/并发 race/导出 md/守卫/降级）
  - `internal/numind/store`（document CRUD/查命中/miss ErrRecordNotFound/用户隔离/UpdateContent NotFound）
  - `internal/pkg/util`（DownloadFromCOS 404/403/500/nil 分类）
  - `internal/pkg/errno`（编译）
  - `internal/numind/controller/v1/document`（编译；controller 按约定 E2E 覆盖不单测）
- `go vet ./...` **PASS**（含所有 document 包）。
- `task lint` 的 `go vet` 步通过；`golangci-lint` 因 `go install` 后二进制不在 PATH 而无法运行（**环境问题，非代码**，develop baseline 同样受影响）。

### 前端（numind-web-v3 worktree）
- 全量 `vitest`：**953 passed / 0 failed**（89 文件，含 request.ts 共享改动无回归）。
- 新增覆盖：documents store（open/debounce 合并/lost-update 回归/保存失败/导出/reset，8）、isEditable（4）、MilkdownEditor（wiring/emit/readonly/destroy/卸载竞态，5）、AgentArtifactItem 入口可见性矩阵（flag×mime，11，AC1）。
- `eslint` 改动文件 **0 error**；`vue-tsc type-check` **PASS**。

## §2 既有基线失败（非本 feature 引入，已隔离取证）

`go test ./...` 有 3 个包 FAIL，经对照 **develop 主 checkout（无本 feature 改动）跑出完全相同的失败** → 确认 develop baseline 既有，与 document-system 无关：

| 包 | 失败测试（节选） | baseline 同样失败？ |
|----|------|------|
| `biz/salesrag` | TestAcquireSalesragCredits_* / TestFinalize_StreamErrorTriggersRefund | 是（develop 相同） |
| `controller/v1/agent` | TestStudentQueryCtrl_ListAvailableSkills_ParentEmpty | 是（develop 相同） |
| `controller/v1/credit` | TestGrantMembership_*（Trial/Monthly/Idempotency 等） | 是（develop 相同） |

> 我对 salesrag_test.go 的唯一改动是给 `realBizOnlyCustomers` stub 补 `Document()` 方法（IBiz 新增方法的必要适配），让其**编译**；运行时失败是 develop 既有（多为 sqlite 测试环境与 MySQL 特性差异）。遵 `feedback_dev_deploy_isolate_root_cause`：非己引入，告知用户不 chase。

## §3 需 dev + 用户走查的验收项（S6，按 T10 策略）

以下因 **Milkdown(ProseMirror)/pandoc 需真实运行时** + 需 **agent 实际生成的 docx 产物**才能打开 + 用户偏好亲手走查（`feedback_walkthrough_user_executes`），留到 S6 dev：

1. **US1-US5 真链路**（dev，flag on，用户驱动）：
   - agent 对话生成 docx/md → 卡片出现"打开编辑"
   - 点开 → WYSIWYG 显示格式化内容（标题/加粗/列表/表格）
   - 改一处 → 顶部"保存中…→已保存" → 刷新重开为最新版
   - 下载 md / pdf / docx → 文件可正常打开（docx 用 Word，pdf 中文不乱码）
2. **跨用户隔离探针**（独立测试账号 B 拿账号 A 的 agent-outputs URL 调 open → 403），须征用户同意 + 独立账号。
3. **前置**：T6 沙箱 skill 镜像须重建 push（加 pandoc）才能验 pdf/docx 真实产出；未就绪则 pdf/docx 路径记 deferred（md 不受影响）。

**回归诚实声明**：WYSIWYG 编辑体验与 pandoc 真实产出无持久自动回归（一次性 dev 确认）；逻辑层（越权/解析/自动保存/导出守卫/降级）有 go test + vitest 持久守护。

## §4 S5 Gate 结论
- 本 feature 逻辑层全绿（go test + vitest + vet + eslint + type-check）。
- 3 个基线失败属 develop 既有，非阻塞本 feature。
- 视觉/集成 E2E 按 T10 策略留 S6 dev + 用户走查（运行时依赖 + 用户偏好）。
- **建议**：进入 S6（ndf-done 合 develop → dev 部署 → 用户走查），但 merge/deploy 属外向操作，需用户授权。
