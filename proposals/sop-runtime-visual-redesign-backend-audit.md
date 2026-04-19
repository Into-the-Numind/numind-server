# SOP 运行页后端能力摸底报告

> 对照 mockup 的 6 个状态（A/B/C/D/E/F），逐元素核对后端是否支持。
> 摸底由 Explore agent 执行，read-only 模式，结果在本文件归档。
> 摸底时间：2026-04-11

## 总体结论

后端 **95% 满足** mockup 需求。核心业务流程（执行 / SSE 流 / 权限配额 / draft 模式 / chat 上下文注入）全部完整。

只有 **5 个小 gap**，其中 2 个建议在本 feature 内补、3 个纯前端方案即可。

---

## Gap 总结表

| # | Mockup UI 元素 | 状态 | 后端支持 | Gap 建议处理 |
|---|---|---|---|---|
| 1 | 元信息：使用的模型 | E（完成态 footer） | ⚠️ 部分 | sop_node_run 表未冗余存 model_name，需要从 sop_node 表 join 拿。**建议**：在 biz/sop/sop.go 写 sop_node_run 时同步落 model_name 字段 (1 行改动 + 加列 migration)。或：从 sop_node.model_id 反查（无 schema 改动） |
| 2 | chat 元信息：耗时 | F（每条 AI 消息下方 14:42:11 · glm-4-7 · 3.2s） | ❌ 缺 | sop_chat_message 表无 duration_ms 字段。**建议**：加 migration + biz 写入时记录 |
| 3 | 流式中间 token 进度 | D（执行中） | ❌ 不支持 | mockup 没有"已生成 X tokens"显示，**直接放弃** |
| 4 | ~~"保存草稿" 按钮~~ | ~~C~~ | n/a | **已删除**：误判，draft run 机制已经隐式保存所有状态（lazy 创建 run + 节点输入持久化 + 上传文件持久化），按钮多余 |
| 4-bis | ⭐ 收藏（保存书签）| E、B | ✅ 完整 | 用户选项 2：output card head 加 ⭐ toggle。后端 endpoints: POST `/v1/sop/bookmarks` (save) / GET `/v1/sop/templates/:id/bookmarks` (list) / POST `/v1/sop/runs/:id/nodes/:node_id/apply-bookmark` (apply)。**前端 sopApi 缺 saveBookmark 封装**，需要补 |
| 4-ter | 自动应用书签 | C（draft 入口/创建 run） | ✅ 完整 | 用户选项 C：createRun 时传 `auto_apply_bookmarks=true`，后端自动应用所有匹配 (user, template, node) 的书签。无 UI 需求，纯参数 |
| 5 | "停止生成" 按钮 | D（执行中） | ❌ 后端无 abort | **建议**：纯前端 EventSource.close()，立即停止接收。后端流会继续跑完但前端忽略。无需后端改动 |

---

## 章节级摸底答案（核心引用）

### 1. 模板与节点数据 ✅ 完整

- **GET 节点列表**：`GET /v1/sop/templates/:id/nodes`，返回 `{template, nodes, total}`，nodes 中是 SopNodeDTO（不含 prompt/api_key/base_url/model_name 等敏感字段，spec 安全要求满足）
- `node.name` ✅ 存在
- `node.description` ⚠️ 存在但**老节点为 NULL**（templateId=1,2 的节点 description 全 null，因为 add_sop_node_description migration 未 backfill）。前端需要 graceful fallback：null 时不显示描述行
- `node.sort` ✅ 存在，决定顺序
- **模型字段**：每节点独立配置 model_id（B 端可选不同模型），通过 sop_node.model_id 关联

### 2. Run 与 NodeRun ✅ 大部分完整

- `sop_run.status` 合法值：`draft / running / done / failed`，draft 是其中之一 ✅
- `sop_node_run` 字段：id / run_id / node_id / user_input / output / status / created_at / updated_at / **latency_ms** / **prompt_tokens** / **completion_tokens** / **total_tokens**
- ✅ **耗时**：`latency_ms` 字段直接存
- ⚠️ **模型名**：不冗余存，需要从 sop_node.model_id 反查（见 Gap 1）
- ✅ **token 用量**：3 个字段都在 sop_node_run 表

### 3. 执行流程 API ✅ 完整

