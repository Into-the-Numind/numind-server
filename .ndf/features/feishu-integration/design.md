# 飞书集成 — S2 技术设计 (spec)

> 上游：`requirement.md`(S0) + `proposal.md`(S1)。方案 = C（Spike→Native）。
> S1 gate 决定：① 不做功能层会员门（靠 agent runtime 积分消耗自然控制，非会员跑不了 agent）② 飞书 API 调用不计 credits ③ 首批工具=写文档/发消息/读多维表格 ④ 提案通过。

## §1 范围

**做**：用户连接自己飞书（per-user 自建应用 + 授权码 OAuth）→ token 加密入库 → agent 用 3 个飞书工具操作用户自己的飞书 → agent 任务中途授权可挂起恢复。
**不做**（本期）：飞书个人版能否建自建应用（客户侧）；日历/待办/邮件等更多工具（地基建好后增量）；admin 端管理界面。

## §2 架构与落点

```
numind-web-v3                         numind-server
─────────────                         ─────────────
[连接飞书]入口 ──POST /connect──────▶ controller/v1/feishu (新)
agent 对话授权卡片(pause_type=auth)        └─ biz/feishu (新): provisioning + oauth + tokenstore
飞书重定向 ──GET /oauth/callback───▶ controller (无JWT, state 校验)
[连接状态/解绑]                            └─ biz/feishu/client (新, oapi-sdk-go 封装)
                                      biz/agent/tool_lark_*.go (新, 3 工具)
                                      internal/pkg/crypto (新, AES-256-GCM)
                                      store: user_third_party_account (新表)
```

复用：`biz/agent` FullTool/BaseTool + yield/resume；`middleware.UserIDFromCtx`；`internal/pkg/httpclient`；langfuse span。

## §3 数据模型

### 表 `user_third_party_account`
```go
type UserThirdPartyAccount struct {
    ID             uint64    `gorm:"primaryKey;autoIncrement"`
    UserID         uint      `gorm:"not null;uniqueIndex:uniq_user_provider"`
    Provider       string    `gorm:"size:32;not null;uniqueIndex:uniq_user_provider"` // "lark"
    AppID          string    `gorm:"size:64;not null"`
    AppSecretEnc   []byte    `gorm:"type:blob"`   // AES-256-GCM
    AccessTokenEnc []byte    `gorm:"type:blob"`
    RefreshTokenEnc []byte   `gorm:"type:blob"`   // 若飞书提供
    TokenExpiresAt time.Time
    Scopes         string    `gorm:"size:512"`
    CreatedAt      time.Time
    UpdatedAt      time.Time
}
func (UserThirdPartyAccount) TableName() string { return "user_third_party_account" }
```
- migration `migrations/YYYYMMDD_HHMMSS_create_user_third_party_account.sql`（含 `uniq_user_provider`）。
- 唯一约束 → 重复授权=`UPSERT`（幂等更新）。

### Store 接口 `IThirdPartyAccountStore`
`Get(ctx,userID,provider)` / `Upsert(ctx,*acc)` / `Delete(ctx,userID,provider)` / `UpdateTokens(ctx,userID,provider,access,refresh,exp)`。

## §4 加密 `internal/pkg/crypto`
- `Encrypt(plain []byte) ([]byte, error)` / `Decrypt(cipher []byte) ([]byte, error)`，AES-256-GCM，随机 nonce 前置。
- 密钥来自 config `security.thirdparty_token_key`（32 字节 base64，**禁硬编码、禁进 config_prod.yaml** → 运维侧注入）。config 缺失则该 feature 启动报错（fail-fast，不静默明文）。
- 仅 store 读写边界调用 crypto，biz 层拿明文。

## §5 OAuth / Provisioning 流程 + API 契约

### 新端点（router.go 注册）
| 方法 | 路径 | 鉴权 | 说明 |
|------|------|------|------|
| POST | `/v1/feishu/connect` | user_token | 发起连接，返回当前需要的下一步（建应用 URL 或授权 URL）+ state |
| GET | `/v1/feishu/oauth/callback` | **无 JWT** | 飞书授权重定向回调，验 state→换 token→存→恢复 run |
| GET | `/v1/feishu/status` | user_token | 连接状态（未连/已连/过期 + scopes） |
| DELETE | `/v1/feishu/connection` | user_token | 解绑（删 token 行；飞书侧 app 保留） |

### state 设计（防 CSRF/伪造）
`state = base64url( payload + HMAC_SHA256(payload, key) )`，`payload = {user_id, run_id, step, question_text, nonce, exp}`。callback 验 HMAC + exp，再解出 user_id/run_id。key 复用 §4 或独立 config。

### 完整时序（授权步骤）
```
agent 工具需要飞书 → 检测未连/过期 → 返回 yieldError{pause_type:auth, auth_url, question_text}
  runner 持久化 messages+pending_question_json(含 question_text) → state=waiting_for_user_choice
前端渲染授权卡片(URL/二维码) → 用户浏览器授权 → 飞书 302 → GET /oauth/callback?code&state
  callback: 验 state → code 换 user_access_token(+refresh) → crypto 加密 → Upsert
          → 内部调 biz.Answer(run_id, {question_text: {free_text:"已授权"}}) → runner.Run(ExistingRunID) 断点续
```
> 「建应用」步骤同构，但飞书无重定向回调 → 暂定 agent 卡片让用户点「我已建好」(free_text) 触发 `biz.Answer` 恢复；Phase 0 Spike 若发现 `/open-apis/spark/v1/apps` 有完成回调/可轮询再优化。

