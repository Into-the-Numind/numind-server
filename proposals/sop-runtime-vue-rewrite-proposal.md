# SOP 运行页 Vue 3 完整重写 — 提案

## §1 方案概述 [客户可见]

### 要解决的问题
莫小派的 SOP 工作流引擎目前存在一个核心信任问题：**B 端客户通过 SOP 配置器配置出来的步骤名称、描述、数量，在 C 端用户实际运行时根本不会显示**。无论 B 端怎么修改配置，C 端运行页永远显示一套早期硬编码的步骤标题、描述和样式。这让 self-service-config 这套自助配置功能形同虚设，B 端客户对平台的可信度受到严重威胁。

### 历史成因
最早的两个 SOP 模板（"流量选题口播稿"和"朋友圈创作"）是在 SOP 配置器还不存在的时候用纯前端 JavaScript 硬编码实现的（一个 7518 行的 vanilla JS 文件 `sop-legacy.js`，配套 `SOPView.vue` 1019 行的 Vue 包装层）。后来上线了 self-service-config 配置器，让 B 端可以自助配置任意 SOP，但 C 端运行页**没有同步升级** —— 它仍然在用前端硬编码的 `TEMPLATE_CONFIGS` 和 `STEP_NAME_MAP` 常量去覆盖后端返回的真实数据。这导致两套数据源并存，且前端永远赢。

### 本次改造的核心原则
**数据库 = 唯一真相源（Single Source of Truth）**。前端不再硬编码任何 SOP 业务数据，所有步骤名称、描述、数量、提示词都从后端 API 动态读取。

### 解决方案
将 7518 行 legacy vanilla JavaScript（`sop-legacy.js`）+ 1019 行的 Vue 包装层（`SOPView.vue`，当前是个 hydration shell，通过 `legacyReady` ref 加载 legacy 后才显示真实内容）完整重写为 Vue 3 + Composition API + TypeScript 组件体系，全部数据由后端 API 驱动，删除一切硬编码模板与硬编码 UI。重写总规模约 8537 行 legacy 代码 → 预计 15-25 个 Vue 组件 + composables。

### 预期效果
1. B 端配置器配置什么，C 端运行页就显示什么 —— 配置契约真正被兑现
2. 退役 7518 行无类型、无组件化、无测试的 legacy JS，建立可维护、可演进的 Vue 基线
3. 顺手修复一个隐藏的 P0 安全 + IP 漏洞（后端 API 当前正在向所有登录用户暴露 LLM `api_key`、`base_url`、`model_name`，**以及 B 端的核心 IP `prompt`** —— 即驱动整个 SOP 价值的系统提示词模板，这是比 api_key 更敏感的商业资产）

### 不在本次范围内
- SOP 配置器（self-service-config 已完成，本次零修改）
- 视觉重设计（保持现有视觉风格 + DESIGN.md token，不做品牌升级）
- 新增 SOP 运行功能（仅做"等价重写 + 删除硬编码 + 修复安全漏洞"）

## §2 报价与周期 [客户可见]

- 预估工作量：基于 self-service-config 量级类比，约 15-20 个独立 task
- 交付时间线：S2 spec 完成后给出精确估算
- 内部项目，无外部报价

## §3 技术可行性 [AI 内部]

### 现有功能复用

#### 后端（高复用，~95%）
后端 SOP 运行时 API 几乎完整 —— 配额检查、Langfuse trace、计费扣减、流式执行、节点状态机、聊天对话、书签、历史记录、Draft 生命周期全部已经在 `internal/numind/biz/sop/` 实现。本次重写**仅需要 3 处后端微调**：

1. **创建 `SopNodeDTO`**（隐藏 `api_key` / `base_url` / `model_name` / `timeout_seconds` 等基础设施字段，**以及 `prompt` 这一 B 端核心 IP 字段**，修复 P0 安全 + IP 漏洞）
2. **完善 `GetTemplateNodes` 返回的 template 元信息**（补 description、status 等字段，让前端能正确渲染模板标题/描述）
3. **在 `CreateNode` / `UpdateNode` 加 LLM 字段白名单守卫**（拒绝 B 端写入 base_url/model_name/api_key，与决策 3 对齐）

**实测验证后的完整后端 SOP 端点清单（共 ~20 个，本次重写依赖）：**

