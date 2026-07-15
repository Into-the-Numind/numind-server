# ADR 0005：CommandCatalog 只登记 lark-cli 1.0.68 的真实 record 路径

- 日期：2026-07-13
- 状态：Accepted
- 决策人：Michael / AI implementation review
- 阶段：S4 Task 4

## 背景

S2 设计与 S3 实施计划把 Base 记录写入概括成 `record create/upsert/update`，scope manifest 进一步写成了 `base +record-create` 与 `base +record-update`。Task 4 开始前用固定二进制 `lark-cli --version` 和逐命令 `--help` 复核后确认：1.0.68 没有这两个 shortcut，实际提供的是 `+record-batch-create`、`+record-upsert`、`+record-batch-update`。

若 Catalog 原样登记不存在的路径，策略测试会通过，但运行时必然返回 unknown command；若在执行层悄悄翻译虚拟路径，则版本 manifest 不再代表真实 CLI 攻击面。

## 决策

1. Catalog 只允许 1.0.68 真实存在的 record 路径：读取 `+record-get/list/search`，写入 `+record-batch-create/upsert/batch-update`。
2. `+record-batch-create` 映射 `base:record:create`；`+record-upsert` 映射 `base:record:create` 与 `base:record:update`；`+record-batch-update` 映射 `base:record:update`。
3. 单条创建使用不带 `--record-id` 的 `+record-upsert`；单条更新使用带 `--record-id` 的同一命令。首版保持 shortcut 级 exact scope，不在 Task 4 内另造 raw API adapter。
4. 1–20 条批量写入是普通写风险；21 条以上且不超过 CLI 单次 200 条上限进入通用高风险确认；超过 200 条拒绝。删除与无条件全表清空继续永久拒绝。
5. 1.0.68 版本 manifest 快照使用真实路径。后续 CLI 升级若新增稳定单条 shortcut，必须通过快照差异与 ADR 再决定是否替换。

## 理由

- 安全目录必须同时是能力目录；允许一个不存在的命令既不能服务用户，也无法真实约束执行面。
- 保持 argv 与官方 CLI 一致，便于审计、版本锁定、scope 说明和故障复现。
- 批量阈值属于有数的产品风险策略，不应与 CLI 的 200 条技术上限混为一谈。

## 后果

- Agent 技能与工具 schema 要展示真实 record shortcut，不生成 `+record-create` / `+record-update`。
- Task 7 的确认状态机要为 21–200 条批量写入和其他高风险参数在确认后生成可执行 argv；模型不能自行传 `--yes`。
- Task 21/23 需要用固定 1.0.68 二进制对 version manifest 与真实 help 做最终一致性 Gate。
