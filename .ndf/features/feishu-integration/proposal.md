# 飞书集成（C 端用户绑定个人飞书 → agent 调用飞书能力）— 提案

> S1 产出。S0 需求卡见同目录 `requirement.md`。方案 = 方案 A：每用户自建应用 + agent 代办，无需 ISV。

## §0 前提确认（Premise Challenge）

| # | 前提 | 状态 |
|---|------|------|
| P1 | C 端用户绑自己飞书 = 每用户在自己组织建自建应用、单租户、永不跨组织 → **无需 ISV/无需审核** | ✅ 已核实（飞书文档 + lark-cli 行为） |
| P2 | 飞书有程序化建应用能力（`apps +create` → `/open-apis/spark/v1/apps`）+ device-code/授权码 OAuth | ✅ 已核实（lark-cli 二进制 strings） |
| P3 | 有数 agent 的挂起恢复(yield/resume)能支撑「中途出去授权 → 回来从断点续跑」，且**双暂停**（建应用 + 授权）也不丢进度 | ✅ 已核实（`tool_ask_user_question.go` + `answer.go` + `resume_transcript.go` 的 mergeResumeTranscript） |
| P4 | 飞书个人版/无组织用户能否建自建应用 = **客户侧解决，不在本 feature 范围** | ✅ 用户拍板（2026-06-23） |
| P5 | 产品价值已确认，必做 | ✅ 用户拍板 |

## §1 方案概述 [客户可见]

用户在有数点「连接飞书」→ agent 引导完成绑定（中途给用户两个浏览器链接：① 创建飞书应用，填个名字头像 ② 给应用开通并授权权限），连上后 agent 就能用一套飞书工具，**在用户自己的飞书里**写云文档、发消息、读写多维表格等。

关键体验：agent 执行任务**中途**需要绑定/授权时，会优雅暂停、把链接递给用户；用户在浏览器完成后回到有数，**agent 从断点接着把原任务跑完，不会重来、不会死循环**（复用有数现成的 agent 挂起恢复机制）。

每个用户连的是**自己**的飞书，账号间完全隔离，无需任何审核上架。

## §2 工作量与周期 [内部]

> 内部产品，无对客报价。按单个后端+前端开发估算。

| 阶段 | 内容 | 估算 |
|------|------|------|
| Phase 0 Spike | 打通 provisioning 全链路 + 攻克 bootstrap（建应用初始 token 怎么来）；用 lark-cli 当参考/兜底验证 | 2-3 天 |
| 地基 | provisioning 流程 + OAuth 回调 + `user_third_party_account` 表 + 刷新中间件 + **yield/resume 扩展支持「授权暂停」类型** | 5-7 天 |
| 飞书客户端 + 首批工具 | oapi-sdk-go 封装 + 首批 2-3 个飞书工具 | 3-4 天 |
| 前端 | 「连接飞书」入口 + agent 对话内授权暂停卡片渲染 + 连接状态/解绑 | 3-4 天 |
| 测试 + dev 验收 | 单测 + gstack 浏览器端到端 + dev 部署 | 2-3 天 |
| **合计** | | **~15-21 天**（约 3-4 周 / 单人） |

> Spike 结果可能上下浮动地基估算：若 bootstrap 无法第三方复刻、必须包 lark-cli，地基偏上限。

**Phase 0 Spike exit criteria**：能端到端用一个**测试账号**程序化建出一个 app + 走完授权码流拿到 user_access_token。**决策点：第 2 天若 bootstrap 仍未通 → 切方案 A（lark-cli wrapper）**，不继续耗在 native 重写上。同步验证：飞书 user_access_token 是否带 refresh_token（决定刷新 vs 重授权策略）。

## §3 技术可行性 [AI 内部]

### 现有功能复用
- **agent 工具框架** `FullTool`/`BaseTool`（`biz/agent/`）：飞书工具与 `web_fetch`/`image_gen` 同形状。
- **用户身份流转**：`user_id` 已通过 context 流进每个工具 `Execute`（`middleware.UserIDFromCtx` / `billing.FromContext`）→ 「按当前用户取飞书 token」前提天然具备。
- **挂起恢复机制**（核心复用）：`yieldError` 哨兵 → 持久化 `messages`+`pending_question_json` → `state_reason=waiting_for_user_choice` → `/answer` 端点 → `runner.Run(ExistingRunID)` 从断点续。`mergeResumeTranscript` 支持多次暂停不丢进度。**yield 机制底层通用（任何工具可触发），目前仅 ask_user_question 接线 → 需扩展**。
- **工具注册** Registry/Factory（`factory_platform.go`）；HTTP 基础设施 `internal/pkg/httpclient/`。

