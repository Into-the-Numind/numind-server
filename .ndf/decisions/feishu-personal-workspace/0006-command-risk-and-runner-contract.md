# ADR 0006：Catalog 风险元数据与 Runner 输入边界必须分离且闭合

- 日期：2026-07-13
- 状态：Accepted
- 决策人：Michael / AI implementation review
- 阶段：S4 Task 4

## 背景

Task 4 首轮审查发现三类契约缝隙：Catalog 的正文与重复参数上限可能超过 Task 3 runner 的 argv count/bytes，形成“策略允许、运行必失败”；所有 `RiskHigh` 不能统一转换成 CLI `--yes`，因为 1.0.68 只有 `base +field-update` 接受该参数；`ReplaySafeOnAuthError` 若被理解为写操作授权重放结论，会绕过 Task 7 对“未产生副作用”的结构化证明。

同时，版本快照若只把 `1.0.68` 写死在测试文件名而不绑定 `LarkCLIVersion`，升级 runner 常量不会强制 Catalog 审阅。

## 决策

1. Catalog 返回前必须用 Task 3 的同一输入校验验证最终 argv；`RequiresCLIYes` 命令同时对未来末尾追加字面量 `--yes` 的 argv 做预留校验。runner 的 argv count/bytes 作为 version manifest 全局限制，单项内容限制不能替代最终 aggregate Gate。
2. `RiskHigh` 只表示产品需要确认或明确意图，不暗示 CLI 参数。新增 `RequiresCLIYes` 静态元数据，1.0.68 仅 `base +field-update=true`；模型始终不能传 `--yes`。
3. `ReplaySafeOnAuthError` 是无条件 catalog 基线，首版仅读命令为 true。写命令的授权后重放由 Task 7 固定错误分类器逐次证明“无副作用”；timeout/5xx/损坏输出等继续进入 unknown。
4. Catalog 无 operation 上下文时把 Docs overwrite 统一标为 high；同 chain 新建空文档的豁免由 Task 7 用持久化资源证明决定。
5. field-update 因 1.0.68 full PUT 与 CLI 强制 `--yes` 统一标为 high。用户原指令若已明确目标，可作为通用确认策略的意图证据，但类型变化必须明确展示数据风险。
6. 版本快照文件名从 `LarkCLIVersion` 生成，内容同时记录版本、runner 全局限制、命令风险与 `RequiresCLIYes`；只改版本常量必须导致测试失败。

## 理由

- 产品风险、CLI 交互参数和副作用可重放性是三个不同维度，压成一个 high/read-write 布尔值会在 Task 7 产生错误推导。
- Catalog 是 runner 的上游安全边界，不能声明大于下游硬限制的可执行能力。
- 版本绑定让 1.0.68 的命令与参数攻击面在升级时自动进入人工审阅。

## 后果

- Task 7 的确认恢复只为 `RequiresCLIYes` 追加参数；其他 high-risk 命令确认后使用原规范化 argv。
- Task 7/21 必须覆盖写命令 missing_scope 可重放与 unknown 不可重放的结构化错误矩阵。
- Agent 遇到过大的重复 argv 应改用 Catalog 已允许的紧凑 JSON 形态或拆分，不得绕过 runner 上限。
