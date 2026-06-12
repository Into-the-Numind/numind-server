# Plan — tool-soft-error-sweep

> 串行执行（Tier 4 默认；改动文件少且同包，无并行收益），每 task 完成后：commit 验证（Rule 8）→ 双 Sonnet reviewer 并行（Rule 6）→ P0/P1/P2 现场修 → manifest reviewed_tasks += 1。

## T0 — 失败复现测试（Rule 11 首 commit）

- 新建 `internal/numind/biz/agent/tool_soft_error_sweep_test.go`：表驱动覆盖 spec §2 清单中全部工具的输入错误路径（非法类型 + 缺字段）
- 线上事故用例必含：`web_fetch` + `{"url":"https://example.com","prompt":true}`（run 136）、`image_gen` + `{}`（run 137）
- 预期：此时测试 **RED**（hard error 工具返回非 nil error）
- commit: `test(qa): reproduce tool hard-errors killing agent runs (dev run 136/137)`
- 验收：`go test ./internal/numind/biz/agent/ -run TestToolSoftErrorSweep` 失败且失败原因是断言 err 非 nil

## T1 — 共享 helper + P0 工具（image_gen、web_fetch）

- 新增 `tool_soft_error.go`（spec §1）
- image_gen L74-78、web_fetch L96-97 改 soft（复用各自文件内既有 helper）
- 验收：T0 测试中 image_gen/web_fetch 用例转 GREEN；`go test ./internal/numind/biz/agent/`
- commit: `fix(agent): soft-error image_gen + web_fetch malformed input (run 136/137)`

## T2 — create_* 四件套 + png_chart 上传路径

- create_csv / create_json / create_html / create_text 全部输入与可恢复错误改 soft（spec §2 #3-6）
- create_png_chart 上传失败 `, err` → `, nil`（spec §2 #7）
- 验收：对应测试用例转 GREEN
- commit: `fix(agent): soft-error create_* file tools on malformed input`

## T3 — kb_search + memory_read/write + document_generate

- spec §2 #8-11
- 验收：对应测试用例转 GREEN；T0 全表 GREEN
- commit: `fix(agent): soft-error kb_search/memory/document_generate`

## T4 — S5 验证策略（Rule 10 专项 task）

- **验证方式：仅后端 Go TDD + dev 部署冒烟。不做 Playwright / gstack /qa。**
- **理由**：纯 biz 层工具错误处理，无 UI/无新端点/无支付权限逻辑；行为契约（烂参数 → soft error → run 存活）可被单元测试完整表达；唯一端到端关注点（Eino 收到 nil error 不杀 run）已由既有加固工具在 run 143/145 的线上行为证实，且 T0 断言的就是该契约的工具侧
- **关键路径**：
  1. `go test ./...` 全仓 0 FAIL（S4 期间每 task 跑包级，收尾跑全仓）
  2. `task test`（race + coverage）
  3. `task lint` 0 issue
  4. ndf-done + `/deploy-dev server` 后，dev 实测定位调研助手发起一次正常调研（冒烟：run 正常推进到提问或完成，不因工具参数死亡）
- **回归保护诚实声明**：T0 测试永久留库，未来任何人把 soft error 改回 hard error 会立即 RED

## 排除项（明确不在本 feature）

- 退化检测 / think 标签清理 / 质量 fallback（用户拍板不做，Claude Code 亦无）
- Eino 升级、输入类型 coerce 扩展、管理端可视化
