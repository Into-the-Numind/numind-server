# ADR 0001：飞书连接改为 Agent 主导、平台加护栏

- 日期：2026-07-18
- 状态：accepted

## 决策
采用 Agent-led runtime：Agent 负责业务命令选择和结构化错误后的判断；平台负责身份、catalog、scope preflight、授权、确认、幂等和 unknown-write fence。

## 原因
Dev Docs update 的固定版本真实证据显示，合法业务命令因缺少写 scope 返回了 CLI 可理解但旧 classifier 未登记完整的形状。逐错误补丁会持续制造可用性缺口；直接把该 started write 标记为可重放又会削弱 unknown-write 安全。

固定 `auth check --scope ... --json` 在同一真实用户 HOME 上已证明能在业务写入前给出 granted/missing 的机器合同。以 catalog scope 做统一 preflight 可以同时解决 Docs/Base/Wiki 写授权，并保留严格幂等。

## 否决方案
- 裸 `lark-cli`：不满足多租户凭证和业务边界。
- 继续逐错误编排：不能形成可扩展的 Agent 闭环。
- 看到 code-less missing_scope 就重放 started write：副作用证据不足，质量审查 P1 阻塞。

## 后果
- write/high-risk 多一次只读 scope check。
- Agent 获得安全结构化失败和只读 inspect 能力。
- 未知写入仍停止，不以体验为由放宽。
