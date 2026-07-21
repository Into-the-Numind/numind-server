# S4 双路代码审查

## 结论

PASS。最终复审无剩余 P0/P1，允许进入 S5 全量验证。

## 审查闭环

- Redis 写入改为原子 `XADD + PEXPIRE + PUBLISH`，使用独立 150ms fail-fast command pool；按 run 熔断，客户之间不互相拖慢。
- Redis 故障主动断开受影响订阅；真正 final terminal 异步重试，pause terminal 不重试，避免语义游标后移。
- DB 已真正终态时提供 data-only synthetic terminal；`external_resume_ready` / `ext_resume:*` 继续保持实时订阅。
- 无 cursor 的重建页使用 Redis 中最新 external-pause terminal 作为语义 baseline，注册 live 后从该 cursor 回放，避免 snapshot 到订阅之间丢事件。
- 初始 SSE 在卡片后异常断开仍按最后 cursor attach；cursor 按 run 保存，同标签重建与第二标签均有独立订阅入口。
- 其他用户与未知 run 使用相同 404 安全外观；同 run 多订阅不使用 Consumer Group，不抢消息。

## 回归保护

- 后端覆盖 atomic TTL、replay、fan-out、subscriber cancel、Redis unavailable、pause baseline、terminal retry 替换、持久化终态和 durable continuation 状态。
- 前端覆盖卡片后断线续传、已有 run attach、cursor 保存、continuation 工具 generation、无 delta 的 authoritative assistant_message。
- 客户 Playwright 用真实时间间隔验证卡片后 reasoning、正式文字、工具活动在 terminal 前进入 DOM。
