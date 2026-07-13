# ADR 0012: External Action 持久等待契约

- Date: 2026-07-13
- Stage: S4 Task 9
- Status: accepted

## Context

飞书授权不是普通问答。Agent 必须在不重新生成工具参数的前提下暂停原 tool call、展示实时授权链接，并在授权完成后由 operation resume 精确继续。verification URL、device code 和凭据均为短期敏感数据，不能进入 `agent_run`、日志或可恢复快照。

## Decision

1. 新增独立 `external_action` SSE 与 snapshot message，复用既有 `waiting_for_user_choice` terminal，但不新增或修改固定 `TerminalReason` / `LoopEvent`。
2. live payload 可携带 URL；`Persistent()` 返回 `internal/pkg/externalaction.Payload`，该 durable 类型只包含 provider、operation、auth session、tool call、phase 与 expiry，类型层面没有 URL。
3. `externalaction.Parse` 是读写共用的唯一安全边界：逐 token 要求 6 个精确小写 key 各一次，拒绝 duplicate、case variant、unknown、URL/device code/secret、缺失/空/null/非法值、非对象与 trailing JSON。store 只持久化解析后重新 marshal 的 canonical JSON。
4. Runner 通过独立 `IExternalActionWriter` 能力写入；该接口不加入 `IAgentRunStore`。写入成功前不得发卡；writer 缺失或持久化失败时 fail closed，SSE、RunResult 与 DB 均为 `model_error`。
5. external 与普通 question 写入时原子清理对方 stale 字段。快照检测到任何 present external JSON 后只走 external 分支；解析失败则不显示等待卡，不回退 stale question。Answer API 在普通问题解析前明确拒绝 external wait。
6. 重载快照只返回无 URL 卡；重新生成链接由 Task 13 refresh 端点承担。

## Verification

- 首轮规格审查发现 corrupt external + stale question 会回退普通 answer，修复后复审 PASS。
- 质量审查发现 raw JSON duplicate/case key 与读写 parser 漂移，提取 neutral parser、canonical store 后复审；同时补齐 16 个 SSE event 的穷举测试。
- 最终规格/质量审查 P0/P1/P2 均为 0。
- external-action 定向测试、3 轮 race、完整三包、全仓 `go test ./...`、`task lint` 与 `git diff --check` 全部 PASS。
