# 飞书 Base 授权后终态修复提案

## 1. 问题重定义

这不是“授权超时”。授权已经成功；真正的问题是同一个 dispatcher 同时承担两种不兼容的职责：

- 成功 operation：向原 tool call 回填数据并继续 Agent；
- 非成功 terminal operation：应写入固定错误并终结 Agent，不应继续模型。

当前两者都调用 `Resume`，因此 Base 写入进入 `unknown` 后，Agent continuation 拒绝该结果，外层再把它包装为 500。

## 2. 方案选择

采用“一个 dispatcher、两条明确终态路径”：

- `succeeded` → `Resume`；
- `failed/unknown/cancelled` → `FinalizeExternalToolWait`；
- waiting → 不交接；
- 未知状态 → fail closed。

同时在 operation 执行边界增加安全的结构化 observation。日志只记录固定枚举、官方结构化错误 tuple、耗时和 opaque UUID，不记录命令参数或内容。

## 3. 拒绝方案

1. **把 unknown 当成功继续 Agent**：会让 Agent 基于不确定副作用继续写，可能重复创建。
2. **自动重试 Base 创建**：第一次可能已经创建部分 Base，重试会产生重复资源。
3. **仅把 HTTP 500 改成 200**：会掩盖 durable Agent wait 未终结，刷新后仍不一致。
4. **直接升级 lark-cli 1.0.72**：官方差异中没有 `+base-create` 修复，且升级会扩大 Catalog、技能和分类器验证范围。
5. **记录原始 stderr/stdout**：包含潜在资源、内容或凭据，违反安全边界。

## 4. PRD

### 用户故事 A：授权后业务失败

用户完成飞书授权后，即使原写操作失败或结果未知，页面也收到稳定业务结果，原 Agent 明确终结，不显示 Internal server error。

### 用户故事 B：授权后业务成功

成功 operation 仍精确恢复原 run/tool call，Agent 继续创建记录和回读，不产生第二次 CLI 写入。

### 用户故事 C：可排查性

工程侧能够从安全日志区分 transport、structured CLI error、malformed output、timeout 与 Agent handoff/finalize 结果，从而在下一次真实租户失败时直接定位，而不暴露用户数据。

## 5. 范围

仅 `numind-server`。不修改前端、数据库、公开 API、权限 scopes 或 lark-cli 版本；不部署生产。
