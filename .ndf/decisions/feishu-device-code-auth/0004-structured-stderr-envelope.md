# ADR 0004: 严格解析 lark-cli 非零退出时的结构化 stderr 错误

- 状态：Accepted for S4 repair
- 日期：2026-07-18
- 关联：`feishu-device-code-auth`、Dev Agent run 209

## 背景

Dev 上已经连接的用户执行 `docs +fetch` 时，三次 operation 都在约 100ms 内进入 `failed`，没有生成增量授权卡片。连接、Vault 和 operation 执行租约均正常。

对同一加密 HOME 做只读、脱敏重放后确认：固定版本 `lark-cli 1.0.68` 对缺少 `docx:document:readonly` 的调用以退出码 3 结束，并把一个完整 JSON 错误 envelope 写到 stderr，stdout 为空。该 envelope 的固定机器字段为 `identity=user`、`type=authorization`、`subtype=missing_scope` 和精确的 `missing_scopes`；此次真实形状不包含历史 Docs-create fixture 中的数值 `code`。

当前 `ControlledLarkCLIRunner` 只解码 stdout，并在非零退出时优先返回 process error。结果是 `ErrorClassifier` 看不到结构化权限证据，只能 fail closed 为 `feishu_operation_failed`。

## 决策

1. 仅当 stdout 为空、stderr 未截断、进程已启动且 stderr 是唯一完整 JSON object 时，允许把 stderr 解码为 `CLIEnvelope`。
2. stdout 非空时绝不回退到 stderr，避免两个数据源产生歧义；任何拼接 JSON、尾随字节、数组或不完整对象继续 fail closed。
3. stderr 原文仍不进入公开错误、日志、数据库 summary 或 LLM 上下文；分类只读取既有 `CLIError` 固定机器字段。
4. 在固定版本 tuple 表中增加本次真实观测到的 code-less `authorization/missing_scope` 契约。缺失 scope 仍必须完全属于当前 Command Catalog 的精确 scope 集，不能由错误文案推断或扩大授权范围。
5. 写操作仍保留现有 unknown-result 防线；本次真实 Docs fetch 是 read 风险，权限补齐后可以恢复原 operation。

## 拒绝的方案

- 根据 stderr 文案包含“missing scope”来触发授权：文案不稳定且会扩大安全边界。
- 所有非零 stderr 都尝试解析并覆盖 stdout：双通道歧义会掩盖 CLI 协议异常。
- 把 stderr 原文直接返回给 Agent：可能泄露资源细节、控制台 URL 或未来新增的敏感字段。

## 验收

- 客户 RED 必须复现“stdout 空、stderr 为真实 code-less missing-scope envelope、exit 3”并在旧实现失败。
- 修复后 runner 保留结构化 envelope，operation classifier 生成 `docx:document:readonly` 的 user-auth recovery。
- 非 JSON stderr、截断 stderr、stdout/stderr 双 envelope、未知 tuple 和非 Catalog scope 均继续 fail closed。
- Dev 真实 `docs +fetch` 首次触发增量授权卡，授权后自动读取成功；第二次读取不再授权。
