# 上传附件单次解析与统一读取链路

日期：2026-07-20

## 背景

当前 Agent 上传附件后，后台 fallback worker 会解析并写入 `text_fallback`；运行开始时又可能把这段内容直接拼入模型输入，而模型后续调用 `file_read` 时还会重新下载并重新解析整份文件。现状通常不会把同一份正文重复塞入上下文两次，但会产生重复下载、重复解析、分页时反复解析，以及前端只传 URL 导致附件数据库身份丢失的问题。

## 用户结果

1. 用户上传文件后，平台后台只执行一次标准化解析，并把原始解析结果作为附件的规范缓存持久化。
2. Agent 初始上下文只收到附件 ID、文件名、类型和 URL 引用，不直接注入全文；需要正文时统一自主调用 `file_read`。
3. `file_read` 优先用当前用户的附件 ID 读取持久化缓存，后续分页不再下载或重新解析源文件。
4. 前端向非流式和流式 Agent 请求发送 `attachment_ids`；只有数据库持久化失败、ID 为零的极端兼容路径才发送 URL。
5. 历史客户端仍可传 URL，历史附件和 Agent 自己生成的 `agent-outputs` 仍可读取。
6. 所有附件读取继续执行当前用户归属校验；附件 ID 或 URL 均不能读取其他用户文件。

## 范围

- 为 `agent_attachment` 增加规范解析正文、摘要指纹、正文字节数、页数和解析时间字段，并回填可复用的历史成功结果。
- 将纯文本/Markdown 纳入后台解析 modality。
- fallback worker 在解析成功时同时写兼容 `text_fallback` 和规范缓存。
- `file_read` 新增 `attachment_id` 输入并优先分页读取数据库缓存；URL 命中附件记录时也复用缓存。
- 将 Agent 输入从“全文 fallback”改为“附件引用 + 必须调用 file_read”。
- 前端更新请求契约、流式发送、非流式发送和对应测试。

## 不在范围

- 不删除 `text_fallback`、旧 `attachment_urls` API 字段或现有多模态辅助字段。
- 不改变上传大小上限、COS 存储、OCR/VLM/ASR/DocumentParser 提供方。
- 不把用户文件正文写入日志、Langfuse 或前端状态。
- 不为生成型 `agent-outputs` 新建数据库记录；它们保留安全 URL 兼容读取。
- 不改生产环境。

## 验收标准

- AC1：新上传的 PDF、Office、文本、图片和音频在后台成功处理后均保存规范解析正文与稳定 SHA-256 指纹。
- AC2：同一附件连续读取多页时，`file_read` 的 parser 调用次数为零，内容可按 UTF-8 安全 offset 完整重组。
- AC3：`attachment_id` 和 URL 两种输入都强制当前用户归属；ID 优先且不需要 HEAD/COS 下载。
- AC4：解析尚未完成或已失败时，`file_read` 返回模型可见的可恢复软错误，不终止 Agent run。
- AC5：Agent 初始输入不包含 `parsed_content`/`text_fallback` 全文，而包含明确的 `file_read(attachment_id=...)` 指令。
- AC6：前端正常上传发送 ID；ID 缺失才发送 URL，附件 chip 展示不变。
- AC7：migration/rollback、后端全量测试与 lint、前端 lint/type-check/相关测试全部通过，后端先于前端部署到 Dev 且健康。
