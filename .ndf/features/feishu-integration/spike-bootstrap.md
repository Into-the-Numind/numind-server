# T1 Phase 0 Spike — bootstrap 结论（已实测）

> 目标：能否程序化①建开放平台自建应用 ②走 OAuth 拿 user_access_token。
> 方法：lark-cli 二进制分析 + **实际运行 `lark-cli config init --new`**（关键证据）。

## ✅ 结论：可行，无需 ISV

实跑 `lark-cli config init --new` 输出（铁证）：
```
打开以下链接配置应用:
  https://open.feishu.cn/page/cli?user_code=2AF7-MFWU&lpv=1.0.56&from=cli
等待配置应用...
```
这是一个**标准 device-code 流程**（同电视登 Netflix）：lark-cli 跟飞书要一次性 `user_code` → 飞书返回**公开官网**链接 `open.feishu.cn/page/cli` → 用户浏览器打开建 app → lark-cli 轮询取凭据。

**链接指向飞书公开开放平台（open.feishu.cn），非私有后门。任何跑 lark-cli 的人都拿到此链接** —— Claude Code / Codex / Hermes 等全部 agent 就是这么做的，有数照做即可。**无需 ISV。**

## 路线确定：有数服务器跑 lark-cli（W），无 ISV
- 有数后端 per-user 调 lark-cli（`config init --new --name <userid>`）→ 拿到 `open.feishu.cn/page/cli?user_code=...` → 显示在有数网页 → 用户建 app → lark-cli 轮询取得 appId/appSecret → 有数加密入库。
- 与 Claude Code 唯一区别：lark-cli 跑在**有数服务器**（而非用户本机）。正常集成方式。
- lark-cli 是飞书官方 MIT 工具、且其 help 明确为「AI agent」设计（device-code/`--no-wait` 都是为 agent 准备）→ agent 驱动是**官方预期用法**，ToS 风险低（仍建议正式发布前扫一眼 ToS）。

## 纠正之前的错误判断（重要）
- ❌ 我曾说 `larksuite_cli_app/probe`「特权专属、第三方不可复用、故必须 ISV」——**错**。实测证明那只是 device-code 流的轮询端点，走的是公开 `open.feishu.cn/page/cli`，任何人跑 lark-cli 都能用。**ISV 结论作废，三选二/trilemma 作废。**
- ✅ 仍成立的事实：`apps +create`（→spark）是飞书 aPaaS 应用引擎平台，**不是**建自建应用，别用错；`oapi-sdk-go` 无「建自建应用」REST 方法（所以才用 lark-cli 的 device-code 流，而非裸 SDK 调用）。

## refresh_token（待 dev 实测）
飞书 v2 OAuth (`/open-apis/authen/v2/oauth/token`) 通常返回 refresh_token；本 spike 未完成浏览器步骤故未实换，dev 阶段真换一次确认。

## 对计划的影响
- **T6 定为：有数服务器调 lark-cli 跑 device-code 建 app + 取凭据**（原 6b「wrapper」即此，确认为主路径；6a「裸 SDK 原生建 app」作废=飞书无此 API）。
- 可选优化（非必须、不影响 ISV 结论）：将来若想不依赖 lark-cli 进程，可参考其 MIT 源码在 Go 里原生重实现这套 device-code 流；S4 先用 lark-cli 跑通。
- OAuth 换 token / token 存储 / 工具（T5/T7/T9/T10）不变，原生。
- 测试 app：本 spike 未真实建 app（机制已坐实）；dev 阶段用测试账号实建一个验证端到端 + 确认 refresh_token。
