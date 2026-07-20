# 全 Agent 默认开放平台工具

日期：2026-07-20

## 背景

当前 AgentDefinition 的 `tool_flags` 同时承担前端配置、运行时工具筛选和权限拒绝三种职责。三个官方 Agent 因此被固化为不同的最小工具集，用户新建 Agent 也可能因为历史开关缺少 `bash_exec`、`file_read`、`load_skill` 等能力。

## 用户结果

1. 现有 Agent、三个官方 Agent以及以后用户新建的 Agent，默认获得平台当前全部可用工具。
2. `document_generate` 仍不开放；它是不可执行的硬禁用占位工具。
3. `xlsx-author`、`docx-author`、`pptx-author`、`pdf-from-html` 四个内置平台技能默认对所有 Agent 可发现、可加载。
4. 租户自建技能和市场技能不做全局开放，仍需绑定到当前 Agent，且保持租户隔离。
5. 工具开放不绕过现有用户数据归属、飞书 OAuth/能力授权、沙箱、危险操作确认、积分计费和并发限制。

## 范围

- 统一非流式、流式、外部工具恢复和回答恢复路径的工具选择语义。
- 将 `agent_definition.tool_flags` 降级为兼容字段，不再作为工具可用性或权限拒绝依据。
- 更新三个官方 Agent 清单，使声明与平台默认策略一致。
- 增加回归测试，证明旧 Agent 的显式 `false`、分类开关和调用方传入的窄名单都不能缩减平台注册工具。
- 增加技能注册表契约测试，锁定恰好四个全局内置技能。

## 不在范围

- 不删除数据库字段或前端字段。
- 不新增 Agent 工具配置 UI。
- 不开放 `document_generate`。
- 不放宽任何工具内部的数据权限或外部系统授权。
- 不把未绑定的租户/市场技能暴露给 Agent。

## 验收标准

- AC1：任意 AgentDefinition 的 `tool_flags` 缺失、损坏、全 false 或部分 false 时，Run 与 RunStream 都注册所有 `IsEnabled(FullyEnabledToolConfig()) == true` 的工具。
- AC2：`document_generate` 永远不进入模型工具列表。
- AC3：权限流水线不再因 AgentDefinition 的工具开关拒绝工具；其他验证器行为不变。
- AC4：四个内置技能出现在所有 Agent 的统一技能目录并可由 `load_skill` 加载。
- AC5：只有绑定到当前 Agent 的数据库技能进入目录和 `SkillByName`，未绑定技能不可加载。
- AC6：三个官方 Agent 清单对全部当前工具声明 `true`，唯独 `document_generate=false`，分类兼容键为 `true`。
- AC7：完整 Go 测试、race 定向测试和 `task lint` 通过，Dev 部署健康。
