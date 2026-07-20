# Agent 附件单次解析与 file_read 缓存设计

## 1. 现状与根因

上传服务创建 `agent_attachment` 后异步运行 fallback worker。Office 文档经 `DocumentParser`/MarkItDown 解析，PDF/图片/音频分别经既有提取器生成 `text_fallback`。生命周期层对非内联附件等待该字段并把完整包装文本拼入用户消息；`file_read` 则只接受 URL，每次调用都重新 HEAD、GET、解析整份源文件后再切 64 KiB 页面。

因此根因是解析结果没有成为可复用的数据资产，且前端丢弃了上传响应中的附件 ID。

## 2. 数据契约

在 `agent_attachment` 增加：

| 字段 | 类型 | 语义 |
|---|---|---|
| `parsed_content` | LONGTEXT NULL | 标准化 UTF-8 规范正文，不含系统包装 |
| `parsed_content_sha256` | CHAR(71) | `sha256:<64 hex>`，分页 continuation token |
| `parsed_content_byte_size` | BIGINT | 正文 UTF-8 字节数 |
| `parsed_page_count` | INT | 提取器可得页数；未知为 0 |
| `parsed_at` | DATETIME(3) NULL | 规范正文持久化时间 |

现有 `fallback_ready` 继续表示后台处理终态，`fallback_error` 表示终态失败。避免再引入一组可能漂移的 status 字段。成功终态必须原子写入规范字段、兼容 `text_fallback`、`fallback_ready=true` 与完成时间。

Migration 将成功的历史 `text_fallback` 回填到 `parsed_content`，计算 token 与字节数；这是兼容正文，不保证去除旧包装。纯文本/Markdown 历史记录从 `unknown` 更新为 `text`，启动恢复任务会解析仍未完成的记录。

## 3. 上传与解析状态机

```text
upload persisted
    │ fallback_ready=false
    ▼
worker claimed
    ├─ success → parsed_content + sha256 + size + parsed_at
    │             text_fallback + fallback_ready=true
    └─ terminal failure → fallback_error + text_fallback + fallback_ready=true
```

`ModalityText` 加入检测和 worker switch。Document/Text 使用现有 `DocumentParser`；PDF、图片、音频沿用现有提供方，并把其原始提取文本同时写入规范字段。图片规范正文使用已生成的 VLM/OCR组合描述，避免再次调用 OCR。

## 4. file_read 契约

输入增加可选 `attachment_id`：

```json
{
  "attachment_id": 123,
  "file_url": "https://.../agent-attachments/7/...",
  "offset": 0,
  "limit_bytes": 65536,
  "read_token": "sha256:..."
}
```

至少提供 ID 或 URL。ID 非零时为首选身份；服务执行 `GetByIDAndUser(id, currentUser)`，不能由 URL 覆盖归属。只有 URL 时先校验路径归属，再执行 `GetByURLAndUser(url, currentUser)`：命中则复用缓存，未命中才进入 `agent-outputs`/历史受控 URL 解析链路。

缓存分支不执行 HEAD、presign、GET 或 parser：

- pending：软错误 `file_processing`，提示模型稍后重试；
- failed：软错误 `parse_failed`；
- ready：直接按规范正文分页；
- ready 但历史缓存缺失：无错误的 `text_fallback` 可被规范化持久化一次，否则软失败。

输出维持现有分页字段与 64 KiB 上限。token 取持久化 SHA-256；若旧记录 token 缺失，可计算并回写。所有正文仍只出现在工具结果的 `content`。

## 5. Agent 输入契约

生命周期通过 ID 加载并验证附件，然后为每份附件输出固定引用：

```text
【附件引用】用户上传了文件，请先调用 file_read 读取后再回答：
- attachment_id: 123；filename: 客户资料.docx；mime_type: ...；file_url: ...
```

不等待 fallback，不拼入 `text_fallback`/`parsed_content`。这样上传只解析一次，同时正文只有在模型真实需要时才占用上下文。兼容 URL-only 附件继续使用旧引用提示。

请求同时出现 IDs 与 URL-only fallback 时，生命周期合并两组显示与引用，不再采用旧的二选一分支。

## 6. 前端契约

`CreateRunRequest` 同时声明 `attachment_ids?: number[]` 与兼容 `attachment_urls?: string[]`。构造请求时：

- `id > 0` → 放入 `attachment_ids`；
- `id <= 0` → 放入 `attachment_urls`；
- UI 本地消息仍从上传响应保存 `{url, filename}`，chip 行为不变。

流式 `AgentChatView` 与非流式 store 必须共用相同规则。后端先发布，因此旧前端 URL 与新前端 ID 在滚动期间都有效。

## 7. 安全与可观测性

- DB 查询全部包含当前用户，URL 路径校验保持 current-user equality。
- `file_read` span 只记录 MIME、offset、limit、returned bytes、cache hit/status 等标量，不记录 URL、文件名、正文或 prompt。
- 旧 URL 解析仍保留 COS host allowlist、method-bound presign、redirect refusal 和下载上限。
- parser 失败以固定分类返回模型，不反射原始内部错误。
- migration 不改 `config_prod.yaml`，rollback 只删除新增字段并把 `text` modality 恢复为 `unknown`。

## 8. 测试矩阵

1. migration 前后契约与 model/store CRUD。
2. worker 对 document/text/pdf/image/audio 保存规范字段和 token。
3. `file_read(attachment_id)` 缓存命中时 parser/HEAD 均为零次，两页可完整重组。
4. URL 命中 DB 缓存、ID/URL越权、pending、failed、stale token、UTF-8 边界。
5. lifecycle 不注入全文，IDs 与 URL fallback 同时保留。
6. 前端流式/非流式都发送 ID，零 ID 才发 URL。
7. 后端全量/race/lint 与前端 lint/type-check/unit。

## 9. 发布与回滚

后端 migration 与代码先上 Dev，验证字段、健康和无启动错误；随后前端上 Dev，检查实际 POST/SSE 请求含 `attachment_ids`。回滚前端不影响新后端；若回滚后端，必须先回滚前端到 URL-only，再执行 rollback migration。Prod 不在本次授权范围。
