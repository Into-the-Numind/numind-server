# 全 Agent 平台工具策略设计

## 1. 目标架构

```text
AgentDefinition.tool_flags（兼容数据，不授权）
                    │
                    ▼
Run / RunStream / Resume 请求（兼容字段）
                    │
                    ▼
AgentToolRegistry.ListAllTools
                    │
                    ├─ IsEnabled(FullyEnabledToolConfig()) = true  → 注册给模型
                    └─ false（document_generate）                  → 排除
                    │
                    ▼
工具执行时继续经过 permission pipeline + 工具自身权限 + 沙箱/授权/计费
```

## 2. 工具选择契约

`selectToolsForRun` 成为唯一选择函数。它保留旧参数以避免扩大调用方改动，但不再使用 `allowedNames` 或 `enforceAllowlist` 过滤。Run 和 RunStream 已共用该函数，因此两条主路径同时收口。

`applyDefinitionToolPolicy` 改为兼容性归一化：明确关闭 `EnforceToolAllowlist`，不再让定义覆盖调用方工具列表。`enforceExplicitToolAllowlist` 固定返回 false，避免 Create/Resume 路径继续携带严格授权语义。

`toolNamesFromFlags` 暂时保留原有兼容解析结果，但它不再是授权真相源；真正是否注册只由 Registry 与 `IsEnabled` 决定。这样 Create/Answer/外部恢复等历史调用路径即使仍构造 ToolNames，也不能缩减工具集合，后续可无迁移删除这些兼容字段。

## 3. 权限契约

`validators.ToolFlag` 改为总是 passthrough，并注明字段已废弃为运行时授权。验证器仍保留在链中，避免装配和指标变化；租户、父子账户、规则、确认和工具自身 CheckPermissions 不变。

## 4. 技能契约

磁盘 `skills.Registry` 的生产根目录只允许四个平台技能。系统提示的统一技能目录由“当前 Agent 已绑定 DB 技能 + 全部磁盘平台技能”构成：

- 所有 Agent 均能加载四个平台技能。
- DB/市场技能只有在绑定后才进入当前 Agent 的 `SkillByName`。
- 同名时维持现有 DB-bound 优先规则。
- 未绑定 DB 技能没有查询入口，不因本次全工具策略泄露。

## 5. 官方 Agent 清单

清单列出平台注册的 25 个外部工具和 3 个兼容分类键。所有工具为 true，只有 `document_generate` 为 false；`code_sandbox`、`media`、`dangerous` 为 true。内置 `read_tool_artifact` 由 CompactV2 在运行时注入，不进入清单。

## 6. 测试策略

1. RED：旧定义全 false 时，选择器仍返回全部可用工具且排除硬禁用工具。
2. RED：`applyDefinitionToolPolicy` 不再启用严格名单。
3. RED：ToolFlag 对直接 false 和分类 false 均透传。
4. RED：三 Agent 清单全部开放且 `document_generate=false`。
5. RED：磁盘注册表恰好包含四个默认平台技能；未绑定 DB 技能仍不可从 `SkillByName` 解析。
6. GREEN 后运行 Agent、permission、skill 定向测试、race、全量测试和 lint。

## 7. 风险与缓解

- 风险：`bash_exec` 等工具对更多 Agent 可见。缓解：Docker 沙箱、权限钩子和危险操作策略仍是执行边界；本变更不修改它们。
- 风险：旧 UI 仍显示开关但不生效。缓解：本次按用户要求不增加前端功能；保留字段兼容，后续可单独删除或改文案。
- 风险：未来新增工具自动开放。该行为是明确产品要求；工具作者必须通过 `IsEnabled` 硬禁用未就绪能力，并为权限/计费写测试。
