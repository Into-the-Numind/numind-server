# 飞书集成 实施计划 (S3)

> 上游：`design.md`(S2)。执行：NDF S4 每 task RED→GREEN→REFACTOR + 双 reviewer。
> 顺序：后端 T1–T10 先于前端 T11–T13；T14=S5 验证策略。

**Goal**：每个有数用户连接自己的飞书（per-user 自建应用 + OAuth），agent 用 3 个工具操作其飞书，任务中途授权可挂起恢复，无需 ISV。

**Architecture**：复用 agent yield/resume；新建 OAuth provisioning 地基 + 加密 token 存储；飞书工具走 oapi-sdk-go（不走 aiservice）。

## Global Constraints（逐 task 隐含）
- 不新增 `TerminalReason`/`LoopEvent`（`[14]`/`[21]` 编译期不变量）；授权暂停复用 `TerminalWaitingForUserChoice`+`LoopEventAskUserPaused`。
- 飞书工具失败一律 `returnSoftError`，禁硬错误（杀 run）。
- 凭据加密存储，密钥 config 注入，**禁硬编码/禁进 config_prod.yaml**，缺失 fail-fast。
- 新端点必须注册 router.go；schema 变更必须建 migration；改 Go 后 `task lint`。
- 飞书 API 不计 credits；无功能层会员门。

---

### Task 1: Phase 0 Spike — bootstrap 可行性（最先，打掉 R1）
**类型**：探索性，deliverable=决策。
**Files**：Create `.ndf/features/feishu-integration/spike-bootstrap.md`（throwaway 代码不入库）。
**步骤**：用一个飞书测试账号，验证能否程序化①建 app（`/open-apis/spark/v1/apps` native，或退而求其次包 lark-cli）②走授权码流拿 `user_access_token`；记录是否返回 `refresh_token`。
**验收**：测试账号端到端建出 1 个 app + 拿到 user_access_token；文档写明 refresh_token 有无 + **二选一决策**：`native`(→Task 6a) 或 `wrapper`(→Task 6b)。**第 2 天不通 → 选 wrapper**。
**依赖**：无。**产出**：bootstrap 分支决策（供 T6）。

### Task 2: crypto 包（AES-256-GCM）
**Files**：Create `internal/pkg/crypto/aesgcm.go` + `aesgcm_test.go`；Modify config 加载读 `security.thirdparty_token_key`。
**Interfaces 产出**：`crypto.Encrypt(plain []byte)([]byte,error)`、`crypto.Decrypt(cipher []byte)([]byte,error)`（nonce 前置）；`crypto.MustInit(keyB64 string)`（缺失/非32字节→fatal）。
**步骤(TDD)**：① test：encrypt→decrypt 往返一致、错误密钥解密失败、空密钥 init panic/error；② run FAIL；③ 实现；④ run PASS；⑤ commit。
**验收**：往返测试 PASS；密钥缺失 fail-fast；**`MustInit` 在 server 启动链（biz 初始化处）被调用，缺 key→启动失败**（含启动 wiring，不留隐藏责任）。**依赖**：无。

### Task 3: model + migration + store
**Files**：Create `internal/pkg/model/user_third_party_account.go`；`migrations/<ts>_create_user_third_party_account.sql`；`internal/numind/store/third_party_account.go` + test；**Modify `internal/numind/store/store.go`（`IStore` 加 `ThirdPartyAccounts() IThirdPartyAccountStore` + datastore 实现）+ 重新 `mockgen` 生成 `mock_store.go`**。
**Interfaces 产出**：`IThirdPartyAccountStore{ Get(ctx,userID,provider)(*m,error); Upsert(ctx,*m)error; Delete(ctx,userID,provider)error; UpdateTokens(ctx,userID,provider,accessEnc,refreshEnc []byte,exp *time.Time)error }`；model 字段见 design §3（token 列 `[]byte`，`TokenExpiresAt *time.Time`，唯一 `uniq_user_provider`）。
**消费**：crypto（store 边界加解密）。
**步骤(TDD)**：用 in-memory sqlite（`newTestDB`）测 Upsert 幂等（同 user+provider 二次=更新）、Get 解密、Delete。
**验收**：Upsert 幂等 + 加密往返测试 PASS；migration 可 apply。**依赖**：T2。