| 类别 | 端点 | router.go 行 | 备注 |
|---|---|---|---|
| 模板查询 | `GET /v1/sop/templates` | 150 | 列表 |
| 模板查询 | `GET /v1/sop/templates/:id/nodes` | 151 | **P0 安全修复点** |
| 模板查询 | `GET /v1/sop/templates/:id/check-permission` | 152 | 入口前置 |
| 模板查询 | `GET /v1/sop/templates/:id/bookmarks` | 153 | 书签列表 |
| 模板查询 | `GET /v1/sop/templates/executed` | 154 | 已执行历史 |
| 模板查询 | `GET /v1/sop/templates/:id/runs` | 155 | 单模板的 run 列表 |
| Run 生命周期 | `POST /v1/sop/runs` | 163 | 创建（含配额） |
| Run 生命周期 | `GET /v1/sop/runs/:id/next-node` | 164 | 下一个待执行 |
| Run 生命周期 | `POST /v1/sop/runs/:id/nodes/:node_id/execute` | 165 | **流式执行入口** |
| Run 生命周期 | `POST /v1/sop/runs/:id/nodes/:node_id/apply-bookmark` | 166 | 应用书签 |
| Draft 生命周期 | `DELETE /v1/sop/runs/:id/draft` | 167 | 主动清草稿 |
| Draft 生命周期 | `POST /v1/sop/runs/:id/draft` | 168 | Beacon API（关闭浏览器时） |
| 文本编辑 | `POST /v1/sop/text/edit` | 173 | 文本流式编辑 |
| 聊天 | `POST /v1/sop/chat/stream` | 174 | trailing chat 流式 |
| 聊天 | `GET /v1/sop/runs/:id/chat-messages` | 175 | 历史消息 |
| Run 查询 | `GET /v1/sop/runs/:id/status` | 176 | 状态轮询 |
| Run 删除 | `DELETE /v1/sop/runs/:id` | 179 | 物理删除 |
| Run 删除 | `POST /v1/sop/runs/batch/delete` | 180 | 批量 |
| Run 查询 | `GET /v1/sop/runs/:id/detail` | 181 | 完整详情 |
| Run 查询 | `GET /v1/sop/runs` | 182 | 我的 runs |

**没有任何端点需要新增**。本次后端改动仅限于：(a) DTO 字段隐藏、(b) GetTemplateNodes 返回 template 元信息、(c) CreateNode/UpdateNode 字段白名单。

#### 前端（基础设施可复用）
- `src/api/request.ts`（axios + token 拦截器）—— legacy 直接 fetch，重写后统一走这里
- Pinia store 模式（参考 `useSOPStore` 命名）
- DESIGN.md token + variables.css（视觉风格不变）
- 现有 Vue 组件库（`ConfirmModal`、`InsufficientCreditsDialog`、`AppNotification` 等）
- Markdown 渲染：marked.js / highlight.js / DOMPurify（legacy 已 CDN 加载，重写时改为 npm 依赖）

### 技术风险

#### 风险 1：legacy JS 的"沉默功能"丢失（高）
7518 行 legacy JS 包含 60+ 全局函数、30+ 状态变量、15+ API 端点、复杂的 SSE 流处理、scrollFollowManager 状态机、Draft 模式生命周期、文件上传并发处理。Vue 重写极易遗漏。

**缓解：**
- S1 已经产出穷举式 feature inventory（Subagent 1 的报告作为 S2 spec 的输入）
- S2 spec 必须将 inventory 转化为完整的"必须保留功能清单"
- S5 验证策略锁定为 **Playwright E2E**（而非 gstack /qa 一次性验证），覆盖关键用户路径，未来回归保护持久化

#### 风险 2：早期模板兼容性（中）
templateId=1, 2 是 admin 早期手写代码搭建的，DB 中存在但 `sop_node.description` 字段全为 NULL（migration 加字段后未 backfill）。

**缓解：**
- 前端 UI 必须**优雅退化**：description 为空时不显示描述行，而不是显示 "undefined" 或空白占位
- 不在本次做 SQL backfill。creator (user 25) 后续可通过 self-service-config 编辑器自助补齐
- decisions: 与决策人确认"一切以数据库为准，前端零硬编码"

#### 风险 3：流式 SSE + 思维链的精确移植（中）
legacy 的 `handleStreamingResponse()` 解析 `event: thinking` / `event: message` / `event: done` 三种 SSE 事件，且需要边收边渲染 Markdown，并管理思维链容器的动态创建/销毁。

