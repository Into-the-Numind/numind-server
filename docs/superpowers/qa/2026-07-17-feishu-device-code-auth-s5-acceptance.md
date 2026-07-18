# Feishu Split Device Authorization — S5 Acceptance

## 验证环境

- 后端：本地 feature worktree，Go race detector 开启
- 前端：本地 feature worktree，Vue 3 + Chromium/Playwright
- 浏览器：gstack launched Chromium；本地登录页可正常渲染且无首屏 console error

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go lint | `PATH="$(go env GOPATH)/bin:$PATH" task lint` | PASS | 无 lint 错误 |
| Go full non-race | `go test -tags sqlite_fts5 ./...` | PASS | 全仓通过 |
| Go feature race | `go test -tags sqlite_fts5 -race ./internal/numind ./internal/numind/biz ./internal/numind/biz/agent ./internal/numind/controller/v1/agent` | PASS | 飞书接线、业务、Agent 与 controller 全部通过 |
| Go repository race | `task test` | BASELINE_FAILURE | 仅未改动的 `internal/numind/biz/sandbox` SkillsDir 测试竞态失败；在原始 `develop` 上用同包命令独立复现 |
| Vue lint | `npm run lint` | PASS | 0 errors；7 个既有且不在 feature diff 内的 warnings |
| Vue type-check | `npm run type-check` | PASS | |
| Vue unit | `npm run test:unit` | PASS | 96/96 files；1114 passed、11 skipped、3 todo |
| Feishu E2E | `npx playwright test e2e/feishu-personal-workspace.spec.ts --project=mocked --workers=1` | PASS | 5/5；桌面、移动端、过期、终态、历史缺链接场景 |
| Diff hygiene | `git diff --check` + `git status --short` | PASS | 两个 worktree 均干净 |

## 浏览器 QA

- gstack 输出：`numind-web-v3/.gstack/qa-reports/qa-report-127-0-0-1-2026-07-17.md`
- 本地 Vite 登录页正常渲染，首屏无 console error；本地未启动后端，提交登录后的代理 500 属测试环境缺失，不计为 feature 回归。
- 5 条 Playwright 浏览器契约证明：新链接、二维码、处理中、思考内容与正式回复在同一页面连续出现，不依赖刷新；同一 operation 只有一张卡片且不会调用普通 answer 路径。
- 结论：无本功能 P0/P1/P2 浏览器回归。

## 可观测性验证

- 本功能没有新增 LLM 调用，Langfuse 为 N/A。
- start、complete、lease、candidate、replacement 与 dispatch observer 的字段均为 allowlist；自动 secret scan 覆盖 URL query、device code、token、App Secret、HOME 与业务正文。

## PRD 验收标准核对

| 验收标准 | 结果 | 备注 |
|----------|------|------|
| 第一个代码 commit 是失败的客户复现测试 | PASS | 后端 `c11e1e83`，前端 `3c5c40b` |
| start 使用固定 lark-cli 1.0.68 非阻塞协议并立即返回卡片 | PASS | 严格 fixture、argv 与短进程边界测试通过 |
| 恢复凭据加密且绑定 exact user/generation/app/operation/session/scope/expiry | PASS | cipher AAD、篡改、轮换、跨用户测试通过 |
| 凭据不进入 API、前端、LLM、日志、错误和普通 sandbox | PASS | allowlist projection、secret scan、shell boundary 测试通过 |
| 点击继续完成 exact session、密封 HOME、connected 并清除凭据 | PASS | fenced publication、transaction rollback 与 terminal clearing 测试通过 |
| 原 operation 自动恢复且业务写最多一次 | PASS | 重复、并发、响应丢失、双实例与 dispatcher recovery 测试通过 |
| 过期、拒绝、异常与解密失败显示可恢复状态 | PASS | 200 notice、typed 409/503 与 scrubbed 500 测试通过 |
| 重启与另一实例可继续有效 session | PASS | 新 instance 只共享 DB 与加密 Vault 的测试通过 |
| Agent 只通过 `lark_execute` 操作飞书 | PASS | shell AST adversarial suite 与系统指引测试通过 |
| Docs/Base/Wiki 编排与命令域无关 | PASS | create/read/update 的 durable fixtures 与 exact argv oracle 通过 |
| 页面不刷新即可继续显示思考和正式回复 | PASS | Playwright 5/5 + reasoning reconciliation unit regression |
| Dev 真实连接开始与官方授权链接生成 | PASS | HTTP 200 / code=0；返回 accounts.feishu.cn 新协议链接，不再出现 Internal server error |
| Dev 真实飞书首次授权、热路径和完整 CRUD | PENDING_USER_AUTH | 需用户在刚生成的飞书官方页面完成授权后继续 |

