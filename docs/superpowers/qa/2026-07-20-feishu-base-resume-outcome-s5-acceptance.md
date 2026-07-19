# QA Report — 飞书 Base 授权后终态恢复

## 验证环境

- 后端：Standard feature worktree；Go 单元、集成与 race 测试使用受控 fake `lark-cli`
- 外部协议：保持官方 `lark-cli` 1.0.68、原有声明权限与 Base 命令格式；本次不升级 CLI
- 数据与接口：无数据库 schema、API 端点、Prod 配置或前端改动

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go lint | `PATH="$PATH:$(go env GOPATH)/bin" task lint` | PASS | `go vet` 与 `golangci-lint` 均通过 |
| 客户回归 | `go test ./internal/numind/biz ./internal/numind/biz/feishu -run '<focused terminal outcome tests>' -count=1` | PASS | 覆盖授权成功、Base 写入 unknown、持久化终态、再次确认幂等返回 |
| Feishu focused race | `go test -race ./internal/numind/biz/feishu ./internal/numind/biz -run '<focused terminal outcome tests>' -count=1` | PASS | 无 data race |
| Go 全仓串行测试 | `go test -p 1 ./... -count=1` | BASELINE EXCEPTION | 飞书及其余包通过；仅两个既有小红书 analytics 测试失败，且在干净 develop 上独立运行同样失败 |
| Diff hygiene | `git diff --check` | PASS | 无空白或补丁格式问题 |
| 双独立审查 | 规格审查 + 代码质量/安全审查 | PASS | P0/P1/P2 = 0 |

## 客户 RED 与回归保护

- 第一个 feature commit `60cd6169` 先复现客户问题：授权成功后，Base 写操作进入 `unknown`，dispatcher 仍错误调用 Agent continuation，continuation 失败后最终返回 HTTP 500。
- GREEN 将结果分流：只有 `succeeded` 才继续原 Agent；`failed`、`unknown`、`cancelled` 进入 durable terminal finalizer。
- Base 写操作只要已经开始就不会自动重放；跨实例、响应丢失和重复点击继续均由 durable operation/result 保证至多一次收尾。
- 授权完成后的第二次 lifecycle finalization 是幂等 no-op，接口返回已保存的终态结果，不再把业务终态包装成 Internal server error。

## 安全与可诊断性

- 严格保留当前用户、连接 generation、授权 session、operation、飞书应用和原 Agent run/tool call 的绑定校验。
- 新日志只输出固定 phase/outcome/risk、UUID、CLI 版本、退出码、耗时及经过 allowlist 验证的错误分类。
- 不记录 argv、stdin、scope 明文、HOME、token、设备码、应用密钥、URL、stdout/stderr 或文档/Base 内容。
- 诊断日志不参与业务决策，不扩大命令、权限或重试边界。

## 全仓基线例外

全仓串行测试仅失败：

- `TestGetAnalyticsSummaryAggregatesMVPFunnel`
- `TestGetAnalyticsSummaryCountsCanonicalMVPEventNames`

两项测试位于 `xhsscript` analytics，与本次飞书代码没有文件或调用链重叠；在 feature worktree 与干净 `develop` 上以相同命令运行均得到相同失败。该既有基线问题不在本次客户修复范围内，不阻塞飞书修复进入 Dev。

## Dev 验收提示词

使用一个新的唯一名称，避免此前结果不确定的写入已经在飞书侧部分完成：

`请创建一个飞书多维表格，名称为「有数 Base 联调测试-0720-终态修复」。创建一个名为「任务列表」的数据表，包含字段「任务名称」（文本）和「状态」（单选：待处理、已完成）。新增一条记录：任务名称为「验证有数 Agent」，状态为「待处理」。完成后重新读取这条记录，并告诉我多维表格链接。不要创建飞书文档或知识库。`

预期：需要授权时，在 10 分钟内完成授权并点击“我已完成，继续”。页面不再出现 Internal server error。若飞书 CLI 返回明确失败或写入结果未知，Agent 应收到真实终态并停止盲目重试；服务端安全日志可定位失败来源。

## 结论

飞书范围 `PASS`，两个独立审查均通过。记录上述 develop 可复现的非飞书基线例外后，允许进入 S6 原子合并、推送与 Dev 部署；Prod 不在本次范围内。