**缓解：**
- S2 spec 必须明确定义 SSE 事件契约
- S4 实现时优先做这部分的单元测试（用 mock SSE 流）
- Vue 组件用 composable `useSSEStream()` 封装

#### 风险 4：scrollFollowManager 状态机移植（中）
legacy 有一个相当复杂的"自动滚动跟随"状态机：用户向上滚动时打断跟随、流式输出新内容时检查是否在底部、显示"跳到底部"按钮、移动端手势识别。

**缓解：**
- 封装为 `useScrollFollow()` composable
- 在 S2 spec 中绘制状态机图

#### 风险 5：Draft 模式 → 正式 Run 的 lazy 升级（中）
新建 SOP 时不立即创建 run（避免占用配额），而是用 `draft_{templateId}` 作为 localStorage key，首次执行节点时才调用 `lazyCreateSOPRun()` 升级。涉及配额扣减触发点。

**缓解：**
- S2 spec 必须详细描述 Draft 生命周期
- 在 S4 实现时与后端 `CreateRun` 配额检查路径对齐

#### 风险 6：本次不可避免地接触计费/配额/Langfuse 链路（低）
SOP 节点执行 → 配额扣减 → Langfuse trace → 计费记录。这条链路在后端，前端只是触发点，重写不应破坏。但 Draft 升级、错误重试、超时处理等边界情况可能引发副作用。

**缓解：**
- 前端纯 view 层 + API 调用，不在前端做任何业务判断
- S5 Playwright E2E 必须覆盖：trial 配额耗尽、standard 月度重置、premium 无限运行、节点中途失败重试、网络中断恢复

### 涉及仓库
- [x] numind-server（P0 安全修复 + DTO 改造，~5 个文件）
- [x] numind-web-v3（重写 SOP 运行页 + 删除 legacy 文件，~15-20 个文件新增 / 3 个文件删除）
- [ ] numind-admin-web（不涉及）

### AI 可观测性（如功能涉及 LLM 调用）
- [x] 涉及 LLM 调用：是
- **Trace 起点**：`internal/numind/biz/sop/sop.go` 的 `ExecuteNodeStream` 方法（已存在，line 686-695 处调用 `langfuse.CreateTrace`，**不在 executor.go**），下游 `internal/numind/biz/sop/executor.go:565` 处生成 `sop-node-<name>` generation
- **Generation 点**：节点 LLM 调用 + chat 流式调用（已存在，本次不新增）
- **关键元数据**：run_id、node_id、node_name、user_id、template_id、user_tier（已记录）
- **本次重写对 trace 链路的影响**：**零** —— trace 创建在后端，前端只是 HTTP 调用方，不传递任何 trace ID，不参与 trace 创建

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事

1. 作为 **B 端客户**，我通过 self-service-config 配置了一个 5 步的 SOP，每步有自定义的名称和描述。我希望 C 端用户访问这个 SOP 时，看到的就是 5 个步骤、显示我配置的名称和描述，而不是某套硬编码的"爆款口播仿写 4 步"。

2. 作为 **C 端用户**，我希望进入任何 SOP 运行页都能看到与该 SOP 主题一致的步骤标题、描述、提示。不应该看到某个写死的"运行次数 156 / 65% 进度条"绿色卡片 —— 这与我当前正在运行的 SOP 毫无关系。

3. 作为 **平台运营方**，我希望前端不再泄露后端 LLM 服务的 API key、base URL、model name。当前任何登录用户打开 SOP 运行页都能在浏览器 DevTools Network 面板里看到这些敏感信息。

4. 作为 **维护工程师**，我希望未来对 SOP 运行页的任何修改都能在 Vue + TypeScript + 组件化体系下进行，享受类型检查、组件单测、Playwright E2E 回归保护，而不是在 7518 行 vanilla JS 里"心惊胆战地改一行"。

### 验收标准

