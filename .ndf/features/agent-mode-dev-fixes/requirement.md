# agent-mode-dev-fixes — 需求/设计/计划合并卡（Standard, bugfix 批次精简）

> 起因：User 在 dev 环境测试 agent mode，探测出 4 个面向终端用户的缺陷。
> 4 个根因均由并行 read-only investigator workflow 落实到 file:line + dev 日志 + DB 取证（confirmed）。
> Bug-from-Customer（User 上报）→ NDF §12 适用：每个 fix 第一个 commit 为失败复现测试（test(qa):/test(repro): 前缀）。

## Triage
- DB schema 变更：无
- 新 API 端点：无
- 新外部服务集成：无（复用现成 parser.DocumentParser / COS / aiservice）
- 影响文件：~20 文件跨 numind-server + numind-web-v3，且 #2/#4 共用热点文件（runner_stream.go / adapter_full_to_eino.go / agentChat.ts）
- 判定：**Standard（不可降级）** —— 文件数 >3 + 跨仓库 + #2/#4 文件交集需串行协调
- 执行方式：User 指令「全自动跑，不要问我，最佳实践修复，创建 workflows」→ 自主推进、串行实现（文件交集 Tier-4 不并行写）、并行 Tier-1 双 reviewer、止步 dev（不碰 prod）

## 四个缺陷与修复

### #3 多轮上下文丢失（先做，最高影响，相对独立）
- 根因：每轮一个独立 agent_run，messages 列只存本轮；runner 的 buildEinoMessages 永远只返回当前 Input 单条；执行链路从不按 session 加载历史轮（展示层 GetSessionSnapshot 正确聚合，执行层不聚合）。memory 子系统是 fact 抽取（且 dev 已 403 故障），不承担会话连续性。
- 修复：RunRequest 加 History []*schema.Message；buildEinoMessages 前置 History；在 biz 层 student_run_lifecycle 调 ListBySession 加载同 session 历史 run 的 user/assistant 文本对（v1 不带 tool_call/tool_result 避免 OAI 配对协议错误），注入 RunRequest.History；加最近 K 轮上限保护窗口；加载失败 fail-open。
- 文件：runner.go、student_run_lifecycle.go、(runner_runstream.go 自动受益)、runner_test.go

### #1 docx 上传/解析（相对独立）
- 根因（两层）：(L1) upload.go 白名单按 http.DetectContentType 嗅探，docx→application/zip 被拒 HTTP 500；扩展名兜底 switch 无 office 分支。(L2) 即便 PDF，file_read 把裸 URL 喂 qwen-long（TODO-T7 从未接），无法解析。项目现成的 parser.DocumentParser（容器已含 python3/antiword/markitdown）+ 百炼 fileID 链均未被 agent 接入。
- 修复（best practice：复用本地 parser.DocumentParser，零跨境零计费确定性高）：
  - upload.go：扩展名兜底校正 docx/doc/pptx/xlsx/rtf 的 MIME，放行白名单
  - file_read_parsers.go：新增 documentParserImpl 复用 parser.DocumentParser；tool_file_read.go MIME 分派增加 office 分支 + application/pdf 也改走 DocumentParser（修掉裸URL喂qwen-long坏路径）
  - fallback_service.go + multimodal.go：新增 document modality，上传后台 fallback 即本地抽正文注入 system prompt
  - 错误用现成 errno.ErrUnsupportedFileType
- 文件：upload.go、fallback_service.go、agent_attachment.go(注释)、multimodal.go、tool_file_read.go、file_read_parsers.go、factory_platform.go(wiring)

### #4 生成图片不可见（后端 emit + 前端渲染，与 #2 共用热点文件 → 在 #2 之前做）
- 根因：图真实生成+上传 COS 成功，但 ToolCallResultPayload.ArtifactURL 字段从无赋值（events.go 仅定义）；前端 agentChat.ts 'tool_call_result' 只读 preview 不读 artifact_url，AgentArtifactItem.vue 是死代码；且 image_gen 用 GenerateSignedDownloadURL（attachment 头破坏 inline 显示）；LLM 也未被引导贴 markdown 图。
- 修复（确定性下发 > 依赖 LLM 贴 URL）：
  - 后端 emit helper：在 adapter_full_to_eino.go emitStreamToolResult + runner_stream.go emit 点解析 fileCreateOutput JSON 填 ArtifactURL(+filename/mime)；events.go 补 ArtifactFilename/ArtifactMime
  - tool_create_helpers.go：image/* 产物改用 inline 签名（GenerateSignedURL）而非 attachment
  - 前端 agentChat.ts：读 artifact_url，image/* 时 push type:'artifact' 消息接通 AgentArtifactItem.vue；types 同步
- 文件：adapter_full_to_eino.go、runner_stream.go、stream/events.go、tool_create_helpers.go、(output_tools_priority_prompt.go 可选兜底) + web: agentChat.ts、types/agent-stream.ts、AgentMessageItem.vue

### #2 工程师错误文案 → 用户友好中文（全面扫清，最后做，吸收 #4 已改的 emit 点）
- 根因：SSE 把裸 err.Error() 与机器码 TerminalReason 透传；前端 applyError 当 markdown 直显，AgentMessageItem markdown 短路掉友好兜底；HTTP 402 显示 "HTTP 402"。现成中文 errno/ai.go 被绕过。
- 修复（统一翻译层 + 边界替换）：
  - 后端新增 user_error.go：UserFacingErrorMessage(err) + UserFacingTerminalMessage(reason)，复用 errno/ai.go Message + withhold.go 分类器；保留 raw 仅入日志
  - emit 点（runner_runstream.go emitStreamErrorEvents / runner_stream.go consumeEinoStream / adapter_full_to_eino.go emitStreamToolError）改用翻译层；TerminalPayload 增 user_message
  - 区分类别 a（给 LLM 的 tool result 英文保留）vs 类别 b（冒泡用户屏幕→翻译）
  - 前端 agentChat.ts/AgentMessageItem.vue/AgentToolCallItem.vue：优先用后端 user_message，兜底 reason→中文 map；agent-stream.ts 402→InsufficientCreditsDialog
- 文件：user_error.go(new)、runner_runstream.go、runner_stream.go、adapter_full_to_eino.go、errno/ai.go + web: agentChat.ts、AgentMessageItem.vue、AgentToolCallItem.vue、agent-stream.ts

## 实现顺序（串行，热点文件交集决定）
T1 #3 上下文 → T2 #1 docx → T3 #4 图片下发 → T4 #2 错误文案（吸收 #4 的 emit 改动）→ T5 前端联调（#2+#4 同改 agentChat.ts/AgentMessageItem.vue，一并完成）

## 验证策略（S5）
- 后端：每 task 失败复现测试（test(qa): 前缀）→ go test ./... 全绿 + task lint
- 前端：vitest（agentChat streaming spec）+ npm run lint + type-check
- dev 部署后冒烟：上传 docx、多轮对话、生成图、触发错误，确认用户侧表现修复
- 止步 dev（不碰 prod）

## 自主决策记录（technical，User 授权全自动）
- #1 解析走方案 A 本地 parser.DocumentParser（docx 纯 Go extractTextFromDOCX 优先稳定；xlsx/pptx 依赖容器 markitdown 已具备）；方案 B 百炼 fileID 列 follow-up
- #2 用户只见友好中文，raw 仅入日志（不做可折叠技术详情 v1）；402 复用 InsufficientCreditsDialog
- #4 图片以 artifact 气泡渲染；URL 24h 过期的历史会话重载、generateImage 收编 aiservice = follow-up（不扩 blast radius）
