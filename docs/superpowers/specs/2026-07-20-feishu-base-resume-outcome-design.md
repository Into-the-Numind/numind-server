# 飞书 Base 授权后终态设计

## 1. 核心不变量

1. 每个 operation 最多执行一次 write-like CLI business invocation。
2. 只有 `succeeded` 可以进入 Agent model continuation。
3. `failed/unknown/cancelled` 必须使用 durable terminal finalizer；它追加固定工具错误、终结原 run，并以 operation/run/tool/user tuple 做幂等围栏。
4. waiting state 不进行任何 Agent 交接。
5. 交接失败仍为可重试基础设施错误；交接成功或幂等 no-op 为正常业务完成。

## 2. Dispatcher 契约

扩展 dispatcher 的 Agent 依赖，使其同时暴露：

- `Resume(ctx, ExternalToolResult) error`
- `FinalizeExternalToolWait(ctx, userID, runID, operationID, toolCallID, outcome) (bool, error)`

状态映射：

| Operation state | 动作 |
|---|---|
| waiting_* | return nil |
| succeeded | MarshalLarkToolResult → Resume |
| failed | Finalize(..., failed) |
| unknown | Finalize(..., unknown) |
| cancelled | Finalize(..., cancelled) |

所有 terminal state 先验证 operation ID、AgentRunID、ToolCallID。finalizer 返回 `false,nil` 表示 durable 幂等 no-op，dispatcher 仍返回 nil。

## 3. Lifecycle 兼容

`WorkspaceLifecycleService` 在 dispatcher 返回后已有 terminal re-read/finalize。保留该二次调用作为跨实例补偿；durable finalizer 必须幂等，因此不会重复写 tool result 或重复取消 run。

DeviceAuthFlow 的后台/HTTP completion 都通过同一 dispatcher，避免只有浏览器路径能终结 unknown。

## 4. 安全可观测性

新增 `OperationObservation` 严格 DTO，只允许：

- user_id、generation、operation_id
- phase：`invoke` / `handoff`
- outcome_class：固定枚举
- risk、CLI version、duration
- CLI error type/subtype/code：仅当来自完整结构化 envelope，且每段通过长度/字符 allowlist；不得包含 message/details/hint/permission evidence。

production sink 再次校验 UUID、枚举、风险、CLI 版本和 tuple 字符集后才写日志。禁止 argv、stdin、scopes、HOME、token、URL、stdout、stderr、文档/Base 内容。

write-like invocation 即使结构化错误可分类，也仍提交 `unknown` 且不重放；observation 只用于排查，不能改变安全决策。

## 5. 失败语义

- operation infrastructure/lease/storage 失败：dispatcher 返回 error，HTTP 保持可重试错误。
- terminal finalizer 失败：dispatcher 返回 error，后续相同 resume 可幂等重试。
- terminal finalizer 成功或已处理：dispatcher 返回 nil，lifecycle 返回 stored operation summary。
- Base write unknown：前端获得业务终态；用户应先检查飞书是否已产生同名 Base，再决定后续操作。

## 6. 测试

- 客户 RED：unknown 经 `Resume` 失败导致 dispatcher error。
- GREEN：三类非成功 terminal 均只调用 finalizer，unknown 不调用 Resume。
- 成功回归：succeeded 仍只调用 Resume。
- 幂等：finalizer false,nil 仍成功；finalizer error 保持可重试。
- observation：结构化 tuple 可见，原始 message/argv/stdin 不可进入 DTO 或日志；非法字段被 sink 丢弃。
- lifecycle：授权 completion → operation unknown → terminal wait finalized → API service 返回 stored unknown 而非 unavailable。
