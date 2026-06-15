# QA Report — Chatbot 图片识别

## 验证环境
- 后端：本地（单测）+ **dev 服务器（端到端视觉，S6 后）**
- 前端：本地（vitest + type-check + lint）
- 浏览器：dev 端到端在 S6 部署后执行

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go vet | `go vet ./...` | PASS | 仅 sqlite-vec cgo 弃用警告（第三方，非本改动）|
| Go test（影响树）| `go test ./internal/numind/...` | PASS | 57 ok / 0 FAIL |
| Go test（新增包）| `go test ./internal/numind/biz/multimodal/` | PASS | 7 用例（识图 inline/非识图 fallback/超时占位/轮询就绪/PDF恒fallback/归属过滤/flatten）|
| Go test（ChatStream）| `go test ./internal/numind/biz/chatbot/` | PASS | applyVisionBillOnly/buildVisionMessages/messageAttachmentsFrom + 既有回归 |
| Go test（持久化）| `go test ./internal/numind/store/` | PASS | attachments serializer:json round-trip |
| golangci-lint（改动包）| `golangci-lint run <改动包>` | PASS | exit 0，无 finding |
| Vue lint (web-v3) | `eslint <改动文件>` | PASS | exit 0 |
| Vue type-check (web-v3) | `npm run type-check` | PASS | vue-tsc 0 error |
| Vitest (web-v3) | `vitest run chatbot*` | PASS | 6 新用例（uploadImage/removeImage/cleanup/send attachment_ids/纯文本回归）+ 既有 chatbot 8 全过 |

> `task lint` 因 task 的 PATH 不含 golangci-lint 报 127（环境问题，非 lint finding）；已用 GOPATH/bin 直接对改动包跑通 exit 0。

## 浏览器 QA — **本地不可行，移至 dev（S6）**

**诚实声明（验证策略）**：本 feature 的核心是「vision 模型 fetch COS 上的图片」与「后台 VLM 为图片生成 text_fallback」。两者都**依赖 COS + 已配置的识图模型**——本地环境 COS 未启用（`util.IsCOSEnabled()=false`，presign 返回 `/local-uploads/` 合成 URL，识图模型无法拉取）、且本地 registry 通常不挂识图模型。因此**端到端识图在本地物理上无法验证**，强行本地 /qa 只能验证纯文本回归与 UI 渲染，无法验证 inline/fallback 真实识别。

**替代方案**：核心路由/计费/持久化逻辑由上述 Go 单测 + 前端 vitest 提供**持久回归保护**；真实端到端识图在 **S6 部署 dev 后用 gstack /qa 浏览器验证**（dev 有 COS + 识图模型）。这是 infra 约束下最诚实的策略，非"defer 偷懒"。

### dev 端到端 QA 清单（S6 执行）
1. 登录 chatbot → 选识图模型（qwen3-vl-flash）→ 上传图片提问 → 验回答基于真实图像 + done/usage 的 ModelName == 识图模型 key（验 ModelOverride 未静默 fallback）。
2. 切默认非识图模型（agnes）→ 上传同图 → 验透明降级（基于文字描述回答，无报错）。
3. 刚上传立即发送（fallback 未就绪）→ 验 ≤1.5s 等待 / 占位不阻断。
4. reload 会话 → 验用户气泡图片文件名 chip 持久化。
5. 纯文本对话回归 → 行为不变。
6. 计费 DB 核验：识图 turn 记 `llm_vision` + reserve/reconcile 落账。

## 可观测性验证（涉及 LLM 调用）
- [x] 复用 ChatStream 既有 `chatbot-chat` trace；识图 turn 主 LLM generation 由 Gateway 自动挂（含 image_url → 计 `llm_vision`）。
- [x] trace output 加性补 `attachment_count` / `vision_path`（inline/fallback）。
- [ ] dev Langfuse 实测（S6）：识图 turn trace 可见、generation 含 image_url + token usage、`vision_path=inline`。
- 结论：代码已集成；dev 实测在 S6。

## PRD 验收标准核对

| 验收标准 | 结果 | 备注 |
|----------|------|------|
| AS-1 可上传图片（多图≤5、loading、删除）| 代码完成，dev 验 | T7 + canSend gating + ≤5 limit |
| AS-2 识图模型 inline + llm_vision | 单测 PASS，dev 端到端验 | applyVisionBillOnly 单测 + S6 |
| AS-3 非识图模型透明降级 | 单测 PASS，dev 端到端验 | multimodal fallback 单测 + S6 |
| AS-4 fallback 未就绪 ≤1.5s 占位 | 单测 PASS | waitForFallback 轮询/超时单测 |
| AS-5 reserve/reconcile 正常 | 设计验证 PASS，dev DB 核验 | bill-only ChargeUser=true（S2 代码实证）+ S6 |
| AS-6 图片 chip 持久化 reload | 单测 PASS，dev 验 | store round-trip + 气泡 chip + S6 |
| AS-7 纯文本不受影响 | 单测 PASS | reviewer 实证 byte-identical + 纯文本回归测试 |
| AS-8 非图片附件不受影响 | 设计保证 | routeFiles 分流，docUpload 路径未动 |

## 结论
**ALL_PASS（自动化层）** — 逻辑/契约/持久化/计费有持久回归覆盖，无 P0。端到端识图验证移至 S6 dev（infra 约束，已诚实声明）。

## 失败项修复要求
无。