### Task 4: config + errno
**Files**：Modify `config_local/dev/qa.yaml` 加 `security.thirdparty_token_key`、`security.feishu_state_key`（两个独立密钥，prod 运维另注）；Create `internal/pkg/errno/feishu.go`。
**Interfaces 产出**：`errno.ErrLarkNotConnected/ErrLarkReauthRequired/ErrLarkCallFailed/ErrLarkStateInvalid`。
**验收**：errno 编译可用；config 字段被读取。**依赖**：无。

### Task 5: state 签验 + Redis 一次性 nonce
**Files**：Create `internal/numind/biz/feishu/state.go` + test。
**Interfaces 产出**：`SignState(payload)(string,error)`（HMAC-SHA256 用 `feishu_state_key`，nonce 写 Redis TTL=exp）；`VerifyState(state)(*Payload,error)`（验 HMAC+exp+nonce 存在并删除；payload={user_id,run_id,step,question_text,nonce,exp}）。
**消费**：redis、config。
**步骤(TDD)**：签→验往返；篡改 HMAC 失败；过期失败；**nonce 二次验证失败（防重放）**。
**验收**：重放测试 PASS。**依赖**：T4。

### Task 6: provisioning + OAuth 换 token（分支二选一，按 T1 决策）
> **Spike 已定（2026-06-24）**：建 app = 有数服务器跑 lark-cli 的 device-code 流（实测得 `open.feishu.cn/page/cli?user_code=...`），**无 ISV**。原「6a 裸 SDK 原生建 app」作废（飞书无此 API）；可选优化=将来参考 lark-cli MIT 源码在 Go 原生重实现 device-code 流，S4 先用 lark-cli 跑通。
**实现** `internal/numind/biz/feishu/provisioner.go`：封装「调 lark-cli `config init --new --name <userid>`(独立 profile/config-home per user) → 解析输出取 `open.feishu.cn/page/cli` URL → 轮询直到建好 → 读该 profile 的 appId/appSecret」+ OAuth 换 token。
**Interfaces 产出**：`Provisioner{ StartProvision(ctx,userID)(pageURL string,sessionRef string,error); PollCredentials(ctx,sessionRef)(appID string,appSecretEnc []byte,done bool,error); ExchangeCode(ctx,appID,code)(access,refresh []byte, exp *time.Time, scopes string, error) }`。
**消费**：crypto、os/exec(lark-cli)、httpclient。
**验收**：dev 用测试账号端到端跑通（拿到 page URL→建 app→取 appId/secret→换 token），refresh_token 有无确认。**依赖**：T2、T5。

### Task 7: OAuth 端点 + controller + router
**Files**：Create `internal/numind/controller/v1/feishu/feishu.go`；`internal/numind/biz/feishu/service.go`；Modify `router.go`；**Modify `internal/numind/biz/biz.go`（`IBiz` 加 `FeishuSvc() feishu.IFeishuService` + 初始化 wiring，注入 store/state/provisioner/StudentRunService 以便 callback 内部调 `biz.Answer`）**。
**端点**（契约见 design §5）：`POST /v1/feishu/connect`、`GET /v1/feishu/oauth/callback`(无JWT)、`GET /v1/feishu/status`、`DELETE /v1/feishu/connection`。
**关键**：callback 验 state→ExchangeCode→crypto 加密 Upsert→**内部直调 `biz.Answer(runID,{state.question_text:{free_text:"已授权"}})`**→302 到前端；callback **幂等**（run 不在 waiting→直接 302 success）。
**消费**：T3 store、T5 state、T6 provisioner、`biz.Answer`(answer.go)。
**步骤(TDD)**：service 层单测（status/connect/unbind）；callback handler 测 state 无效→ErrLarkStateInvalid、幂等重复回调不报错。
**验收**：4 端点注册且 service 单测 PASS；callback 恢复路径单测（mock provisioner+Answer）PASS；**跨用户防护单测：state.user_id ≠ run.UserID → 302 error（design §5）**。**依赖**：T3、T5、T6。

