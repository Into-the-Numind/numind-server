# ADR 0002：Agent 按需读取官方技能并受控执行 lark-cli

- 日期：2026-07-13
- 状态：Accepted
- 决策人：Michael
- 阶段：S2

## 背景

初版提案延续了已有 `lark_create_doc`、`lark_read_bitable` 等固定工具的思路。这能完成少数操作，但没有真正复刻 Claude Code/Codex 使用 lark-cli 的方式：模型应能读取官方技能说明，理解飞书资源和命令，并自行组合多步操作。

另一方面，有数在服务端托管多用户的长期飞书凭据，不能像本地 CLI 一样把任意 shell 或任意 lark-cli 命令交给模型。

## 决策

1. 现有有数 Agent 按需读取与固定 lark-cli 发行版一致的 `lark-shared`、`lark-doc`、`lark-base`、`lark-wiki` 官方技能；不复制长期维护的静态技能副本。对应域执行前必须有绑定当前 run 和 CLI 版本的签名技能读取 receipt。
2. Agent 通过统一 `lark_execute` 提交结构化 argv，自行选择 Docs/Base/Wiki 的允许命令和多步顺序。
3. 服务端 CommandCatalog 根据命令路径和参数计算 exact scopes、风险和副作用语义；Agent 不能声明或覆盖这些安全属性。
4. 执行器只允许 user identity、Docs/Base/Wiki 创建/读取/更新；拒绝 shell、raw API、IM、auth/config、删除、权限管理和未注册命令。
5. ConnectionOrchestrator 独占 app 创建和 OAuth 命令，FeishuOperationService 负责持久化、租约、错误分类和原 argv 精确重放。
6. 固定 lark-cli 1.0.68，二进制、技能、命令目录和 contract tests 同版本升级。
7. 正常操作不前置 `auth status`；直接执行真实命令，只按确定的结构化错误进入 app scope 或 user scope 恢复。

## 理由

- 保留 LLM 对业务意图、资源和命令组合的判断能力，用户体验更接近 Claude Code/Codex。
- 官方技能与 CLI 同版本，减少有数复制文档后过期的问题。
- 受控 argv 和服务端命令目录能满足多租户长期凭据场景的安全要求，又不把 Agent 降级为固定按钮编排。
- operation 级持久化让授权后只重放未完成的那一步，避免模型重生成参数或重复创建资源。

## 后果

- 旧三个飞书工具不再是目标架构，尤其 `lark_send_message` 必须从 Agent 能力目录移除。
- 需要增加技能读取器、命令目录、参数策略、加密 HOME vault 和外部工具结果恢复机制。
- lark-cli 升级变成受控发布事项，不能自动浮动。
- Base create 的官方 shortcut 在 1.0.68 声明了 table delete scope；产品必须如实说明，执行器仍禁止删除命令。
