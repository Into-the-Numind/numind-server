# QA Report — 三 Agent 飞书内容流水线

## 验证环境

- 日期：2026-07-20（Asia/Shanghai）
- 后端：`feature/three-agent-feishu-pipeline` server worktree；自动化测试使用隔离 fake store / fake Lark / fake Langfuse。
- 前端：`feature/three-agent-feishu-pipeline` web worktree，Vite `http://127.0.0.1:5173`；浏览器只读冒烟经本地代理访问可用 Dev API。
- 浏览器：gstack browse 管理的 Chromium，桌面 `1280x720` 与移动端 `375x812`。
- 数据边界：未触发 Agent 运行、未扣积分、未连接或写入真实客户飞书。后端经 SSH 隧道验证数据库可达后，因启动路径进入自动 schema migration 而立即中止；停止前日志仅观察到 `SELECT` / `information_schema` 查询，未观察到 DDL 或业务数据写入。

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go lint | `PATH="$(go env GOPATH)/bin:$PATH" task lint` | PASS | 仅 macOS sqlite deprecation warning |
| Go 全量测试 | `go test ./... -count=1` | PASS | 全包通过；Feishu 包约 130 秒 |
| 三 Agent 重点包 | `go test ./internal/numind/biz/agent ./internal/numind/biz/skill ./internal/numind/store ./internal/pkg/aiservice/middleware ./internal/pkg/parser -count=1` | PASS | Agent 1/2/3、XHS、file_read、权限与 tracing 均通过 |
| 可观测性与工作流 | 重点 `go test ... -run 'Test(ThreeAgentPipelineWorkflow\|PipelineRunMetrics\|PipelineMetrics\|...Langfuse...\|LarkExternalResumeIntegration)' -v` | PASS | 三 Agent 矩阵、243 条 checkpoint、指标白名单、敏感信息隔离与授权续跑通过 |
| Vue lint | `npm run lint` | PASS | 0 error，7 个既有 warning |
| Vue type-check | `npm run type-check` | PASS | `vue-tsc` 退出 0 |
| Vue unit | `npm run test:unit` | PASS | 99 files；1129 passed、11 skipped、3 todo |
| 相关浏览器契约 | `npm run test:e2e -- --project=mocked e2e/feishu-personal-workspace.spec.ts e2e/agent-tool-recovery.spec.ts` | PASS | 13/13；覆盖授权卡、刷新、原调用续跑、unknown/recovery 语义 |
| mocked 全集 | `npm run test:e2e -- --project=mocked` | BASELINE_EXCEPTION | 20 passed、1 skipped、1 failed；失败为既有 `question_prompt` 用例仍断言 `.msg-final`，截图中最终回答文本实际已显示，单独重跑稳定复现 |

## 浏览器 QA

- gstack 输出：`numind-web-v3/.gstack/qa-reports/qa-report-localhost-2026-07-20.md`
- 截图目录：`numind-web-v3/.gstack/qa-reports/screenshots/`
- 已验证路径：登录、工作区、运行记录、客户管理、知识库、小红书选题库、技能市场、Agent 聊天入口。
- 功能结果：桌面端导航、XHS 查询/筛选/分页、Agent 输入区/附件入口/快捷任务均可达；未点击会触发模型调用或外部写入的操作。
- 浏览器健康分：89/100。
- P0：0。没有发现由本 feature 引入的视觉或功能回归。

### Develop 基线关注项（不阻塞本 feature）

1. **P1 / 移动端 Agent 聊天**：`375x812` 下左侧会话栏默认展开并遮挡主体，移动端聊天基本不可用。证据：`agent-chat-entry-mobile.png`。
2. **P2 / XHS 历史封面**：选题库出现两个历史封面资源 403；表格数据与操作仍加载。证据：`xhs-library.png` 与同一时间戳的两条 console error。
3. **P2 / E2E 断言漂移**：`question_prompt` 最终文案已显示，但旧用例只接受 `.msg-final` DOM 类，导致 mocked 全集 1 个稳定失败。证据：`numind-web-v3/test-results/agent-streaming-Scenario-5-db61c-m-resumes-with-final-answer-mocked/test-failed-1.png`。