### biz.Answer 复用约束（reviewer P1）
callback **不走 HTTP `/answer`**（那是带 JWT 的用户端点），而是 callback handler 内部直调 `biz.Answer()`；传入的 answer key 必须等于 yield 时存进 `pending_question_json` 的 `question_text`（`answer.go` 校验 `asked[qText]`）。

## §6 yield/resume 扩展（硬约束：不新增枚举）

- `YieldPayload` 加字段 `PauseType string`（`"question"|"auth"`，默认 question 向后兼容）+ `AuthURL string`（auth 时填）。
- **不新增** `TerminalReason`/`LoopEvent`（CLAUDE.md §6b I2/I7）→ 授权暂停复用 `TerminalWaitingForUserChoice` + `LoopEventAskUserPaused`。
- runner 处理 yieldError 的逻辑不依赖工具名（已验证），无需改；只需 payload 多带字段 + 前端按 `pause_type` 渲染。
- `/answer` 与 `biz.Answer` 对 auth 类暂停的恢复路径与问答一致（mergeResumeTranscript 撑多次暂停）。

## §7 飞书 API 客户端 `biz/feishu/client`
- 基于 `github.com/larksuite/oapi-sdk-go/v3`；按 user_access_token 构造 per-call client（不缓存跨用户）。
- token 过期：调用前查 `TokenExpiresAt`，到期且有 refresh_token → 刷新（加分布式锁防并发刷新）→ UpdateTokens；无 refresh_token 或刷新失败 → 返回 sentinel `ErrLarkReauthRequired`。
- **非 LLM 调用，不走 aiservice 入口**（aiservice 是 AI gateway；飞书是外部业务 API）。

## §8 飞书工具（首批 3 个，biz/agent/tool_lark_*.go）
统一基类继承 `BaseTool`，`Execute` 内：`UserIDFromCtx` → 取 token（过期处理见 §7）→ 调 client → **失败一律 returnSoftError**（绝不硬错误杀 run，遵循已知教训）。未连接/需重授权 → 软错误提示触发连接流。

| 工具 | 飞书 API | scope |
|------|---------|-------|
| `lark_create_doc` | docx 创建+写块 | `docx:document` |
| `lark_send_message` | im 发消息 | `im:message` |
| `lark_read_bitable` | bitable 读记录 | `bitable:app:readonly` |

授权步骤一次性请求**首批全部 scope**（缺则后续 403）→ 授权卡片提示语列全。

## §9 可观测性
每个飞书工具调用记 langfuse **span**（非 generation）：`langfuse.CreateSpan` name=`lark.<tool>`，metadata=user_id/tool_name/lark_app_id（**不记 token/secret**）。沿用 agent run 现有 trace，不新建。

## §10 前端契约（numind-web-v3）
- 设置/个人中心「账号连接」区：连接状态卡（异步 4 状态 loading/empty+CTA/error 重连/success+解绑），调 `/v1/feishu/status`、`/v1/feishu/connection`、`/v1/feishu/connect`。
- agent 对话内：`pause_type=auth` 的暂停渲染为「授权卡片」（URL 可复制+二维码），复用现有 ask_user_question 暂停 UI 心智；用户完成后前端轮询 run 状态自动续显。
- API 层走 `src/api/request.ts`；新建 `src/api/feishu.ts`；状态入 Pinia store（setup 语法）。

## §11 Phase 0 Spike（先行，打掉最大未知 R1=bootstrap）
- 目标：测试账号端到端程序化建 app + 走完授权码流拿 token。
- Exit criteria：能建出 1 个 app + 拿到 user_access_token + 确认是否有 refresh_token。
- **决策点：第 2 天 bootstrap 仍不通 → 切方案 A（lark-cli wrapper 藏在 `biz/feishu/provisioner` 接口后，仅用于建 app 这步）**，其余仍 native。
- 法务：若走 wrapper，核实飞书 ToS 是否限制 SaaS 包装 lark-cli。

## §12 PRD 覆盖核对
| PRD 项 | spec 落点 |
|--------|----------|
| 连接+双暂停恢复 | §5 §6 |
| token 加密/刷新/重授权 | §3 §4 §7 |
| 3 工具操作用户飞书 | §8 |
| 解绑幂等 | §3 §5 |
| 不计费/无功能门 | §8（工具不计费）+ 无会员检查 |
| 边界(软错误/并发刷新锁/code 过期) | §7 §8 |

## §13 任务切分预告（S3 细化）
后端先行：crypto 包 → 表+store → Spike → provisioning+oauth+callback → yield 扩展 → client → 3 工具 → trace。前端后随：feishu api+store → 连接 UI → 授权卡片。S5 验证：gstack 浏览器端到端（连接→建应用→授权→agent 写出 docx）。
