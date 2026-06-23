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

## 关键外部约束（已核实，影响时间线）
- **C 端任意用户授权 = 必须做「商店应用（ISV）」**：飞书规则——企业自建应用只能在同一企业内使用（单租户）；要服务任意企业/任意用户，必须上架飞书应用中心、需 ISV 资质、过飞书**官方审核**。([应用类型与能力](https://open.feishu.cn/document/platform-overveiw/overview)、[发布与审核](https://open.feishu.cn/document/best-practices/intro-to-custom-app-review))
- **审核是外部依赖、不在团队掌控内**（数周量级，且审核要求应用已可用）→ 开发可并行推进，**上线时间线受审核 gating**。
- **OAuth 机制本身成熟**：自建/商店应用获取 `user_access_token` 方式一致，标准 OAuth 2.0 授权码流程（RFC 6749）；user_access_token 有效期 ≤6900s（约 115 分钟），需定期刷新。([获取 user_access_token](https://open.feishu.cn/document/authentication-management/access-token/get-user-access-token))

## 架构落点（贴现有代码，S2 细化）
- **可复用**：agent 工具框架 `FullTool`/`BaseTool`（飞书工具同形状）；`user_id` 已通过 context 流进每个工具 `Execute`（`middleware.UserIDFromCtx` / `billing.FromContext`）→ 按当前用户取飞书 token 的前提天然具备；统一工具注册 Registry/Factory。
- **需新建**：OAuth 授权码流程（授权 URL + 回调 controller）；`user_third_party_account` 表 + 加密 token 存储；token 自动刷新中间件（115 分钟级）；飞书 API 客户端封装（`larksuite/oapi-sdk-go`）；按能力逐个新增的飞书工具。
- **成本结构**：贵的是「OAuth + token 存储 + 刷新」地基（一次性）；地基建好后每加一个飞书能力都是边际成本递减 → **不必一次做完「一整套」，先地基 + 2~3 个高价值工具打通，剩余增量加**。

## S1 待决项（留到可行性与提案阶段）
1. **ISV 资质主体**：用哪个公司主体申请飞书商店应用？能否拿到 ISV 资质？（总开关，提出人侧确认中）
2. **首批能力范围**：第一阶段先做哪 2~3 个飞书工具？（建议从授权 scope 干净、价值高的入手，如「写飞书文档」「发消息/群通知」「读多维表格」）
3. **飞书个人版 / 单人组织的安装路径**：商店应用需被安装进用户租户；C 端个人/单人组织如何自助安装 + 授权，需在 S1 验证 UX 路径。
4. **计费策略**：飞书 API 调用是否计入有数 credits？还是免费（仅 LLM 调用计费）？
5. **token 安全**：加密存储方案（KMS / 应用层加密）、refresh 失败/过期后的重新授权 UX。

## 备注
- 当前另有活跃 feature `xhs-collector`（S0/S1，并行 session），与本 feature 互不冲突（NDF v3 多 feature 并行）。
- 已有 `biz/monitor/feishu.go` 仅为 webhook 告警卡片，**不是** OpenAPI 集成，不可复用为授权链路。
