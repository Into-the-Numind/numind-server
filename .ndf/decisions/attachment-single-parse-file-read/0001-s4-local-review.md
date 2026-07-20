# S4 本地双轮审查

日期：2026-07-21
范围：`attachment-single-parse-file-read` 后端与前端全部 feature diff

## 第一轮：数据、安全与发布兼容

结论：PASS，P0=0，修复 P1=1。

- 当前用户隔离：ID 路径只调用 `GetByIDAndUser`，URL 路径先校验 URL user id，再调用 `GetByURLAndUser`；任一失败只返回固定软错误，未泄露存在性或内部错误。
- 网络边界：缓存命中在 presign/HEAD/GET/parser 之前返回；旧 `agent-outputs` 与无记录 URL 仍经过 COS host allowlist、method-bound 签名、禁止 redirect 和大小限制。
- 正文隐私：model/store JSON 均隐藏 `parsed_content`；file_read/worker 新增 span 只记录 attachment id、MIME、offset、limit、字节数和状态，不记录正文/token/prompt。
- migration：新增 LONGTEXT 规范正文与稳定 token，历史成功 fallback 回填；纯文本历史行进入恢复任务；rollback 与新增字段成对。
- rolling：后端同时接受 IDs/URLs，重复 URL 去重；前端只对安全正整数 ID 走 ID，ID=0/缺失保留 URL。
- P1 修复：旧 `text_fallback` 是 MySQL TEXT。大文档若把完整包装文本与 LONGTEXT 规范正文原子写入，会因 64 KiB 上限导致整个成功更新失败。现将兼容字段 UTF-8 安全限制在 60 KiB，完整正文只保存在 LONGTEXT，并增加 75 KiB 回归测试。

## 第二轮：行为、原子性与质量

结论：PASS，P0=0，修复 P1=1。

- 单次解析：document/text/pdf/image/audio 成功路径在 worker 中原子写入正文、SHA-256、字节数、兼容 fallback 与 ready 状态；file_read 缓存测试证明多页重组时 parser/HEAD 为零次。
- 流式一致性：Create 与 RunStream 共用 `composeAttachmentInput`，不会出现一条链发 ID、另一条链仍注入 URL/全文。
- 上下文：ready 的 `TextFallback` 与 `ParsedContent` 都不会进入初始用户消息；固定引用明确要求 `file_read(attachment_id)`。
- 分页：保留 64 KiB、UTF-8 边界、offset/read-token/stale-token 契约，ID 与 URL cache hit 共用同一分页函数。
- 前端：流式和非流式共用 `buildAttachmentRequestFields`；附件 chip 仍从本地上传响应生成，无视觉变化。
- P1 修复：上传后立即启动 Agent 时，worker 可能尚未置 ready。生产 file_read 现在只轮询数据库最多 1.5 秒以吸收常规竞态，期间绝不解析；超时仍返回可恢复 `file_processing`。增加“等待后命中缓存且 parser=0”测试。

## 已验证

- 后端完整 `go test ./...` 通过。
- file_read、fallback、lifecycle、factory、schema 定向测试通过。
- 前端两组相关 Vitest：44 passed，3 todo（既有 Playwright 所有权）通过。
- 两仓库 `git diff --check` 通过。

S4 无已知 P0/P1，进入 S5 完整质量门禁。
