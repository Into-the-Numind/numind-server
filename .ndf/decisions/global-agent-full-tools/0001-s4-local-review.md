# S4 本地双轮审查

日期：2026-07-20

## 第一轮：运行路径一致性

- `Run` 与 `RunStream` 共用 `selectToolsForRun`，均只按 Registry + `FullyEnabledToolConfig` 选择。
- Create、Answer、外部工具 Resume 和流式 Resume 虽保留 `ToolNames`/`EnforceToolAllowlist` 兼容字段，但 `enforceExplicitToolAllowlist` 永远 false，且最终选择器忽略这两个参数。
- AgentDefinition 重载后 `applyDefinitionToolPolicy` 明确清除旧严格标记。
- 旧工作流测试已从“仅脚本所需工具可见”改为“完整注册表可见”，业务步骤和产物断言不变。

结论：P0=0，P1=0。修复 1 个 P2：流式指标测试仍断言 `rogue_tool` 被旧开关过滤，已同步为全局策略。

## 第二轮：安全与技能边界

- `document_generate.IsEnabled` 仍恒为 false，选择器测试和三 Agent 清单测试双重锁定。
- ToolFlag 仅停止 AgentDefinition 层的工具开关拒绝；PlatformHardRule、TenantAdminRule、UserSessionRule、WorkingDir、LLM classifier、工具自身 CheckPermissions、飞书授权、沙箱、计费和限流未改。
- 生产 `skills/` Registry 契约恰好锁定四个内置技能。
- 数据库/市场技能仍只从当前 Agent binding 构造 `SkillByName`，`load_skill` 没有查询未绑定技能的数据库入口。
- 新工具自动开放是明确产品语义；未就绪工具必须由自身 `IsEnabled=false` 硬禁用。

结论：P0=0，P1=0。修复 2 个 P2：移除 ToolFlag 已废弃 store 状态，并更新权限装配/上下文的过时注释。

## S4 结果

PASS。进入 S5 全量验证。
