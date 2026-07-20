# S4 Task 5：三份 AgentDefinition 契约

- 日期：2026-07-20
- RED：`TestThreeAgentDefinitionContract` 因版本化 manifest/runtime/final prompt 文件不存在而失败。
- SSOT：继续锁定用户原始 Prompt 文件 SHA-256 `fc2bea1b8e05ddd285975120d0b7b401a56ed69683f90a63a4fa30f907dc66f5`；测试按三个一级标题从该文件提取业务段，不用最终产物反证自身。
- 合成：每份最终 Prompt 等于 runtime contract + 固定分隔符 + 对应 SSOT 业务段。Agent 2 零字节改动；Agent 1 只补“可借鉴部分/不可照搬部分”两行；Agent 3 只补“选择原因”一行，且 patch anchor 必须恰好出现一次。
- 配置：manifest 不含组织、用户、Agent 或飞书环境标识；三份 Prompt 分别约 15.7/15.9/17.0 KiB。每个 Agent 对平台当前 25 个工具及 3 个类别键均显式 true/false，只有其最小必需集合开启。
- 持久化：隔离 SQLite 中通过现有 `skill.Service.Create/Patch/Get/ListHistory` 回读名称、Prompt、flags 和两个版本快照；其他 parent 的 list/get/patch 均不可见。
- Gate：Prompt/manifest、平台 metadata 闭集、controller Create/Patch/history、Skill + Agent 整包、`task lint` 与 diff hygiene 通过。
