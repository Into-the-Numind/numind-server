# S4 Task 6：隐私安全可观测性

- 日期：2026-07-20
- RED：final metrics parser/recorder 不存在；既有 `file_read` 与 document/OCR/direct span 会写入完整输入或 presigned URL；XHS 没有专项 span。
- 工具 span：`xhs_note_list` 只记录 projection、filter kinds、limit、returned_count、has_more、duration/error class；`file_read` 只记录 MIME、offset、limit、returned bytes、has_more、duration/error class。三个 parser 子 span 只记录 parser kind 和返回字节数。
- 错误隐私：span 的 ERROR status message 只使用固定分类，不写 provider/DB 原始错误、URL query、正文、prompt、cursor 或 read token。无 trace/Langfuse disabled 时工具语义不变。
- 最终统计：只接受唯一、严格的 `numind-pipeline-report/v1` marker，校验 Agent/schema、未知字段、必需字段、非负整数与每 Agent 的 output mode 枚举。任何缺失、重复或畸形输入都归一为 `status=unavailable`，不记录 raw final/marker。
- Runner：non-stream 与 stream 的成功终态共用 `finalizeRun` recorder；结构化日志和 `UpdateTraceMetadata` 使用同一 safe map，observability lookup/disabled 不影响业务成功。
- Gate：三个专项测试命令、focused race、Agent 整包、`task lint` 与 diff hygiene 通过。