#### 数据契约（核心）
- [ ] 任意 SOP 模板的运行页步骤指示器**步骤数量** = 后端 `GET /v1/sop/templates/:id/nodes` 返回的 nodes 数组长度 + (template.trailing_chat_enabled ? 1 : 0)
- [ ] 每个步骤的**标题** = 后端返回的对应 `node.name`，无任何前端覆盖
- [ ] 每个步骤的**描述** = 后端返回的对应 `node.description`；为空 / NULL 时优雅退化（不显示描述行，不显示 "undefined" / "null" / 空白占位）
- [ ] 模板的**总标题** = 后端返回的 `template.name`
- [ ] 全局搜索 Vue 项目源码，**`TEMPLATE_CONFIGS` 字符串 0 命中**（除了 git deleted-files 历史）
- [ ] 全局搜索 Vue 项目源码，**`STEP_NAME_MAP` 字符串 0 命中**（这是 legacy 第二个硬编码常量，sop-legacy.js:111）
- [ ] 全局搜索 Vue 项目源码，**`applyTemplateCustomization` 0 命中**（覆盖逻辑入口）
- [ ] `numind-web-v3/public/legacy/sop-legacy.js` 文件被删除
- [ ] `numind-web-v3/public/legacy/sop-legacy.css` 文件被删除
- [ ] `numind-web-v3/src/views/SOPView.vue` 完全重写（不再有 `legacyReady` ref / `__sopLegacyInit` 调用 / `__sopLegacyCleanup` 调用）
- [ ] 侧边栏不存在"运行次数"绿色卡片（HTML 与 CSS 全部删除）

#### 安全 + IP 保护（P0）
- [ ] `GET /v1/sop/templates/:id/nodes` 返回的 node 对象**不包含** `api_key`、`base_url`、`model_name`、`timeout_seconds`、**`prompt`** 五个字段
  - 注意 `prompt` 是 B 端的核心 IP（系统提示词模板），与基础设施凭证一起隐藏 —— C 端用户只需要看到 step 的 name + description，不应看到驱动 LLM 行为的 prompt
- [ ] curl 直接调用该 API，验证 response body 中无以上 5 个字段
- [ ] 浏览器 DevTools Network 面板复查，确认前端不再能看到任何 LLM 服务凭证或 prompt 模板
- [ ] `POST /v1/config/sop-templates/:id/nodes`（CreateNode）拒绝 base_url / model_name / api_key 字段（即使前端发了，后端也忽略并不写入 DB）
- [ ] 同上 `PUT /v1/config/sop-templates/:id/nodes/:nodeId`（UpdateNode）
- [ ] 用 SQL 验证：若历史 sop_node 行中已经有不该出现的 base_url/model_name/api_key 数据（早期 self-service-config 写入的），S2 决定是否清理

#### 功能等价（重写不能丢功能）
- [ ] 步骤切换（前进 / 后退 / 直接点击步骤指示器）
- [ ] 节点执行 → 流式 SSE 输出 → 思维链显示 → Markdown 渲染
- [ ] 文件上传：PDF 转文本（`/v1/pdf/convert-to-text`）+ 图片 OCR（`/v1/ali/vision/analyze`）
- [ ] 输入框持久化（localStorage，按 runId 隔离，draft 模式按 templateId 隔离）
- [ ] **输入修改检测**（legacy 用 `originalInputValues` 比较 trim 值，决定"重新生成"按钮的文案与是否删除书签 —— S2 spec 必须明确等价复刻这个逻辑）
- [ ] Draft 模式 → 首次执行时 lazy 升级为正式 Run（`lazyCreateSOPRun()` 等价行为）
- [ ] **Draft 清理（Beacon）**：浏览器关闭 / 切换路由时通过 `POST /v1/sop/runs/:id/draft`（Beacon API）或 `DELETE /v1/sop/runs/:id/draft` 主动清理草稿，避免脏数据
- [ ] 节点权限检查（`is_accessible` 字段）
- [ ] 重新生成（节点级 + 聊天消息级）
- [ ] 复制 AI 输出到剪贴板
- [ ] 历史记录弹窗（列表 / 进度 / 删除 / 切换到不同 run）
- [ ] 末尾 AI 聊天（`trailingChatEnabled` 控制）+ 多轮对话 + 重新生成
- [ ] **模型选择 + 深度思考绑定**（`window.__selectedModel.modelKey` + `window.isDeepThinking` 当前是全局变量，重写时改为 Pinia store + 通过 query param 传递给 `ExecuteNodeStream`，行为等价不打折）
- [ ] 自动滚动跟随 + 用户打断 + 跳回底部按钮（`scrollFollowManager` 状态机等价）
- [ ] 书签系统（应用 / 自动恢复 / 删除 / 修改时自动失效）
- [ ] sessionStorage 步骤恢复（刷新页面回到上次停留的 step）

