# QA Report — Feishu Resource Discovery

## 验证环境
- 后端：本地 feature worktree，Go 与固定版本 `lark-cli 1.0.68`
- 前端：N/A（本次仅修改后端工具边界与能力投影）
- 浏览器：N/A

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go lint | `PATH="$(go env GOPATH)/bin:$PATH" task lint` | PASS | 无 lint 发现 |
| Go test | `go test ./... -count=1` | PASS | 全仓库非 race 测试通过 |
| Feishu/store race | `go test -race ./internal/numind/biz/feishu ./internal/numind/store -count=1` | PASS | 用户隔离、能力状态与命令边界通过 race 检查 |
| Agent/Feishu/store focused | `go test -count=1 ./internal/numind/biz/agent ./internal/numind/biz/feishu ./internal/numind/store` | PASS | 包含客户标题读取回归测试 |
| 固定 CLI 契约 | `lark-cli version` 与 `--dry-run` | PASS | 版本为 1.0.68；Drive Search v2 与 Wiki space-list 官方请求形状匹配 |
| Skill 可用性 | `lark-cli skills read lark-drive --json` | PASS | 托管环境可读取官方 Drive 指南 |
| Vue / Admin / E2E | N/A | N/A | 无前端或管理端改动 |

## 浏览器 QA
- N/A：本次没有 UI 改动；授权卡沿用已验收的现有组件和 API 契约。

## 可观测性验证
- 结论：N/A。此改动不新增 LLM provider 调用；Feishu 操作继续写入既有 operation、capability 与 Agent run 观测链路。

## PRD 验收标准核对

| 验收标准 | 结果 | 备注 |
|----------|------|------|
| 任意 Agent 均通过当前登录用户的持久 Feishu 连接执行 | PASS | 工具调用继续以当前 user ID 与 generation 隔离，双用户回归通过 |
| 新对话仅给文档标题时先安全发现，再读取资源 | PASS | 托管策略使用只读 `drive +search`，精确标题匹配后按 Docs/Wiki/Base 路由 |
| Drive 仅开放安全只读发现，不开放写入或 IM | PASS | 命令目录只允许受限搜索；Drive 写操作和 IM 仍被拒绝 |
| Wiki 空间列表可按官方只读命令使用 | PASS | `wiki +space-list` 与 `wiki:space:retrieve` 已加入严格分页边界 |
| 搜索分页、歧义和不支持类型均 fail closed | PASS | 最多 5 页/100 条；零结果、多结果、截断、不支持类型均有明确规则和回归测试 |
| 缺少增量权限时沿用执行优先的授权恢复 | PASS | 新 scope 进入现有 user-scoped device authorization 与原任务续跑链路 |
| 平台拒绝不得误报为飞书连接故障 | PASS | 受控目录现在接受合法读取命令；非法参数仍返回可行动的平台边界错误 |

## 独立审查
- 规格审查：PASS，P0/P1/P2 = 0。
- 代码质量审查：PASS，P0/P1/P2 = 0。

## 结论
ALL_PASS。允许合并到 `develop` 并部署 Dev。真实用户标题读取与可能出现的一次增量授权作为部署后的产品验收，不阻塞合并。

## Dev 失败后的 S4 修复验收

首次 Dev 验收的 runs 212/213/215 证明：`lark-drive` 技能的完整工具结果约 28 KiB，超过通用 16 KiB artifact 触发阈值后，模型只能看到 1 KiB preview，位于结果尾部的签名 receipt 被隐藏。命令因此在服务器策略边界被拒绝，尚未访问飞书。

修复保持安全校验不变，并增加以下边界：

- 只有服务端私有具体 `lark_skill_read` 工具可原子返回技能正文与 receipt；仅名称相同的工具仍走普通 artifact 路径。
- 完整 JSON envelope 上限为 64 KiB，超限返回固定 soft error，不回显正文或 receipt。
- references 最多 64 条、合计最多 16 KiB；技能正文仍按 32 KiB 分页。
- 普通大工具输出的持久化、1 KiB preview 和读取方式完全不变。