### 技术风险
- **R1（最大）bootstrap 机制**：调 `/open-apis/spark/v1/apps` 建 app 需先有 token，token 又需 OAuth client（鸡生蛋）。lark-cli 内置「引导 client」解了，但第三方能否复刻未知。**缓解**：Phase 0 spike 先攻克；兜底=把 lark-cli（MIT）包在一个干净内部接口后只用于「建应用」这一步。
- **R2 yield/resume 仅问答接线**：现 `YieldPayload` 是「问题+选项」形（`yield_error.go`）。**缓解**：在 `YieldPayload` 加 `pause_type: "question" | "auth"` 字段区分（前端据此渲染不同卡片）。**⚠️ 硬约束（CLAUDE.md §6b I2/I7）：TerminalReason(19)/LoopEvent(19) 枚举不新增**——授权暂停**复用** `TerminalWaitingForUserChoice` + `LoopEventAskUserPaused`，禁止新建 `waiting_for_auth` 之类枚举。
- **R3 凭据安全（零基础，独立技术任务）**：现库无列级加密先例（`model/llm.go:11` 的 APIKey 是明文，仅 `json:"-"`）。**缓解**：新建 `internal/pkg/crypto` 包做应用层 **AES-256-GCM**，密钥从 config 注入（禁硬编码，遵循 §7）；`user_third_party_account` 的 secret/token 列存密文；不落明文日志。
- **R4 授权流形态（S1 修正 S0）**：S0 requirement.md 记的是 lark-cli 的 **device-code**（CLI 无法接收重定向才用它）。**S1 决策：有数是 web，改用授权码重定向流**（授权 → 飞书重定向回有数 callback → 换 token → 触发恢复，无需轮询），device-code 不采用。「建应用」这步无重定向，完成检测见 §3.5（暂定用户手动确认，Spike 后若有程序化检测再改）。
- **R5 个人版覆盖率**：超范围（P4，客户侧）。

### 涉及仓库
- [x] numind-server（provisioning + token store + 刷新 + 飞书工具 + OAuth 回调端点）
- [x] numind-web-v3（连接飞书入口 + 授权暂停渲染 + 状态/解绑）
- [ ] numind-admin-web（本期不涉及）

### AI 可观测性
- [x] 涉及 LLM 调用：**间接**——飞书工具本身**不是** LLM 调用（无 generation），但运行在 agent run 内，已有 trace。
- Trace 起点：沿用 `AgentRunner.Run` 现有 trace（不新建）。
- Generation 点：N/A（飞书工具是工具调用，非 LLM）。建议每个飞书工具调用记一个 **span**（`langfuse.CreateSpan`，name=`lark.<tool>`），与现有工具一致。
- 关键元数据：user_id、tool_name、lark_app_id（不记 token/secret）。

## §3.5 关键链路与数据结构（S1 草案，S2 细化）

### OAuth callback → run 恢复 完整链路（本 feature 最复杂的一段，单列）
1. 飞书工具/连接流触发 yield：`pending_question_json` 存「授权问题」（含 OAuth URL），`pause_type=auth`。
2. 生成 OAuth URL，`state = base64(HMAC_sign({user_id, run_id, question_text, nonce}))`（HMAC 防 CSRF/伪造，密钥从 config）。
3. 用户浏览器点 URL → 飞书授权页 → 同意。
4. 飞书重定向到 `GET /v1/feishu/oauth/callback?code=...&state=...`（**新端点，不挂 JWT 中间件**——浏览器重定向无 token）。
5. callback handler：验 `state` HMAC → 解出 user_id/run_id/question_text → 用 code 换 token → 加密入库。
6. **服务端内部直接调 `biz.Answer(runID, {question_text: 已授权})`**（不是走 HTTP `/answer`；`/answer` 校验 `asked[qText]`，所以 key 必须用 yield 时存的 question_text）→ `runner.Run(ExistingRunID)` 从断点续。

> 「建应用」那步同构，但无飞书重定向 → 暂定用户在 agent 卡片点「我已建好」(free_text) 触发恢复；Spike 若发现程序化完成检测再改。

### `user_third_party_account` 表（草案）
| 列 | 类型 | 说明 |
|----|------|------|
| id | bigint PK | |
| user_id | uint FK→users | |
| provider | varchar(32) | `lark` |
| app_id | varchar(64) | 用户自建 app 的 appId |
| app_secret_enc | blob | AES-256-GCM 密文 |
| access_token_enc | blob | 密文 |
| refresh_token_enc | blob | 密文（若飞书提供 refresh_token） |
| token_expires_at | timestamp | |
| scopes | varchar(512) | 已授权 scope 列表 |
| created_at / updated_at | timestamp | |

唯一约束 `uniq_user_provider (user_id, provider)` → 支持「重复授权=更新而非新建」幂等。

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事
- 作为有数用户，我要把**我自己的**飞书账号连接到有数 agent，以便 agent 能直接在我的飞书里写文档/发消息/填表格。
- 作为有数用户，agent 在任务**中途**需要我去浏览器建应用/授权时，我完成后回到有数，agent 能**从断点接着跑完原任务**，不用重来。
- 作为有数用户，我能查看飞书连接状态、随时解绑。