### Task 8: yield 扩展（PauseType/AuthURL + auth 恢复键）
**Files**：Modify `internal/numind/biz/agent/yield_error.go`（`YieldPayload` 加 `PauseType string`、`AuthURL string`）；**Modify `internal/numind/biz/agent/stream/events.go`（`QuestionPromptPayload` 同步加 `PauseType`/`AuthURL`）**；**Modify `internal/numind/biz/agent/runner_stream.go`（`persistAndEmitYield` 把 `p.PauseType`/`p.AuthURL` 填进 SSE payload）**。
**关键（P0×2）**：
- 恢复键：auth 类 yield 的 `Questions` **必须含恰好一条**固定提示文，`state.question_text=Questions[0].Question`，callback 据此构造 Answer key（design §6）。
- **SSE 传播**：仅改后端 struct 不够——非流式路径整体序列化进 `pending_question_json`（前端从此读，OK），但**流式 SSE 路径 `QuestionPromptPayload` 不含新字段则前端 T13 永远收不到 `pause_type`**，必须同步改 stream payload + emit。
**步骤(TDD)**：测 auth yield payload 序列化含 PauseType/AuthURL；测 SSE question_prompt 事件含 pause_type/auth_url；测带固定 Question 的 auth yield 经 `biz.Answer(question_text)` 能恢复（复用现有 answer 夹具）。
**验收**：auth 暂停→恢复 单测 PASS；**SSE 事件含 pause_type/auth_url 单测 PASS**；未新增任何 TerminalReason/LoopEvent（编译期数组不变）。**依赖**：T7（恢复路径）。

### Task 9: 飞书 API client（per-user token + 刷新）
**Files**：Create `internal/numind/biz/feishu/client.go` + test。
**Interfaces 产出**：`Client.For(ctx,userID)(*larkClient,error)`（取 token；过期且有 refresh→加锁刷新→UpdateTokens；无/失败→`ErrLarkReauthRequired`）。底层 oapi-sdk-go，**不走 aiservice**。
**消费**：T3 store、oapi-sdk-go、redis(刷新锁)。
**步骤(TDD)**：mock store，测过期触发刷新、并发只刷一次、无 refresh_token→ErrLarkReauthRequired。
**验收**：刷新+锁单测 PASS；刷新锁 key 含 userID（`feishu:refresh:<userID>`，不跨用户共享）。**依赖**：T3、T4（ErrLarkReauthRequired）。

### Task 10: 3 个飞书工具
**Files**：Create `internal/numind/biz/agent/tool_lark_create_doc.go`、`tool_lark_send_message.go`、`tool_lark_read_bitable.go`（+ tests）；Modify `factory_platform.go`（注册）。
**关键**：继承 `BaseTool`；`Execute` 取 user→`Client.For`→调 API→**失败一律 returnSoftError**（含 ErrLarkReauthRequired→提示重连，未连接→提示连接）；记 langfuse span `lark.<tool>`（不记 token）。scope：docx:document / im:message / bitable:app:readonly。
**依赖注入**：lark 工具从 `f.ds.ThirdPartyAccounts()`（T3 扩展的 IStore 方法）懒构建 `feishu.Client`，**`NewPlatformToolFactory` 签名不变**（feishu client 不作新参数，借已注入的 ds）。
**步骤(TDD)**：mock client，测正常输出、ErrLarkReauthRequired→软错误（不杀 run）、未连接→软错误。
**验收**：3 工具注册且软错误测试 PASS；factory LoadTools 数量+3。**依赖**：T9。

