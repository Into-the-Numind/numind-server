# ADR 0008：版本化技能 receipt 证明主技能送达，任务引用按需读取

- 日期：2026-07-13
- 状态：Accepted
- 决策人：Michael / AI implementation review
- 阶段：S4 Task 6

## 背景

Agent 需要读取与服务端固定 lark-cli 完全同版本的官方 Docs/Base/Wiki 技能，但不能读取任意服务器文件，也不能用读完一个短 reference 冒充已经读完主技能。Wiki 节点内容实际通过 Docs 命令读写，因此只验证 Wiki 或 Docs 任一技能都不完整。

评审还发现三个可靠性边界：主文件为空时仍可能签 receipt；非规范 Base64URL 文本可能解码成同一 MAC；大量并发技能读取复用业务 runner 时会制造进程风暴和偶发测试失败。

## 决策

1. 只允许 `lark-shared`、`lark-doc`、`lark-base`、`lark-wiki`，并通过固定绝对路径的 lark-cli 1.0.68 精确执行 `skills read ... --json`。不传用户 HOME，不使用 shell，不从网络或服务器文件系统解析技能。
2. reference 必须是主 `SKILL.md` 直接声明且位于 `references/` 命名空间的规范相对路径。`scripts/`、`assets/`、跨技能、绝对路径、遍历和规范化变化全部在启动 CLI 前拒绝。
3. 单页最多 32 KiB 且不切断 UTF-8。Cursor HMAC 绑定 run、skill、reference、offset、expiry 与完整内容摘要；每页重新读取固定资源，内容漂移、跳页、篡改或过期均 fail closed。
4. 只有非空主 `SKILL.md` 的最后一页签 skill receipt；主 envelope 还必须含 1.0.68 真实契约中的非空 guidance。reference 永不签 skill receipt，防止短文件绕过主技能阅读 Gate。
5. Receipt 只证明主技能完整分页已送达，绑定 run、skill、CLI version、expiry 和内容摘要；它不声称 Agent 已读完所有 task-specific references。不同 create/fetch/update/格式路径所需引用由官方主技能指令驱动 Agent 按需读取，并在 Task 17/21 的 Agent 集成 Gate 验证。
6. Domain Gate 固定为：Docs=`shared+doc`，Base=`shared+base`，Wiki 命令=`shared+wiki`，Wiki 内容=`shared+wiki+doc`。Task 7 必须为 Wiki 内容选择复合 domain。
7. Receipt/cursor key 从现有 `security.thirdparty_token_key` 经独立 HMAC label 派生；token payload 与 signature 必须是唯一规范 Raw Base64URL，拒绝 CR/LF、未使用位别名、空段和额外字段。
8. 所有 SkillReader 实例共享最多 4 个技能 CLI 进程的 context-aware semaphore；取消等待不能启动进程，所有退出路径释放槽位。技能读取是低频控制面，优先稳定性而非无界并发。

## 理由

- 主技能 receipt 是防止 Agent 在完全未知官方契约时直接执行的确定性门，不是对模型“理解了所有任务分支”的证明。
- 将所有 references 静态强制为 receipt 会让每个简单操作读取大量无关说明；若未来要确定性证明 task-specific reference，必须先把 action/reference 纳入 receipt/verifier 契约，不能用短 reference 覆盖 skill receipt。
- 固定版本、内容摘要和规范 token 让跨实例验证可审计，且不会把用户凭据写入 receipt。

## 后果

- Task 7 仅依赖 `VerifyRequired` 小接口，不反向依赖 Agent 包；Wiki 内容路径必须传 `wiki_content`。
- Task 17 的 `lark_skill_read` 工具需把 references/cursor 原样暴露给 Agent；Task 21 必须验证 Agent 按官方主文继续读取与具体动作匹配的 reference。
- Task 12 在注册服务前仍必须先执行 runner 版本 Gate；Task 23 继续承担进程级 sandbox/UID 加固。