## 独立审查

- Specification review：PASS，P0/P1/P2/P3 = 0。
- Quality review：PASS，P0/P1/P2/P3 = 0。

## Dev 故障诊断后的协议修正

- 真实 Dev 调用确认 lark-cli v1.0.68 当前返回
  `accounts.feishu.cn/oauth/v1/device/verify`，而不是旧版
  `open.feishu.cn/suite/passport/oauth/device`。原后端因此把 CLI 成功结果误判为协议错误并返回
  `Internal server error`。
- 后端现同时支持新旧官方协议，但分别严格校验 host、path 和 query；不记录、不重建、不回显
  device code 或完整 URL。
- 前端 API 与 Agent Store 复用同一个 phase-aware 校验器：`accounts.*` 只允许
  `user_auth`，且必须为固定 path、恰好一个 `flow_id` 和一个 `user_code`；错误 path、缺失、
  额外或重复参数全部 fail closed。`open.*` 保留既有多阶段兼容策略。
- 客户回归 commit 顺序符合 RED → GREEN：后端 `35014cba` → `87c69e40`；前端
  `7cbaf4e` → `9ea6bd8`，安全收紧追加 `4665fcf` → `3a6e984`。
- 修正后验证：后端关键回归与 lint PASS；前端聚焦 82/82、全量 1114 tests、lint、
  type-check、production build、Playwright 5/5 全部 PASS；双 reviewer 均 PASS 且无 findings。

## Dev 部署与真实连接验证

- 后端 Dev 镜像：`numind-server:develop-919060ed`，健康检查 PASS。
- 前端 Dev 镜像：`numind-web-v3:develop-2498fbd`，健康检查 PASS。
- 登录真实 Dev 账号后，`POST /api/v1/feishu/connect` 返回 HTTP 200、`code=0`、
  `state=waiting_user_auth`、`phase=user_auth`，并带有 live URL。
- 为避免泄露一次性授权信息，验收只记录 URL 结构：host 为 `accounts.feishu.cn`，path 为
  `/oauth/v1/device/verify`，query key 恰为 `flow_id`、`user_code`；没有记录参数值或完整链接。
- 页面 console error 为 0；部署后两个容器的 panic/fatal/error 计数均为 0。
- 下一门禁是用户在飞书官方页面完成本次授权。之后继续验证原 Agent 自动恢复，以及
  Docs/Base/Wiki create/read/update 的真实业务闭环。

## Dev run 204 托管技能冲突修复

- 真实 Dev run 204 在飞书 device session 已过期后连续发起四次 `lark_execute`，但没有创建任何
  Feishu operation 或 auth-session 记录。根因不是飞书服务波动，而是上游官方技能中的本地电脑
  CLI 指引（`auth` / `config` / `whoami` / 手工凭证）与有数托管执行边界冲突；通用错误文案又诱导
  Agent 盲目重试。
- 客户 RED `d60a0c91` 固化该回归。托管工具现在通过独立 `hosted_policy` 明确优先规则，不改写签名
  skill 原文；Agent 直接执行 Docs/Base/Wiki，连接或权限不足由平台自动生成授权卡，不再索要
  App ID/App Secret。
- CommandCatalog 在业务参数解析上下文中兼容官方固定 `--as user` / `--format json`，仍由平台重建
  固定身份与 JSON 输出。正文值恰为 `--as=user` 或 `--format=json` 时会原样保留；重复 flag、bot
  身份及非 JSON 输出继续 fail closed。
- 同一 Agent run 首次命令拒绝后只允许一次纠正。第二次仍被拒绝即进入 exhausted，第三次在触达
  executor 前阻断；mutex 状态机同时封住并发窗口。不同 run 相互隔离，正常完成会复位，Run 与
  RunStream 退出都会清理状态。
