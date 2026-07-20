# S4 Task 3：可续读且 UTF-8 安全的 `file_read`

- 日期：2026-07-20
- RED：focused tests 因 `file_read` 尚无 `offset`、`limit_bytes`、`read_token` 输入以及续读元数据而失败。
- GREEN：文本、文档与 OCR 解析器不再在 200 KiB 静默截断；文本和文档完整读取至 20 MiB，超限明确失败。工具将解析文本规范化为有效 UTF-8，以完整内容 SHA-256 作为 `read_token`，按默认/最大 64 KiB 返回 rune-boundary 安全页面。
- 一致性：后续页必须携带上一页令牌；源内容变化、越界 offset、非 rune 边界 offset 或无法容纳下一个 rune 的过小页面均返回可叙述软错误，避免混读、乱码和死循环。
- 兼容：保留 URL 归属校验、COS HEAD/GET 分离签名与 MIME 路由；`truncated` 与 `has_more` 对齐供旧调用方渐进迁移。
- Gate：focused、focused race、Agent 整包、`task lint` 与 diff hygiene 通过。
