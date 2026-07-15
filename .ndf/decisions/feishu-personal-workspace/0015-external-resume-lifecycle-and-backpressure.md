# ADR 0015: 外部恢复的生命周期与背压边界

- Date: 2026-07-14
- Stage: S4 Task 11
- Status: accepted

## Context

飞书授权或缺 scope 完成后，系统必须自动恢复原始 Agent tool call。该恢复既不能依赖仍然存在的 HTTP 请求，也不能在重复回调、删除会话、服务停机、数据库瞬时失败或恢复容量耗尽时重复写入飞书资源、永久卡住或遗留后台 goroutine。

## Decision

1. 外部结果以 operation ID、tool call ID 和 canonical result 为身份，原子回填为同一 `role=tool` turn；绝不追加伪造 user turn，也不重发原 lark-cli 业务操作。
2. HTTP 与 reclaimer 共用应用级 `ExternalContinuationSupervisor`，最多四个 continuation。HTTP 请求取消不取消已接受任务；应用生命周期关闭会取消并 join 全部受管 runner 与 narration。`AgentRunner.Run` 以协作式 context cancellation 为合同；不合作实现令 Stop 明确超时并保持可追踪，不能伪装为已停止。
3. 容量满或 supervisor 已开始停止时，已验证的外部结果仍先 tokenized Claim 后 Release 为 `external_resume_ready`。启动扫描/周期扫描在槽位释放后自动接管；不要求用户再次点击，也不突破四槽上限。
4. 首个模型调用的 gate 覆盖自动压缩、主模型与流式首边界。取消、删除、lease 丢失或失败均释放 lease；Complete 持久化失败使用独立的有界 Release context，并保留联合错误。
5. store lease capability 不再暴露未带 token 的 `ResumeExternalTool` 兼容入口，所有恢复强制经过 `AgentRunResumer`。

## Consequences

- 一期“授权完成后自动继续”在容量满、回调重试、进程重启和短暂数据库失败下保持成立。
- 恢复并发被明确限制为四个；超额任务保持 durable ready，而不是丢失或无限创建 goroutine。
- 后续 Task 12 的飞书 composition 必须复用该 supervisor/resumer，不得另建未受生命周期管理的恢复路径。