### 验收标准
- [ ] 用户从「连接飞书」入口发起 → agent 给出建应用链接（暂停态）→ 用户浏览器建应用 → 回来 → agent 恢复（同一 run，历史完整）。
- [ ] agent 继续给授权链接（第二次暂停）→ 用户授权 → 飞书重定向回有数 callback → 自动恢复 → token 入库（加密）。
- [ ] 连接后，agent 调用首批飞书工具，成功在**该用户自己的**飞书里产生效果（如建出一篇 docx）。
- [ ] token 过期时刷新中间件自动续；refresh 失败时引导重新授权（不静默失败）。
- [ ] 解绑后 token 清除，再次调用飞书工具触发重新连接流程。
- [ ] 两次暂停全程同一个 `agent_run`，无「从头重来/死循环」。

### 边界情况
- 用户中途放弃授权（暂停态长期挂起）→ 不报错，下次可继续/取消。
- device/auth code 过期 → 重新发起，旧链接失效提示。
- 重复授权 / 已连接再连 → 幂等，更新而非重建。
- 并发 run 共用同一用户 token → 读时按用户取，刷新加锁防并发刷新风暴。
- 飞书 API 返回权限不足（scope 没开全）→ 软错误（**遵循 agent 工具硬错误杀 run 的教训，必须 returnSoftError**），提示补授权。
- **token 刷新失败 / 无 refresh_token**（用户 token≤115min，等待期可能过期）→ 工具内 returnSoftError 告知「飞书连接已过期，请重新连接」，**不**在工具里再触发 yield；用户主动重连。
- **解绑语义**：解绑=仅删有数侧 token 行；飞书侧 app 是用户自建的、保留（用户可自行删）；重绑复用已有 app_id（幂等更新）。

### 权限规则
- 【**待确认 1**】哪些用户能用：建议**在期会员**（与现有 agent 能力门槛一致）；C 端非会员是否开放待定。
- 飞书侧权限 = 用户自己授权的 scope，有数不越权。

### UI 行为规格
- 页面位置：个人中心/设置「账号连接」区有「连接飞书」入口；agent 对话内的授权暂停以**卡片+链接**呈现（复用 `ask_user_question` 的暂停 UI 心智）。
- 布局：连接状态用状态卡（已连/未连/已过期）；遵循异步 4 状态（loading/empty/error/success）。
- 交互：点击发起 → 展示链接（可复制/二维码）→ 完成后自动恢复。
- 状态处理：未连接(empty+CTA) / 连接中(暂停态) / 已连接(success+解绑) / 过期(error+重连)。

## §5 备选方案（Alternatives）

| | 方案 | 摘要 | 工作量 | 风险 | 复用 |
|--|------|------|--------|------|------|
| **A** | 包一层 lark-cli | 服务端按 per-user profile 调 lark-cli（MIT）做建 app+授权，凭据存服务端 | S-M | 中（多租户跑 CLI 属 off-label、版本脆弱；**需核实飞书 ToS 是否限制 SaaS 场景包装 lark-cli**） | lark-cli 全部 bootstrap 逻辑 |
| **B** | 原生重写 | oapi-sdk-go + spark/apps + 授权码流，全自实现 | M-L | 中（须攻克 bootstrap） | 有数 token store + 工具框架 |
| **C** ⭐ | 混合（Spike→Native） | Phase 0 用 lark-cli 验证+学 bootstrap；生产走 native；若建 app 这步 bootstrap 无法第三方复刻，则该步保留 lark-cli wrapper 藏在干净接口后 | M | 低（先用 spike 把最大未知 R1 打掉再定） | 两者之长 |

**推荐：方案 C** —— 先用一个小 spike 把 R1（bootstrap）这个最大未知打掉，再决定建 app 这步走 native 还是 wrapper；授权/token/工具部分确定走 native，干净集成进现有 agent 架构。

## §6 待客户确认（S1 Gate）
1. **【产品】权限门槛**：飞书连接开放给「在期会员」还是「所有用户」？（建议在期会员）
2. **【业务】计费**：飞书 API 调用是否计 credits？（建议**不计**——只有 LLM 推理计费，与现有工具调用一致）
3. **【产品】首批工具范围**：建议首批 3 个=「写飞书文档」（scope `docx:document`）、「发消息」（`im:message`）、「读多维表格」（`bitable:app:readonly`）。⚠️ 授权步骤须**一次性开通首批工具全部 scope**，否则后续调用 403——scope 清单直接写进授权卡片提示语。是否调整范围？
4. **【方案】** 是否采纳推荐的方案 C（Spike→Native）。
