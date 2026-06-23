# 0001 — ext-token 采用无状态 JWT，v1 不做 DB 吊销

**Date:** 2026-06-24

**Feature:** `xhs-collector`

**Stage:** S4 / T7（ext-token endpoint + scope 最小权限）

**Status:** Accepted（v1 范围内的 deliberate tech debt）

---

## Context（背景）

浏览器插件「一键授权」需要给插件下发一个可长期持有的凭证（design.md §5）。
设计选型上有两条路：

1. **复用现有 web JWT + 加 `scope:"xhs"` claim**（无状态，零持久化）。
2. **新建 OAuth / 持久化 token 表 + 吊销列表**（有状态，可即时吊销）。

约束：

- 客户（莫小派 B2B2C）当前规模小，N=1 机构客户，安全攻击面有限。
- v1 目标是最小切口跑通采集 → 入库 → 富化 → 选题库闭环，授权机制不应成为关键路径上的重活。
- 现有 `biz/user/user.go` 的 `generateWebToken` 已是无状态 HS256 JWT，7 天 TTL，复用成本极低（仅加一个可选 `scope` claim 变体 `signWebToken`）。

## Decision（决策）

采用方案 1：**ext-token = 现有 web user JWT + `scope:"xhs"` claim，TTL 7 天，无状态，v1 不引入任何 DB 吊销机制。**

实现落点：

- `biz/user/user.go`
  - `signWebToken(userID, scope)`：`scope==""` 时签发与既有登录完全一致的 token（不写 `scope` claim，向后兼容旧 token）；`scope!=""` 时额外写入 `"scope"` claim。
  - `IssueScopedToken(ctx, userID, scope)`：校验用户存在后换发 scope token，供 `GET /v1/xhs/ext-token` 端点使用。
- user_token 中间件：对带 `scope=="xhs"` 的 token，仅放行 `/v1/xhs/*` 路由，其它 `/v1/*` 路由 403（最小权限收敛）。

## Consequences（后果 / 已知 tech debt）

**接受的风险（v1 deliberate trade-off）：**

- **注销不失效**：用户在 web 端登出，已下发给插件的 ext-token 不会被主动失效，仍可继续调用 `/v1/xhs/*` 直到 7 天 TTL 自然过期。
- **改密不失效**：用户修改密码，已下发的 ext-token 同样不会被主动失效（无状态 JWT 不查 DB，不感知密码变更）。
- **无即时吊销能力**：一旦 token 泄露，在 7 天窗口内无法主动作废单个 token；只能等 TTL 过期，或整体轮换 `jwt.secret`（代价是使所有用户的所有 token 同时失效，不可接受作为常规手段）。

**缓解因素（为何 v1 可接受）：**

- TTL 仅 7 天，泄露窗口有界。
- `scope:"xhs"` 最小权限：即使泄露，攻击者也只能访问 `/v1/xhs/*`（采集/选题库读写），无法触及 SOP、账号、计费等高敏面。
- xhs 路由全部 user 隔离（`c.GetUint("userID")` + store `WHERE user_id`），泄露 token 只能操作该用户自己的选题数据，无法越权。
- 客户规模小，攻击面与影响有限。

## Follow-up（后续工作，明确推迟到 v1 之后）

如需即时吊销能力，应引入：

1. **持久化 token 记录表**（如 `ext_token`：token_id / user_id / scope / issued_at / expires_at / revoked_at），ext-token 签发时落库。
2. **吊销列表 / 校验钩子**：user_token 中间件对 scope token 增加一次 DB（或缓存）校验，命中 `revoked_at` 即拒。
3. **登出 / 改密联动**：在登出和改密流程中将该用户的所有 ext-token 标记 `revoked_at`。

此 follow-up 已同步登记在 design.md §11「不做 / 边界」与 §12「follow-up tech debt」中。

> 代码侧对应说明见 `internal/numind/biz/user/user.go` 的 `IssueScopedToken` doc comment（本 ADR 为该注释的权威 artifact 来源，满足 NDF §4.4：重大决策必须落 `.ndf/decisions/{feature-id}/` 文件，而非仅存代码注释）。
