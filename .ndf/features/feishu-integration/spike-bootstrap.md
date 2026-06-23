# T1 Phase 0 Spike — bootstrap 结论

> 目标：能否程序化①建开放平台自建应用 ②走 OAuth 拿 user_access_token。
> 方法：lark-cli 二进制 strings + 命令 help + SDK 符号分析（未真实建 app——证据已足以定论）。

## 核心发现（纠正 S0/S1 的错误假设）

### ❌ `apps +create` 不是「建自建应用」
`lark-cli apps +create --app-type html|full_stack` 是飞书 **aPaaS「应用引擎」平台**（建托管 web app，子命令含 `+html-publish`/`+db-execute`/`+release-create`/`+chat`）。其端点 `/open-apis/spark/v1/apps` 属于该平台，**不产生 appId/appSecret、不给 OpenAPI scope**。S0/S1 把它当成「建自建应用」是错的。

### ❌ 公开 API 不能建自建应用
`oapi-sdk-go/v3/service/application/v6` 仅 `application.Get` / `applicationCollaborators.Get` / `applicationAppVersion.Get` / `appBadge.Set`——**无 Create**。开放平台「自建应用」无程序化创建 API；scope 也只能在开发者后台（浏览器 console）开通（证据串：`enable it in the developer console (see console_url)`）。

### ⚠️ lark-cli 的建应用是「特权专属」
config init 用 `/open-apis/application/v6/**larksuite_cli_app**/probe` —— 路径名写死 `larksuite_cli`，是飞书给自家官方 CLI 的私有 provisioning 通道（browser 建应用 → CLI 轮询 probe 取凭据）。**第三方无法复用**。

### ✅ OAuth / token 部分可原生
授权码/device-code 流 + `/open-apis/authen/v2/oauth/token` 是标准公开能力，有数可原生实现。user_access_token ≤6900s。（refresh_token 有无：飞书 v2 OAuth 提供 refresh_token，待真实换一次确认。）

## 结论：R1 = 「原生自动建 app」不可行

「建自建应用」这一步只有三种现实途径，**无法三全（无ISV + 无缝 + 原生）**，最多二选一：

| 途径 | 无需 ISV | 用户 UX | 原生/运维 |
|------|---------|---------|-----------|
| **W. 包 lark-cli**（服务端 per-user 跑 lark-cli 建 app，授权/工具仍原生） | ✅ | ✅ 无缝（只填名字头像，凭据不经用户手） | ❌ 把本地 dev CLI 塞进生产多租户=off-label 脆弱 + **飞书 ToS 待核** |
| **M. 引导手动建**（有数给指引/深链，用户在 console 自建应用、开 scope、把 appId+secret 填回有数；之后原生 OAuth） | ✅ | ❌ 重（C 端创作者要当 5 分钟"开发者"、手碰密钥） | ✅ 干净全原生、无 ToS 问题 |
| **I. 商店应用 ISV**（有数发布一个商店应用，用户只点授权） | ❌ 需审核 | ✅ 最丝滑一键 | ✅ 干净全原生 |

> 即：S1 提案的「方案 C = Spike→Native」中的 native 建 app 分支(6a)**作废**；只剩 W（=原 6b wrapper）/ M / I。OAuth+token+工具部分三种途径都走原生，不受影响。

## 建议（待用户拍板——这是产品/业务决策，非纯技术）
- 若坚持「无 ISV + 无缝 UX」→ **W（包 lark-cli）**，但必须先核飞书 ToS 是否允许 SaaS 包装 lark-cli + 接受运维脆弱性。
- 若可接受「用户当 5 分钟开发者」→ **M**，最稳最干净，但 C 端转化率存疑。
- 若可接受走审核换最佳 UX → **I（ISV）**，回到最初被否的选项，但其实是唯一「无缝+原生+可规模化」的组合。

## 对计划的影响
- T6 的 native 建 app 分支(6a)删除；按用户拍板选 W/M/I。
- OAuth/token/工具（T5/T7/T9/T10）不受影响，继续原生。
- 测试 app：本 Spike 未真实建 app（结论已可定），故无遗留 app 凭据；选 W/M 后在 dev 用测试账号实建。
