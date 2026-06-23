# 飞书集成（C 端用户绑定个人飞书 → agent 调用一整套飞书能力）

## 来源
- 提出人：Michael（产品/创始人）
- 提出日期：2026-06-23

## 需求描述
在有数 agent mode 中，让**每个 C 端用户通过 OAuth 授权绑定自己的飞书账号**，授权后 agent 能以「该用户身份」调用一整套飞书 OpenAPI 能力（云文档读写、群/单聊消息、多维表格 Base、日历、待办、知识库等），把飞书变成 agent 可操作的「手」。

调研入口是 `lark-cli`（飞书开放平台官方 CLI，`@larksuite/cli`，仓库 github.com/larksuite/cli）。生产**不直接用该本地 CLI**，而是复用其背后的「OAuth 授权码流程 + 飞书 OpenAPI」机制，用官方 Go SDK `larksuite/oapi-sdk-go` 在服务端按「每用户 token」调用。

## 业务目标
给 agent 装上飞书生态的执行能力，让 agent 不止「产出内容」，还能直接把结果落到用户自己的飞书工作空间（写文档、发消息、填表格）。产品价值已由提出人确认 → **必做**。

## 优先级
高

## Triage
- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：**是**（新增第三方账号绑定表，加密存 access/refresh token）
  2. 新增 API 端点：**是**（OAuth 授权 URL 生成 + 回调换 token + 账号解绑/状态查询）
  3. 新外部服务集成：**是**（飞书 OpenAPI，全新外部服务）
  4. 影响文件数：**>3**（后端 OAuth 地基 + token store + 刷新中间件 + 飞书工具 + 前端绑定入口）
  5. 高风险业务逻辑（支付/权限）：**是**（处理用户授权凭据 / token，安全敏感）
- 人类决定：**确认 Standard**（提出人已确认）

## 选定方案：方案 A —— 每用户自建应用 + agent 代办（无需 ISV）

**模型 = Claude Code / lark-cli 同款**：每个用户在**自己的飞书组织**里建一个自建应用，agent 代办大部分步骤，用户只做两步浏览器操作（① 填应用名+头像创建应用 ② 点开通并授权 scope）。每个 app 单租户、永不跨组织 → **永远碰不到 ISV，无需官方审核**。账号间天然隔离。

> 决策修正（2026-06-23）：S0 初稿误判「C 端=必做 ISV」。复核 lark-cli 二进制源码后推翻——飞书有程序化建应用能力，per-user 自建应用模型成立，ISV 不是必须。商店应用/ISV 仅在「一个共享 app 跨组织一键服务所有人」时才需要，本方案不采用。

### 已核实的底层机制（来自 lark-cli 二进制 strings + README）
- ⚠️ **更正（S4 T1 Spike 2026-06-24，见 `spike-bootstrap.md`）**：原写「`apps +create`→`/open-apis/spark/v1/apps` = 建自建应用」**是错的**——那是飞书 aPaaS 应用引擎平台，不产生 appId/secret。开放平台「自建应用」**无公开创建 API**；lark-cli 靠特权专属端点 `application/v6/larksuite_cli_app/probe`，第三方不可复用。→ 建 app 只能：W 包 lark-cli / M 引导手动 / I 商店应用 ISV。
- **用户授权 = OAuth**：`/open-apis/authen/v2/oauth/token`（标准 OAuth，公开可原生）。
- 底层 `github.com/larksuite/oapi-sdk-go/v3`；**lark-cli 为 MIT 开源**（可包一层）。
- 底层 `github.com/larksuite/oapi-sdk-go/v3`；**lark-cli 为 MIT 开源**（可包一层或参考重写）。
- user_access_token 有效期 ≤6900s（约 115 分钟），需定期刷新。

### 唯一的工程差异：有数是托管 SaaS（lark-cli 是本地）
provisioning + 凭据存储必须在**服务端按用户**做。两条实现路径，S2 定夺：
- **A1 包一层 lark-cli**（快）：后台按 per-user profile 调 lark-cli `apps +create` / `auth login`，复用其全部 bootstrap 逻辑；代价=把 CLI 塞进生产、管多 profile，运维偏 hacky。
- **A2 原生重写**（干净）：`oapi-sdk-go` + spark/apps + device-code 自实现；代价=要搞清 bootstrap client。

## 架构落点（贴现有代码，S2 细化）
- **可复用**：agent 工具框架 `FullTool`/`BaseTool`（飞书工具同形状）；`user_id` 已通过 context 流进每个工具 `Execute`（`middleware.UserIDFromCtx` / `billing.FromContext`）→ 按当前用户取飞书 token 的前提天然具备；统一工具注册 Registry/Factory。
- **需新建**：per-user app provisioning（建 app + device-code 授权）流程 + 回调/轮询；`user_third_party_account` 表（存每用户 appId/secret/token，加密）；token 自动刷新中间件（115 分钟级）；飞书 API 客户端封装；按能力逐个新增的飞书工具。
- **成本结构**：贵的是「provisioning + token 存储 + 刷新」地基（一次性）；地基建好后每加一个飞书能力边际成本递减 → 先地基 + 2~3 个高价值工具打通，剩余增量加。

## S1 待落实项（都不是 ISV、不是 blocker）
1. **Bootstrap 机制**（核心）：调 `/open-apis/spark/v1/apps` 建 app 需先有 token，拿 token 又需 OAuth client → lark-cli 内置「引导 client」解了鸡生蛋。有数借用还是自注册？决定走 A1 还是 A2。
2. **飞书个人版 / 无组织用户能否建自建应用**（⚠️ 真正的覆盖率风险）：建自建应用要求账号在有开发者权限的组织里；纯个人版创作者能否建需向飞书确认——决定 C 端可用比例。
3. **首批能力范围**：第一阶段先做哪 2~3 个飞书工具？（建议从 scope 干净、价值高入手，如「写飞书文档」「发消息」「读多维表格」）。
4. **计费策略**：飞书 API 调用是否计入有数 credits？还是免费（仅 LLM 调用计费）？
5. **token/secret 安全**：每用户 appSecret/token 加密存储方案、refresh 失败/过期后的重新授权 UX。

## 备注
- 当前另有活跃 feature `xhs-collector`（S0/S1，并行 session），与本 feature 互不冲突（NDF v3 多 feature 并行）。
- 已有 `biz/monitor/feishu.go` 仅为 webhook 告警卡片，**不是** OpenAPI 集成，不可复用为授权链路。