#### 技术质量
- [ ] `npm run lint` 通过
- [ ] `npm run type-check` 通过（所有新组件 TypeScript 严格类型）
- [ ] `task lint` 通过（后端 DTO 改动）
- [ ] `go test ./...` 通过
- [ ] Playwright E2E 覆盖关键路径（具体路径在 S3 plan 的"S5 验证策略 task"中确定）

### 边界情况

#### 数据边界
1. **template.nodes 为空**：显示空状态（"该 SOP 暂未配置步骤，请联系创建者"），禁用所有交互
2. **node.description 为 NULL / 空字符串**：步骤标题正常显示，描述行不渲染（不留空白行）
3. **node.name 为 NULL**：fallback 显示 "步骤 N"（N = sort + 1）—— 此为不应发生的异常但需防御
4. **template.trailing_chat_enabled = false 且 nodes.length 全部完成**：直接显示完成态，不显示第 N+1 步聊天
5. **SOP 步骤数量 > 10**：步骤指示器横向滚动（移动端自动）/ 桌面端折叠为 `.stepper--collapsed` 视图

#### 网络边界
1. **`GetTemplateNodes` 失败**：显示 error 状态 + retry 按钮，禁用交互
2. **节点执行 SSE 中断**：保留已收到的部分内容，显示"连接中断，请重试"，提供"继续生成"按钮
3. **节点执行后端返回 401**：触发 axios 拦截器跳转登录页（与全局保持一致）
4. **节点执行后端返回 402（积分不足）**：触发 `InsufficientCreditsDialog`
5. **节点执行后端返回 403（权限不足 / 配额耗尽）**：显示具体原因 + 升级会员引导

#### 并发与状态边界
1. **同一节点连续点击"下一步"**：第二次点击被忽略（按钮 disabled 状态）
2. **流式输出过程中用户切换步骤**：保留流式输出在原步骤继续，不中断
3. **流式输出过程中用户关闭浏览器**：后端继续执行（后端是异步的），下次进入时通过 `GetRunStatus` 恢复
4. **文件上传中用户切换步骤**：上传继续进行，结果到达时若用户已离开，结果累积到该步骤的 textarea，下次回来可见
5. **多浏览器 tab 同时打开同一 SOP**：localStorage 共享，可能产生竞态。本次不解决，记录为 known limitation

#### 数据一致性边界
1. **B 端在 C 端运行中途修改了 SOP 步骤配置**：C 端 run 已经创建，按 run 创建时刻的快照执行，不响应 template 的实时变化
2. **B 端删除了正在被 C 端运行的 SOP**：C 端 run 仍可继续完成（外键 ON DELETE SET NULL 或 RESTRICT，需在 S2 确认后端行为）

### 权限规则

| 用户等级 | SOP 运行页可访问 | 可执行节点 | 备注 |
|---|---|---|---|
| free | ❌ | ❌ | 直接跳转到升级页 |
| trial | ✅ | ✅ (10 次总量) | 配额耗尽显示升级引导 |
| standard | ✅ | ✅ (20 次/月) | 配额耗尽显示升级引导，月度重置 |
| premium | ✅ | ✅ (无限) | 无限制 |

- 模板权限白名单：通过 `customers.HasTemplatePermission(userID, templateID)` 检查
- 模板发布状态：B 端创建的 template 必须 `publish_status = 'published'` 才可被 C 端访问

### UI 行为规格

#### 页面位置
- 路由：`/sop?templateId=:id&runId=:id`（保持现有 URL 契约不变，因为有外链可能依赖）
- 组件：`numind-web-v3/src/views/SOPView.vue`（重写为 Vue 3 Composition API）

#### 布局要求
- 顶部：返回首页按钮 + 模板标题（动态从 API）+ 历史记录按钮
- 主区：步骤指示器（横向 stepper，动态长度）+ 当前步骤内容区（动态标题/描述/输入/输出）
- 侧边栏：**删除**（不再有任何硬编码绿色卡片，整个侧边栏可以移除或留空）
- 底部：步骤导航按钮（上一步 / 下一步 / 重新生成 / 复制）

#### 交互模式
- 步骤切换：点击步骤指示器（受 `canAccessStep()` 约束）
- 节点执行：点击"下一步"按钮触发 API + 流式渲染
- 文件上传：点击上传按钮 / 拖拽到输入区域
- 重新生成：点击"重新生成"按钮 → 显示确认 → 触发 API（保留输入，重新流式输出）
- 聊天对话：第 N+1 步显示聊天界面，回车 / 点击发送

