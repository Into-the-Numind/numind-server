# S4 Task 4：可信 `file_read` 原子交付

- 日期：2026-07-20
- RED：64 KiB `file_read` 页面仍被通用 16 KiB artifact 流程替换为预览；`boundedAtomicFileReadTool` 与 384 KiB envelope 上限尚不存在。
- GREEN：wrapper 只识别 `fullToolEinoAdapter` 内的具体 `*fileReadTool`，在完整 JSON envelope 不超过 384 KiB 时直接交给模型，不写 artifact。
- 防伪与限流：同名 mock/外部 `file_read` 仍进入通用持久化；超过 envelope 上限时丢弃原 payload 并返回结构化可恢复软错误。现有 `lark_skill_read` 64 KiB 原子路径与其他工具行为不变。
- Gate：focused、focused race、`compactv2` + Agent 整包、`task lint` 与 diff hygiene 通过。