以上问题均存在于未包含本 feature 前端改动的 develop 基线；当前仓库归属模式为 unknown，因此未混入本 feature 修复。

## 可观测性验证

- [x] `agent_pipeline_metrics` 对 Agent 1/2/3 仅记录版本化标量白名单；stream 与 non-stream 共用同一解析器。
- [x] Agent 1 的 `processed/skipped/failed/remaining`，Agent 2/3 的 `source_count/output_mode` 可从最终标记解析；缺失、重复、未知字段均降级为 `unavailable`。
- [x] `xhs_note_list` 与 `file_read` trace 只保留安全分页元数据；预签名 URL、文档正文、解析器原始错误不会进入 Langfuse。
- [x] 正式测试已验证 fake Langfuse sink 收到与日志相同的安全 map。
- [ ] 未执行真实模型 generation：本机没有模型 provider key，且本轮明确禁止扣积分和真实飞书写入。
- 结论：`VERIFIED`（instrumentation contract）；真实 provider generation trace 留给 S6 隔离 Dev 测试身份的人类验收，不作为 S5 feature gate。

## PRD 验收标准核对

| 验收标准 | 结果 | 备注 |
|----------|------|------|
| Agent 1 手动触发后自动扫描当前用户 XHS 库，默认只处理未分析记录 | PASS | current-user store + 默认 completion SOT；无需人工勾选 |
| `xhs_note_list` 单页上限 100，稳定遍历超过 100 条并可 checkpoint 恢复 | PASS | 243 条按 100/100/43；40 条 checkpoint 后恢复剩余 203 条 |
| 缺失 Base 目标先询问；0 个创建、1 个复用、多个精确命中询问 | PASS | `Agent1BaseTargetResolution` 全矩阵通过 |
| Agent 1 写入完整 34 字段，原始字段可追溯到 `xhs_note_list(full)` | PASS | 逐条 raw lineage 检查；类型 normal/video/null 映射完整 |
| Agent 2 完整读取上传/飞书来源并写客户级 profile Doc | PASS | EOF 后才写；`profile/v1` 成对标记与七模块均校验 |
| Agent 3 读取 Agent 1 + Agent 2 产物并写客户级 topic Doc | PASS | `topics/v1`、合法 round、逐选题九字段/taxonomy/主语规则均校验 |
| 新轮次 append，显式指定旧轮次才精确替换；unknown write 不盲重放 | PASS | round-marker 对账与 `replace-round` 矩阵通过 |
| 未连接飞书时进入官方授权并恢复原始工具调用 | PASS | 后端真实 durable external-action integration + 前端 13/13 浏览器契约 |
| 不同 Numind 用户隔离；工具权限由 AgentDefinition 强制而非仅靠提示词 | PASS | current-user store、tenant-aware history、Runner authoritative flags 全部通过 |
| 不增加额外客户/飞书目标配置 UI | PASS | web 仓库无 feature 源码差异 |

## 环境说明

- `task dev` 当前默认查找 `config.yaml`，不会自动选择 `config_local.yaml`。
- 仓库现有 `config_local.yaml` 含重复 `billing` key，YAML 解析失败；`config_dev.yaml` 使用 Docker 内网 DB/Redis 名称。
- 经 SSH 隧道和 Viper 环境覆盖后数据库可连通，但应用启动会自动执行 schema 自检/迁移，因此本轮没有继续做 feature server 的 HTTP 冒烟。该限制不影响隔离 Go 集成测试和浏览器契约结果。

## 结论

`ALL_PASS`（feature scope）。S5 gate 通过：自动化、浏览器、可观测性合同和 PRD 验收均无本 feature P0/P1/P2；三个 develop 基线关注项单独记录，不返回 S4。

## 失败项修复要求

无本 feature 失败项。进入 S6 后先原子合并，再用隔离测试用户在 Dev 完成真实三 Agent 创建/授权/飞书测试资源验收；禁止使用客户真实文档做首轮 smoke。