| 修复检查项 | 命令 | 结果 |
|------------|------|------|
| 客户场景与对抗回归 | `go test ./internal/numind/biz/agent ./internal/numind/biz/feishu -run 'TestWrapToolWithV2ArtifactProcessing_...|TestDeclaredSkillReferences_...' -count=1` | PASS |
| 全仓测试 | `go test ./... -count=1` | PASS |
| Go lint | `PATH="$(go env GOPATH)/bin:$PATH" task lint` | PASS |
| Agent race | `go test -race ./internal/numind/biz/agent -count=1` | PASS |
| SkillReader race | `go test -race ./internal/numind/biz/feishu -run 'TestSkillReader|TestDeclaredSkillReferences' -count=1` | PASS |
| CLI timing tests isolated race | `go test -race ./internal/numind/biz/feishu -run 'TestControlledLarkCLIRunner_(AuthSessionStreamsURLBeforeBlockingCompletion|AuthSessionCancellationKillsProcessGroup|VerifyVersionAcceptsOnlyPinnedVersion)' -count=1` | PASS |
| 规格复审 | independent reviewer | PASS，P0/P1/P2=0 |
| 质量复审 | independent reviewer | PASS，P0/P1/P2=0 |

说明：完整 Feishu race 与全仓测试并行运行时，三个使用固定 3–5 秒墙钟超时的既有 CLI 测试超时；没有 race detector 报告。系统负载恢复后将这三个测试单独以 race 模式重跑，全部通过。受本修复影响的 SkillReader race 集也全部通过。

## 失败项修复要求
无。

## Agent-led Runtime S5 最终验收（2026-07-18）

本节取代上方历史迭代的 S5 结论，覆盖用户确认的 Option B：Agent 选择业务动作，平台统一负责当前用户隔离、固定命令目录、写前权限预检、授权恢复、确认、幂等、结构化结果与防重。

| 检查项 | 命令 | 结果 |
|--------|------|------|
| 核心包 | `go test ./internal/numind/biz/feishu ./internal/numind/biz/agent ./internal/numind/biz -count=1` | PASS |
| Go lint | `PATH="$(go env GOPATH)/bin:$PATH" task lint` | PASS |
| 全仓测试 | `go test ./... -count=1` | PASS |
| 关键竞态 | `go test -race ./internal/numind/biz/feishu ./internal/numind/biz/agent ./internal/numind/store -count=1` | PASS |
| Diff hygiene | `git diff --check` | PASS |
| 固定 CLI | `lark-cli --version` | PASS，`1.0.68` |
| 规格复审 | independent read-only reviewer | PASS，P0/P1/P2 = 0 |
| 安全与质量复审 | independent read-only reviewer | PASS，P0/P1/P2 = 0 |

审查先发现两项 P1，并按 RED-first 闭环：`feb8d87c` 复现“已启动写操作进入授权恢复重放”和“外部授权续跑后的 unknown 未继承防重状态”；`f091cba3` 修复后，任何已启动写操作失败均进入 `unknown_result` 且不可重放，持久化外部终态会在 continuation 首次模型调用前恢复同一 run 的停止/修正预算。`99991672` 的旧安全重放推断已删除。

最终结论：ALL_PASS。允许执行 `ndf-done`，合并并推送 `develop`，随后部署 Dev。若真实 Dev 账户仍缺少 `docx:document:write_only`，一次用户授权是外部同意步骤，不属于代码或部署阻塞。

## S6 Dev 部署证据（2026-07-18）

- `ndf-done`：PASS。功能分支合并到 `develop`，推送提交 `3d3707d5`，worktree 与本地功能分支已删除。
- TCR 镜像：`ccr.ccs.tencentyun.com/youshunumind/numind-server:develop-3d3707d5`。
- 镜像摘要：`sha256:d6cf649350165d4dbc1f579676bb061fb80f906363b710f2c4f407e90c9e915c`。
- Dev 容器：`numind-server-dev`，状态 `running`，健康状态 `healthy`。
- 公开健康检查：`{"code":0,"message":"","data":{"status":"ok"}}`。
- 容器内 CLI：`lark-cli version 1.0.68`。
- 最近 10 分钟关键启动错误匹配：`0`。
- Prod：未部署。

部署结论：PASS。代码、合并、推送和 Dev 部署已完成。真实飞书写入验收如触发 `docx:document:write_only` 缺失，只剩用户在授权卡片上的一次外部同意；平台会在同一操作上自动续跑且不会重复写入。
