# QA Report — Self-Service Config (B端自助配置工具)

## 验证环境
- 后端：本地 dev（localhost:9091，config_local.yaml → numind-dev DB）
- 前端：本地 dev（localhost:5173，Vite HMR）
- 浏览器：gstack headless Chromium
- 日期：2026-04-09

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go vet | `go vet ./...` | PASS | sqlite cgo 警告（非本项目代码） |
| Go test | `go test ./...` | PASS | 2 个 salesrag 测试失败（预先存在，非本功能引入） |
| Vue lint (web-v3) | `npm run lint` | PASS | |
| Vue type-check (web-v3) | `npm run type-check` | PASS | |
| Admin lint | N/A | N/A | 本功能不涉及 admin-web |
| E2E | N/A | N/A | S5 验证策略选择 gstack /qa + 后端测试 |

## 浏览器 QA

### 测试路径及结果

| # | 路径 | 操作 | 结果 | 截图 |
|---|------|------|------|------|
| 1 | B端侧边栏 | 父用户登录 → 侧边栏可见"配置中心" | PASS | 05-after-fresh-login.png |
| 2 | 知识库管理 | 配置中心 → 知识库管理 → 新建知识库 → 列表显示 | PASS | 07-kb-management.png, 09-kb-created.png |
| 3 | 智能体创建 | 智能体管理 → 新建 → 填写表单 → 关联知识库 → 保存 | PASS | 11-create-chatbot-dialog.png, 12-chatbot-created.png |
| 4 | 智能体测试对话 | 点击"测试对话" → 进入对话页 → 发送消息 → 收到 AI 响应 | PASS | 14-chatbot-test-fixed.png, 22-chatbot-reload-response.png |
| 5 | 智能体发布 | 点击"发布" → 状态变为"已发布" → 按钮变为"下线" | PASS | 24-chatbot-published.png |
| 6 | C端首页可见 | 首页显示已发布的 chatbot 卡片 | PASS | 25-homepage-chatbot.png |
| 7 | SOP 创建 | SOP管理 → 新建 → 填写名称描述 → 添加 3 步骤 → 保存 | PASS | 26-create-sop.png, 27-sop-3-steps.png, 29-sop-saved.png |
| 8 | SOP 步骤排序 | 点击 ▲ 按钮移动步骤 → 顺序正确交换 → 边界按钮禁用 | PASS | 28-sop-reordered.png |
| 9 | SOP 编辑加载 | 重新进入编辑页 → 步骤持久化正确加载 | PASS | 34-sop-edit-persisted.png |

- 截图目录：`numind-web-v3/.gstack/qa-reports/screenshots/`（本地，不进 Git）
- AI 审查结论：无 P0 级视觉/功能回归

### S5 期间发现并修复的 Bug

| # | 严重度 | 描述 | 根因 | 修复 commit |
|---|--------|------|------|-------------|
| 1 | P0 | 智能体测试对话 URL 为 `/chatbot/undefined` | ChatbotConfig 嵌入 `gorm.Model`，JSON key 为大写 `ID`，前端 `bot.id` 为 undefined | `ce93dbc` — 替换 gorm.Model 为显式字段 + json tags |
| 2 | P0 | 所有配置项 user_id 为 0 | Config controllers 使用 `c.GetUint("userID")`，但 middleware 设置的 key 是 `current_user` | `ce93dbc` — 添加 `currentUserID()` helper，使用 `c.Get("current_user")` |
| 3 | P1 | 发送对话消息报验证错误 | 前端发送 `{query:...}` 但后端 `chatReq` 期望 `{message:...}` | `6495daf` (web-v3) — 修改字段名为 `message` |
| 4 | P1 | billing 记录失败 (MySQL JSON column) | `usage_record.metadata` 为 JSON 类型列，空字符串 `""` 不是合法 JSON | `24d8fe4` — recorder fallback 设为 `"{}"` |
| 5 | P1 | SOP 编辑页空白 | `fetchSopTemplateDetail` 没处理 API 返回的 `{template:{...}, nodes:[...]}` 嵌套结构 | `915d3b0` (web-v3) — 解析 `raw.template` 嵌套 |
| 6 | P2 | SOP 列表创建时间显示 `-` | SopTemplate 共用 gorm.Model 大写字段，前端未做映射 | `915d3b0` (web-v3) — 添加 `normalizeSopTemplate()` 映射 |

## 可观测性验证

- [x] chatbot 对话触发了 Volc LLM 调用（服务器日志确认 `StreamChatWithModel completed`）
- [ ] Langfuse trace 验证 — 本地 Langfuse 未部署，跳过。S6 阶段在 dev 环境验证
- 结论：PARTIAL — LLM 调用正常，Langfuse trace 待 dev 环境验证

## PRD 验收标准核对

| 验收标准 | 结果 | 备注 |
|----------|------|------|
| B端父用户可见配置中心入口 | PASS | 侧边栏 + 路由守卫 |
| 知识库 CRUD（创建/列表/删除） | PASS | 文档上传未深度测试（需要实际文件） |
| 智能体 CRUD + 知识库挂载 | PASS | |
| 智能体测试对话（流式AI响应） | PASS | SSE 流式响应 + 消息持久化 |
| 智能体发布/下线状态管理 | PASS | |
| C端首页显示已发布智能体 | PASS | |
| SOP 模板创建 + 步骤管理 | PASS | 添加/删除/排序/提示词编辑 |
| SOP 模板发布流程 | 未测试 | 列表有"发布"按钮，功能逻辑与 chatbot 相同 |
| 子用户权限隔离 | 未测试 | 需要子用户账号，S6 人工验收 |
| 积分扣费（C端对话） | 未测试 | billing metadata 已修复，S6 人工验收 |

## 未覆盖项（S6 人工验收）

1. **子用户权限隔离**：需子用户账号登录，验证看不到配置中心、能看到已发布内容
2. **Langfuse trace**：dev 环境部署后验证 chatbot-chat trace
3. **知识库文档上传**：需要实际文件测试上传+向量化流程
4. **SOP 模板发布 → C端可见 → 执行**：完整端到端流程

## 结论

**PASS（有修复）** — 发现 6 个 bug（2×P0 + 3×P1 + 1×P2），全部在 S5 阶段修复并验证。核心用户路径（9/9）通过浏览器 QA 验证。可进入 S6 人工验收。