### Task 11: 前端 API + store
**Files**：Create `numind-web-v3/src/api/feishu.ts`、`src/stores/feishu.ts`(setup 语法)。
**Interfaces 产出**：`connectFeishu()`、`getFeishuStatus()`、`disconnectFeishu()`（走 `request.ts`）；store `{status, scopes, loading, fetchStatus, connect, disconnect}`。
**验收**：`npm run type-check && npm run lint` 通过。**依赖**：T7（契约）。

### Task 12: 前端连接 UI
**Files**：Create `numind-web-v3/src/components/feishu/FeishuConnection.vue`；Modify 设置页（如 `src/views/SettingsView.vue`）挂载该组件；接 store。
**关键**：异步 4 状态（loading/empty+CTA「连接飞书」/error 重连/success+解绑）；销毁性「解绑」走 ConfirmModal。
**验收**：4 状态可渲染；解绑有确认弹窗；type-check+lint 过。**依赖**：T11。

### Task 13: 前端 agent 授权暂停卡片
**Files**：Modify agent 对话暂停渲染，识别 `pause_type=auth` → 授权卡片（URL 可复制+二维码），用户完成后轮询 run 状态自动续显。
**验收**：pause_type=auth 渲染授权卡片（gstack 验）；pause_type=question 仍走原问答 UI（不回归）；恢复轮询参数明确（如 3s 间隔 / 上限 2min，注释说明）。**依赖**：T11、T8（SSE 须已带 pause_type 字段，否则本 task 验收无法通过）。

### Task 14: S5 验证策略（规则 10）
**验证方式**：gstack 浏览器端到端（登录→连接飞书→建应用→授权→agent 写出一篇 docx）+ 关键路径 Go 单测（crypto 往返 / state 签验重放 / token 刷新锁）做持久回归。
**理由**：连接流是跨浏览器多跳交互，gstack 适合端到端；但 gstack 不留回归代码，故安全敏感件（加密/state/刷新）补 Go 单测。
**关键路径**：连接发起→建应用暂停→恢复→授权暂停→callback 恢复→工具写 docx 成功；解绑后再用触发重连。
**前置环境**：飞书测试账号（浏览器内预登录）；飞书后台 callback URL 白名单；**T1 Spike 的测试 app_id/app_secret 须存进 `spike-bootstrap.md`，S5 前确认有效**；dev 注入 `feishu_state_key`+`thirdparty_token_key`。
**⚠️ gstack 跨域跳转可行性**：E2E 含浏览器被 302 到 `feishu.com` 授权页再 302 回 callback——gstack 能否跟随并在飞书页完成需先评估。**降级方案**：若 gstack 无法跨这一跳，前半段（连接发起→建应用→恢复）单独 E2E，后半段用 `mock callback`（直接请求 `/oauth/callback?code=test&state=<合法签名>`）验证 callback→恢复→工具写 docx。
**验收**：本 task 由 S3 gate 独立 reviewer 一并审查验证策略合理性。**依赖**：全部。

---

## 自检
- **spec 覆盖**：design §3→T3，§4→T2，§5→T7，§6→T8，§7→T9，§8→T10，§9→T10(span)，§10→T11-13，§11→T1，§12→全覆盖。✅
- **多仓库**：后端 T1-10 先于前端 T11-13；前端 T11 引用 T7 契约。✅
- **AI 可观测性**：T10 含 span（非 generation，符合 design §9）。✅
- **依赖无环**：T1→T6；T2→T3→{T7,T9,T10}；T4→T5→T7；T6→T7→T8；T9→T10；T11→{T12,T13}；T8→T13。无环。✅
- **规则 10**：T14 = S5 验证策略 task。✅

**total_tasks: 14**
