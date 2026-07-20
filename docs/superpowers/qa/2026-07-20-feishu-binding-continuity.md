# QA Report — 飞书绑定连续性

## 验证环境

- 后端：`/private/tmp/wt-feishu-binding-continuity-numind-server`，HEAD `578817e2`
- 前端：`/private/tmp/wt-feishu-binding-continuity-numind-web-v3`，HEAD `1b72168`
- 浏览器：Playwright Chromium，mocked localhost 项目

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go lint | `PATH="$PATH:$(go env GOPATH)/bin" task lint` | PASS | `go vet` 与 `golangci-lint` 通过 |
| Go full test | `go test ./... -count=1` | PASS | 全仓通过；Feishu 包 133.294s |
| Go race | `go test -race ./internal/numind/store ./internal/numind/biz/agent ./internal/numind/biz/feishu ./internal/numind/biz -count=1` | PASS | store、agent、feishu、biz 全部通过 |
| Vue lint | `npm run lint` | PASS | 0 errors；7 个既有非本功能 warning |
| Vue type-check | `npm run type-check` | PASS | `vue-tsc --build --force` 通过 |
| Vue unit | `npm run test:unit` | PASS | 99 files；1135 passed、11 skipped、3 todo |
| Feishu E2E | `npx playwright test e2e/feishu-personal-workspace.spec.ts --project=mocked --workers=1` | PASS | 10/10，桌面和移动端 |
| Diff hygiene | `git diff --check` + prod/migration exclusion | PASS | 无 whitespace；未改 `config_prod.yaml` 或 `migrations/` |

## 浏览器 QA

- Playwright trace：`.gstack/qa-reports/playwright-artifacts/feishu-personal-workspace--a9703--stay-live-without-a-reload-mocked/trace.zip`
- 截图证据：trace 中确认旧授权步骤被原位替换为当前 user-auth 卡，按钮显示“正在检查…”，原任务保持处理中且没有重复卡。
- 覆盖路径：`create_app → app_scope → user_auth → original Agent continuation`，包括旧 DOM、URL-less snapshot、重载恢复、过期刷新、并发终态轮询、exactly-once continuation。
- 视觉/交互结论：无 P0/P1；用户只看到一个当前可操作步骤。URL 丢失会自动恢复，失败/过期仍保留手动恢复入口。

## 安全与持久化检查

- 浏览器 mutation 必须同时匹配 operation、session、run 和 session epoch。
- stale/missing session 只读；不轮询当前授权、不确认/取消/重放原 Feishu 操作。
- Agent Run handoff 使用 user/run/operation/tool/session CAS 和最大 32 个 superseded session lineage，防止延迟 worker 倒退。
- URL、device code、scope、secret、argv 和 HOME 不写入 Agent durable action、operation summary 或日志。
- app-scope 重载只从当前 generation 的非敏感 app id 构建固定官方 `/app/{id}/auth` 路径；原 classifier URL/query 不落库。

## PRD 验收标准核对

| 验收标准 | 结果 | 备注 |
|----------|------|------|
| user 438 的旧 create-app 卡不会再错误轮询当前 user-auth session | PASS | 后端 customer regression + stale read-only test |
| 每次阶段推进都持久更新 Agent 当前卡 | PASS | dispatcher、refresh、CAS handoff 回归 |
| 重复点击、旧标签、重载、并发响应不会覆盖新步骤 | PASS | operation/session/run/epoch fence + Vitest/Playwright |
| app-scope 在服务重启后仍可恢复审批入口 | PASS | 独立 service restart 回归；URL 不落库 |
| 完成授权后自动衔接原 Agent 任务且至多一次 | PASS | 完整 mocked Playwright 10/10 |
| 滚动发布兼容 | PASS | missing session read-only；后端先部署 |

## 独立审查

- State/Concurrency reviewer：PASS，P0=0、P1=0。
- Security/Privacy/UX reviewer：PASS，P0=0、P1=0、P2=0。
- 两仓第一个 feature commit 均为独立失败复现测试。

## 可观测性验证

- 结论：N/A。本修复不新增 LLM 调用或 generation。

## 结论

ALL_PASS。可以原子合并，按后端优先顺序部署 Dev；生产环境明确不在本次范围内。
