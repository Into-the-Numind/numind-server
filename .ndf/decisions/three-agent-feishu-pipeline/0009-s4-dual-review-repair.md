# S4 双审修复

- 日期：2026-07-20
- 首轮结论：Spec FAIL（P0=0/P1=4/P2=2）；Quality/Security FAIL（P0=4/P1=4/P2=3）。
- 文件读取边界：生产 `file_read` 只接受当前用户拥有的 HTTPS 腾讯 COS 对象；URL 归属只解析 path，拒绝 query/fragment 伪造、非 COS、userinfo、重定向和非 2xx HEAD，并为 HEAD 设置 context/30 秒超时。所有模型可见软错误改为固定错误码，OCR Langfuse 只留来源类型、格式、字节数、文字字节数和词数。
- 工具授权：Run/RunStream 共用单一选择器。旧 category-only AgentDefinition 保留历史 full-open；存在直接工具键时，`ToolNames` 成为服务端强制 allowlist，首轮、流式、回答恢复和飞书外部恢复均透传同一策略。
- unknown 写入：发生 `unknown_result` 后仍永久阻断后续写操作，但允许 command catalog 证明为 RiskRead 的查询核对业务键/受管标记；reconciliation read 不重置或消耗写重试状态。
- 完整内容：Python parser 在 JSON 解码后独立强制 20 MiB 正文上限，stdout envelope 最终按 JSON 单字节最坏 6 倍转义加 1 MiB 余量设为约 121 MiB；截断显式失败。`file_read` 安全 span 从 preflight 开始，SSRF/归属/签名/HEAD 拒绝也可观测且不记录 URL。
- XHS 快照：`snapshot_total` 只在第一页计算并写入签名 cursor，续页沿用同一值；cursor 使用域隔离 HMAC 防篡改。工作流覆盖 243 条、40 条检查点和 203 条续跑。
- 飞书工作流契约：所有场景加载正式 Agent Prompt、正式工具 schema 和 command catalog；Agent 1 完成记录强制 34 字段，Agent 2/3 写入前完整读取受管目标并严格校验正式可见标记，Agent 3 用真实九字段及固定枚举验证来源准入、蓝 V、0-1 主语及不足 70 条规则；普通歧义使用真实 `ask_user_question`，飞书授权使用真实 external-action 暂停/恢复链路。
- 明确延后：`file_read` 后续页仍会重新解析来源；这是性能优化而非正确性/安全边界，当前用 read token 保证内容一致性，后续可增加按 user/run/token 隔离的有界缓存。
