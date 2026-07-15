# ADR 0007：授权恢复必须绑定当前命令与真实无副作用证据

- 日期：2026-07-13
- 状态：Accepted
- 决策人：Michael / AI implementation review
- 阶段：S4 Task 5

## 背景

Task 5 首轮实现把 CLI 返回的缺失权限与全部 Docs/Base/Wiki Catalog 权限并集比较。这样一条 Docs 命令可能接受 Base 或同域其他命令的权限并触发恢复，违反最小权限。首轮实现也把固定合成的 app-scope、refresh-token tuple 当作“已证明原写请求没有副作用”，会让 Task 7 自动重放缺少真实证据的已启动写操作。

真实 1.0.68 探针同时确认 `identity` 位于 CLI envelope 顶层；runner 若只保留内层 error 字段会丢失分类所需的用户身份。

## 决策

1. `ClassifyEnvelope` / `Classify` 必须接收服务端 `NormalizedCommand.Scopes`。该集合必须精确匹配 Catalog 中某个命令的完整 scope 集，不能由模型声明或拼接。
2. CLI 返回的 `missing_scopes` 必须是本次命令 scopes 的非空子集；跨命令、跨域、未知、空白、NUL 或重复值全部 fail closed。不得通过排序或去重把异常输入变成可信证据。
3. 首版只有真实观测且可证明在远端请求前失败的三类结构可让已启动写操作恢复：Docs create 的 user-level `missing_scope`、`config/not_configured`、`authentication/token_missing`。
4. 合成的 app-scope、refresh-token invalid/expired/revoked 等 tuple 可为读操作或尚未启动的写操作提供确定性恢复，但已启动 write/high-risk 必须进入 `unknown`，禁止自动重放，直到真实版本化证据入库。
5. 写操作遇到 timeout、5xx、瞬时网络错误、损坏或未知输出一律 `unknown`；读操作只对 `context` deadline 或明确 `Timeout()/Temporary()` 为真的网络错误有限重试。永久 `net.Error` fail closed。
6. runner 保留并验证 CLI envelope 顶层 `identity`；内外 identity 冲突 fail closed。错误文案、hint、details 和 stderr 永不参与授权分类。

## 理由

- 恢复权限必须来自当前规范化操作，而不是产品支持能力的全局并集。
- “错误名称看起来发生在授权阶段”不等于“远端写请求一定没有执行”；自动重放需要真实、版本化且可审计的证据。
- 对重复 scope 静默去重会改变外部证据，甚至把异常响应提升成 source-proven，必须拒绝而不是修复。

## 后果

- Task 7 必须把 `NormalizedCommand.Scopes` 原样传入分类器，并只在 `ProvenNoSideEffect=true` 时恢复已启动写操作。
- Task 21/24 的真实租户 Gate 若观测到稳定的新 tuple，可新增脱敏 fixture、先写 contract test，再显式扩大 source-proven 集合。
- 不能证明结果的写操作会向用户报告 `unknown` 并要求核对飞书结果，不会自动执行第二次。
