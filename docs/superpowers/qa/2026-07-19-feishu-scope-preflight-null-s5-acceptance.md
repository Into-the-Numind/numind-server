# QA Report — 飞书授权成功后原任务恢复失败

## 验证环境
- 后端：本地 feature worktree
- 固定外部契约：`lark-cli 1.0.68` 官方源码与测试
- 前端/浏览器：N/A，本修复不修改 HTTP 契约或 UI

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go lint | `PATH="$(go env GOPATH)/bin:$PATH" task lint` | PASS | vet 与 golangci-lint 通过 |
| Go 全仓测试 | `go test ./...` | PASS | 最终 HEAD `2b36f812` 通过 |
| 修改范围 race | `go test -race ./internal/numind/biz/feishu -run '^(TestControlledScopePreflight_\|TestOperationService_WritePreflight...)'` | PASS | 解析、恢复与 exactly-once 覆盖 |
| 既有时序波动复核 | 三个 Feishu 时序用例 `-race -count=5` | PASS | 全包并发时曾超时，隔离连续通过 |
| 双独立审查 | spec + quality | PASS | P0=0、P1=0、P2=0 |
| diff hygiene | `git diff --check` | PASS | 无 whitespace/意外文件 |

## 完整 `task test` 基线例外

普通全仓测试通过。`task test` 的仓库级 race 阶段仍在未修改的 `internal/numind/biz/sandbox` 测试夹具中报告已知数据竞争：测试在 `NewPool` 后台 `spawnWorker` 仍读取 `SkillsDir`/seccomp globals 时直接改写这些字段。该问题已在前序 Feishu S5 记录为 develop 基线；本分支未修改 sandbox 文件。修改相关 Feishu race 门禁全部通过，因此不是本修复引入的回归。

## PRD 验收标准核对

| 验收标准 | 结果 | 备注 |
|----------|------|------|
| 成功 `missing:null` 返回全部已授权 | PASS | 客户 RED 由失败转绿 |
| 部分缺失返回精确授权范围 | PASS | exact partition + suggestion |
| `not_logged_in`/`no_token` 安全恢复 | PASS | 仅官方两种 error shape |
| 畸形/冲突响应 fail closed | PASS | 缺失、null optional、case collision、unknown、duplicate、trailing、stderr、exit 均覆盖 |
| 授权恢复只执行一次业务写 | PASS | 已有 operation exactly-once 测试通过 |
| 全量检查与审查 | PASS_WITH_BASELINE_EXCEPTION | 仅无关 sandbox test-fixture race |

## 结论

ALL_PASS_WITH_RECORDED_BASELINE_EXCEPTION。允许执行 `ndf-done` 并部署 Dev；真实飞书写入由用户在 Dev 做最终产品验收。