- **执行节点**：`POST /v1/sop/runs/:runId/nodes/:nodeId/execute`，返回 SSE 流
- **Draft 模式 lazy 创建 run**：`POST /v1/sop/runs/draft`，受 OptionalAuthMiddleware 保护
- **重新生成**：覆盖式 — 重新调 execute endpoint，后端会更新现有 sop_node_run.output（不会创建新行）
- **进入下一步**：纯前端切 nodeId，无显式 endpoint
- **回看历史步骤**：`GET /v1/sop/runs/:id/nodes/:nodeId` 拿单个节点的 run 数据

### 4. SSE 流细节 ✅ 完整

- 事件名：`thinking` / `message` / `done` / `error`
- `message` data：JSON-encoded string（不是纯文本，前端要 JSON.parse）
- `thinking` 部分模型支持
- `done` 事件可能发送两次（前端必须幂等）
- 心跳：`:\n\n`，前端需要忽略

### 5. Trailing chat (Step 4) ✅ 完整

- **endpoint**：`POST /v1/sop/chat/stream`，SSE 流
- **消息表**：`sop_chat_message`，字段 id / run_id / role / content / created_at / model_name / **prompt_tokens / completion_tokens / total_tokens**
- ⚠️ **缺 duration_ms 字段**（见 Gap 2）
- **上下文注入**：后端自动把前面 SOP 步骤的 output 作为 system message 塞入 chat history
- **不支持附件上传**（mockup 也没要求）
- **支持重新生成**：删掉最后一条 assistant 消息，重新调 chat/stream

### 6. 元信息字段汇总

| 字段 | sop_node_run 提供 | sop_chat_message 提供 |
|---|---|---|
| 耗时 (duration / latency) | ✅ latency_ms | ❌ 缺 |
| 模型名 | ⚠️ 需 join | ✅ model_name |
| prompt_tokens | ✅ | ✅ |
| completion_tokens | ✅ | ✅ |
| total_tokens | ✅ | ✅ |
| 完成时间 | ✅ updated_at | ✅ created_at |

### 7. 上传文件 ✅

- 上传 endpoint 已存在
- 文件关联到 sop_node_run.user_input

### 8. 历史 run 列表 ✅

- `GET /v1/sop/runs?template_id=X` 列出本模板的历史 run

### 9. 权限和配额 ✅

- 执行前 biz/sop/sop.go 检查 sop_run_count / 月配额
- 不足返回 HTTP 403 + business code（这是 numind 的少数走 HTTP 非 200 的路径之一，src/api/request.ts 已处理）

---

## 推荐 Gap 处理方案

| Gap | 推荐 | 理由 |
|---|---|---|
| 1. node_run 缺 model_name | **本 feature 内补**：sop_node_run 表加 model_name VARCHAR(64)，biz 写入时落，1 行 SQL migration + 几行 Go 改动 | 元信息是 mockup E 状态的 footer 关键元素，从 join 拿性能差且每次都要查 |
| 2. chat_message 缺 duration_ms | **本 feature 内补**：sop_chat_message 表加 duration_ms INT，biz 写入时落，1 行 SQL migration + 几行 Go 改动 | mockup F 状态每条 AI 消息显示耗时，缺这个字段就只能不显示 |
| 3. 中间 token 进度 | **放弃** | mockup 没设计这个 |
| 4. ~~保存草稿~~ | **删除按钮** | 误判 — draft run 机制已经覆盖。实际需求是"保存书签 (bookmark)"，见 4-bis |
| 4-bis. 保存书签 (⭐) | **前端补 saveBookmark API 封装 + UI** | 后端完整。前端 src/api/sop.ts 缺 saveBookmark 函数；ToolbarActions / 输出卡片缺 ⭐ toggle UI |
| 4-ter. 自动应用书签 | **createRun 时传 auto_apply_bookmarks=true** | 后端完整，前端只需在创建 run 时设参数 |
| 5. 停止生成 | **EventSource.close() 前端方案** | 后端不动，前端立即停止接收。极简实现 |

### 影响

- 如果接受 Gap 1+2 的"本 feature 内补"建议 → manifest.repos 需要从 `[numind-web-v3]` 扩展为 `[numind-web-v3, numind-server]`
- 后端改动量很小（2 个 migration + ~20 行 Go），不影响 feature 工期
