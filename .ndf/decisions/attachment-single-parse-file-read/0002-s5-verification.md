# S5 完整质量门禁

日期：2026-07-21
范围：`attachment-single-parse-file-read` 后端、前端及迁移

## 结论

功能范围 ALL_PASS；无已知 P0/P1，可以进入 S6 合并与 Dev 部署。

## 通过项

- migration：`go test ./migrations -count=1` 通过。
- Agent 全包：`go test ./internal/numind/biz/agent/... -count=1` 通过。
- Agent 竞态：`go test -race ./internal/numind/biz/agent/... -count=1` 通过。
- file_read、上传 worker、lifecycle、factory、schema 定向回归通过；覆盖单次解析、缓存分页完整重组、零 HEAD/零 parser、当前用户隔离、处理中等待与失败恢复。
- 后端 `task lint` 通过；初次检查发现并删除已失效的预签名辅助代码后重新运行绿色。
- 前端相关测试 44 passed、3 todo；完整 Vitest 99 files、1136 passed、11 skipped、3 todo。
- 前端 `npm run type-check` 通过；`npm run lint` 为 0 errors，保留 7 条既有 warning。
- 两仓库 `git diff --check` 通过；未修改 `config_prod.yaml`，无视觉样式变化。

## 全量基线隔离

执行了后端 `go test ./...`。与本功能相关的包全部通过，但仓库全量仍暴露两类既有、非本次失败：

1. `internal/numind/biz/xhsscript` 的两个 analytics summary 用例在 feature 与主仓库 develop 上均返回全零并失败。
2. 广域聚合运行时，`internal/numind/biz/agent` 有用例偶发请求真实 DMX，因免费额度耗尽返回 403；同一 Agent 全包单独运行及竞态运行均通过。

以上失败均已在未包含本 feature 代码的 develop 基线上复现，且不位于本次改动路径；未在本 feature 中修改或掩盖。

## 发布决定

采用后端先、前端后的滚动部署。后端继续兼容 legacy URL，前端优先发送安全正整数 attachment ID，ID 缺失时保留 URL 回退，因此两次部署之间兼容。