#### 状态处理
- **loading**：
  - API 加载 template/nodes：显示 skeleton（步骤指示器骨架 + 内容区骨架）
  - 节点执行中：内容区显示"AI 正在分析中..."loading dots + 流式输出
  - 文件上传中：上传按钮区域显示进度
- **empty**：
  - template.nodes 为空：显示"该 SOP 暂未配置步骤"+ 联系创建者引导
  - 历史记录为空：显示"暂无运行记录，开始你的第一次 SOP 运行吧"
- **error**：
  - API 失败：显示 error 卡片 + retry 按钮
  - SSE 中断：保留部分内容 + "继续生成"按钮
  - 401 → 跳转登录
  - 402 → InsufficientCreditsDialog
  - 403 → 升级引导
- **success**：
  - 节点执行完成：显示输出 + 工具栏（复制 / 重新生成 / 下一步）
  - 全部节点完成：显示"SOP 运行完成"+ trailing chat（如启用）

---

## §5 Rollback 策略：全量切换

经决策人选择，本次采用**全量切换**而非双轨并存。新 Vue 实现直接替换 legacy，无 feature flag、无灰度。

### 切换前检查（S5 验证阶段必须 100% 通过）

- Playwright E2E 必须覆盖 §4 验收标准的**全部功能等价清单**
- 关键路径必须有 happy path + error path 两套断言
- DTO 安全修复必须用 curl 实测确认 5 个字段全部消失
- B 端 self-service-config 配置的 SOP 必须能被 C 端正确运行（端到端验证）

### 部署顺序

由于全量切换 + 后端 DTO 是 breaking change，部署必须按以下顺序，避免新前端拿不到字段：

1. **先部署后端**：合并 numind-server 改动（DTO + 字段守卫 + GetTemplateNodes 元信息扩展），验证 dev API 返回结构正确
2. **立即部署前端**：合并 numind-web-v3 改动，删除 legacy 文件
3. **5 分钟内冒烟测试**：dev 环境跑一遍 happy path

不推迟、不分两次。同一窗口完成。

### 紧急回退路径

如果上线后发现 P0/P1：

```bash
# 前端紧急回退（最快，~3 分钟）
cd numind-web-v3
git revert <vue-rewrite-merge-commit>
git push develop  # 触发 dev 自动部署
# 或 push release/main 触发 prod

# 后端紧急回退（如果 DTO 改动也要撤）
cd numind-server
git revert <backend-merge-commit>
git push develop
```

**前置条件：** S4 实现时必须把 numind-server 和 numind-web-v3 各自的"重写完成"commit **保留为单独的 merge commit**（不要 rebase / squash），以便 `git revert` 一条命令搞定。S3 plan 必须明确这一点。

### 全量切换的代价（已与决策人对齐）

- 新版有 bug → 所有用户立即受影响，无灰度缓冲
- 紧急回退窗口：5-15 分钟内 SOP 不可用
- SPA 缓存：旧前端可能在用户浏览器中残留 24h，会读到新 DTO 缺字段 → 表现为页面崩溃 / 字段 undefined。**缓解：** 前端构建产物的文件名带 hash，浏览器拿到新 index.html 时会自动加载新 chunk
- **决策人接受这些代价**，理由：用户不多、一人公司协调成本低、S5 Playwright E2E 严格覆盖

---

## §7 决策记录（S1 阶段）