- 修复验证：`go test ./...` PASS；`task lint` PASS；Agent/Feishu 完整包 PASS；新增并发纠正竞态
  测试在 `-race -count=20` 下 PASS；规格审查和代码质量审查均 PASS，无 P0/P1/P2。

## 结论

AUTOMATED_PASS_WITH_BASELINE_EXCEPTION。功能代码可合并并部署 Dev；唯一全仓失败在原始 `develop` 同样存在，且目录不在 feature diff。Dev 真实飞书验收在部署后继续记录到本报告。

## 托管技能修复 Dev 部署

- `ndf-done` 已将修复合并并推送到 backend `develop`：`ef19dd85`。
- Dev 运行镜像：`numind-server:develop-ef19dd85`；Docker 状态 `running` / `healthy`。
- 外部 `GET /healthz` 返回 `code=0`、`status=ok`；部署后最近十分钟 panic/fatal/error 日志计数为 0。
- 下一步人工门禁：用户重新发起同一飞书文档创建请求，完成新授权卡后确认原 Agent 能自动继续且不再出现
  连续“执行出错”或本地 CLI/App Secret 指引。Prod 未部署、也未获得授权。

## Dev run 209 文档读取错误修复验收

- Dev run 209 的三次 `docs +fetch` 并非连接丢失或飞书服务波动。对同一加密 HOME 的只读脱敏重放证明：
  固定版本 lark-cli 1.0.68 在缺少读取权限时以退出码 3 结束，stdout 为空，并把完整的 code-less
  `authorization/missing_scope` envelope 写入 stderr；精确缺失 scope 为 `docx:document:readonly`。
- 客户 RED `e36d2806` 先于修复，复现了 runner 丢失 stderr envelope、classifier 无法生成增量授权的旧行为。
  修复 `8d0526db` 保留严格的 stderr-only envelope 并识别真实 code-less tuple；安全收紧 `daea8ba8`
  只允许明确正退出码使用 stderr，拒绝 stdout/stderr 双 envelope，并确保无 code 的已开始写操作进入
  Unknown、不会授权或重放。
- 自动验收：`go test ./...` PASS；`go test -race ./internal/numind/biz/feishu -count=1` PASS；
  `PATH="$(go env GOPATH)/bin:$PATH" task lint` PASS；飞书完整包与所有定向客户/安全回归 PASS；
  `git diff --check` PASS。
- 独立规格审查与代码质量审查均为 PASS，P0/P1/P2 = 0。
- 结论：S5 AUTOMATED_PASS，可合并并部署 Dev。真实产品验收必须重新发起一次文档读取：首次应只申请
  `docx:document:readonly`，批准后自动继续原读取；再次读取不应重复授权。Prod 未授权。

## 文档读取修复 Dev 部署与真实前半程验收

- `ndf-done` 已合并并推送 backend `develop`，合并提交为 `6481ae2f`；feature worktree 与本地分支均已清理。
- Dev 运行镜像为 `numind-server:develop-6481ae2f`，容器状态 `running` / `healthy`；固定 CLI 版本仍为
  1.0.68；`GET /healthz` 返回 `code=0` / `status=ok`；部署后 critical log count 为 0。
- 自动化使用同一测试账号和 run 209 的历史会话发起新的只读验收 run 210。Agent 完成技能读取后只调用一次
  `lark_execute`，随即发出一张 live `user_auth` 外部动作卡，并以 `waiting_for_user_choice` 暂停；没有再出现
  三次 `failed` 或盲目重试。
- Dev MySQL 的持久证据为：command path `docs +fetch`、risk `read`、operation state `waiting_user_auth`、
  attempt count `1`、auth phase `user_auth`、requested scopes 精确为 `["docx:document:readonly"]`、session state
  `pending`。未记录授权 URL 或凭据值。
- 真实前半程验收 PASS。最后人工门禁：用户完成这张卡片的飞书官方授权；系统应自动继续同一读取并返回正文，
  随后第二次读取不得再次要求授权。Prod 未部署、也未获得授权。