| 决策 | 选择 | 理由 |
|---|---|---|
| 重写策略 | 完整 Vue 3 重写（方向 B），非外科手术式部分迁移 | 双渲染路径无法外科手术清除，必须根治 |
| 安全漏洞处理 | 并入本次重写 S2 spec，不拆独立 hotfix | 用户选择 |
| 数据真相源 | **数据库 = 唯一真相源，前端零硬编码** | 用户选择 |
| 硬编码模板 templateId=1, 2 | 不需要"迁移" —— DB 已有真实数据，只需删除前端覆盖 | 实测 dev DB 确认 |
| description 为 NULL 的老节点 | UI 优雅退化，不在本次做 SQL backfill | 数据库为准原则 |
| 绿色侧边栏卡片 | 直接删除（方向 C） | 用户选择 |
| B 端可配置字段范围 | 仅 prompt / name / description / 顺序，禁止 base_url / model_name / api_key | 用户选择 |
| 视觉风格 | 保持现有 + DESIGN.md token，不做品牌升级 | 范围控制 |
| 验证策略 | Playwright E2E（非 gstack /qa） | NDF Rule 10 + 高风险重写 |
| **隐藏字段范围（修订）** | api_key / base_url / model_name / timeout_seconds / **prompt** 五字段全部隐藏 | Reviewer 发现遗漏 prompt（B 端核心 IP），2026-04-11 修订 |
| **SOPView.vue 描述（修订）** | 1019 行 Vue hydration wrapper，不是"空壳"。重写要 own its own state，不是替换一个空文件 | Reviewer 实测纠正 |
| **多 tab 竞态（终决）** | 不考虑、不修复、不实测。决策人判断"两个设备同时运行不会发生，发生了也没关系" | 用户决策 2026-04-11 |
| **Rollback 策略（终决）** | 全量切换，不做双轨并存 / feature flag / 灰度。依赖 git revert + redeploy 紧急回退（~5-15 分钟窗口） | 用户决策 2026-04-11，知情代价后选择 |
| **Single-session 强制下线（拆出独立功能）** | 不在本次范围。SOP 重写先做，single-session-enforcement 作为后续独立 Standard 功能（强制下线策略） | 用户决策 2026-04-11 |

## §8 待 S2 解决的开放问题

1. **trailing chat 的 UI 位置**：是作为第 N+1 步显示在 stepper 上，还是作为"完成后的后续对话"显示在 footer？legacy 是前者
2. **历史记录弹窗的复用**：是否有可能复用 `numind-web-v3/src/views/sales/` 下已有的聊天组件？需要 S2 调研
3. **删除 legacy 文件的时机**：见 §5 Rollback —— S3 plan 的最后一个 task，配合 git tag
4. **后端 SopNodeDTO 的字段范围**：实测确认 `parent_id` / `is_root` 在 `biz/sop/sop.go` 和 `controller/v1/sop/sop.go` 中**零引用**，可安全排除。S2 spec 中作为 design note 记录"为什么不返回这两个字段"
5. **第三方库迁移**：marked.js / highlight.js / DOMPurify 当前是 CDN 加载，重写时改为 npm 依赖（更好的版本管理 + bundle 优化）
6. **历史 sop_node 数据清理**：是否存在 self-service-config 之前 B 端写入的 base_url/model_name/api_key 残留数据？SQL 查询确认，决定是否在 S2 spec 中加入数据清理 task
8. **`sop_run.template_id` 外键 ON DELETE 行为**：实测 `SHOW CREATE TABLE sop_run` 确认是 SET NULL / RESTRICT / CASCADE，决定 PRD 边界 4.2 的精确行为
9. **PRD 验收标准的可测性细化**：S2 spec 中将"不显示 undefined / null"等断言转化为具体的 Playwright `expect()` 调用或 DOM snapshot 断言

## §9 Reviewer 发现的 P2 问题（S2 解决）

由独立 reviewer subagent 验证发现，已记录但不阻塞进入 S2：

- **trial 配额机制**：trial 使用与 standard 相同的 `MonthlySopRuns` 列，"10 次总量"效果由 `TierExpires` 3 天 + 计数器不重置实现。S2 E2E 测试必须 exercise 正确的 column
- **`parent_id` / `is_root` 是 dead fields** in 当前用户运行路径，DTO 中显式排除并加 design note
- **PRD 验收标准的可测性**：见 §8 第 9 条
- **CreateRun 中的调试日志硬编码 `~/Desktop/...` 路径**（`controller/v1/sop/sop.go:535-576`）：与本次重写无直接关系的 latent bug，可在 S2 spec 中作为 opportunistic cleanup 注释
- **端点数实际是 ~20 个**（不是 ~15），proposal §3 已修正

## §10 输入文档（必读）

- `numind-server/requirements/sop-runtime-vue-rewrite.md` — S0 requirement card
- Subagent 1 报告（legacy feature inventory，记录在 NDF S1 调研日志）—— S2 spec 必须将其转化为完整保留功能清单
- Subagent 2 报告（后端 API 字段审计 + 安全漏洞定位）—— 注意其中"硬编码模板数据库无记录"的结论是错误的，已被人工核实推翻
- `numind-server/docs/superpowers/specs/2026-04-09-self-service-config-design.md` — self-service-config spec，理解 B 端配置数据如何写入
- DB 实测确认结果：dev 环境 templateId=1, 2 真实存在，nodes 真实存在但 description=NULL
