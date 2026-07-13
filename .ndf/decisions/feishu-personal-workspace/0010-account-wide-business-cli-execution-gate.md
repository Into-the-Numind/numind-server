# ADR 0010：账号级数据库租约串行化飞书业务 CLI

- 日期：2026-07-13
- 状态：Accepted
- 决策人：Michael / AI implementation review
- 阶段：S4 Task 7 final safety review

## 背景

同 run 的空资源 create 可以作为一次 overwrite 免确认依据。仅在 proof 预占时检查中间写仍有 TOCTOU：进程 O 预占 proof 后暂停，另一进程 A 可以完成 append，随后 O 继续 overwrite 并覆盖 A 的结果。进程内 mutex 无法覆盖多实例，也无法保护共享的加密 CLI HOME 与 Vault CAS。

## 决策

1. 新增 `feishu_operation_execution_gate`，每个用户一行，以 `generation + lease_owner + operation_id + lease_until` 表示跨进程账号级业务执行租约。所有 connected 状态下会真实启动 lark-cli 的 read/write/high-with-proof 操作都必须持有 gate；连接恢复和普通 high-risk confirmation 不占 gate。
2. gate claim 的固定数据库锁序为 `user_third_party_account(lark) -> feishu_operation_execution_gate`。proof 预占继续使用 `account -> source operation -> consumer operation -> proof consumption`。gate claim 事务提交后才 claim operation，不在持有数据库行锁时进入 operation transition、Vault 或 CLI，禁止反向锁序。
3. Service 必须先拿 gate，再 claim operation 并转为 `executing`。等待 gate 被取消或超时时，operation 保持原状态且没有 operation lease。gate 持有范围覆盖 proof usable recheck、Vault、runner、最多一次 read retry，以及 terminal/waiting transition；最终使用 detached、owner+generation CAS 释放。
4. `IsOperationProofUsable` 在 gate 内重验 proof ledger tuple 与 source，并拒绝 source 之后除 consumer 自身外任何状态的 `docs +update`。若 O 先持 gate，之后创建的 A 只能在 O 完成后执行；若 A 的 operation 已先创建，O 的 recheck 立即退回 confirmation。
5. active lease 无论属于当前还是旧 generation 都阻止其他 owner。账号 generation 变化不会使仍可能运行的旧 CLI 自动失效；新 generation 只能等待 owner 释放或 lease expiry。旧 generation 自身因 active account generation 校验不能续租。
6. gate lease 为 120 秒。Controlled runner 单次硬上限 30 秒，read 最多两次，另预留 30 秒给 Vault 与终态提交；等待总上限 125 秒且服从调用方 context。进程崩溃或 detached release 失败时依赖 expiry 恢复。
7. 本任务不实现 confirmation 后的实际执行，也不追加 CLI `--yes`；该边界仍归 Task 13。

## 后果

- 同一账号不同 operation 会串行执行，换取明确的写顺序、proof 安全和 CLI HOME/Vault 一致性；不同用户仍可并行。
- operation row 可以在 gate 外幂等创建，但不得在 gate 前 claim/transition 为 `executing`。
- Task 13 的解绑/generation bump 必须尊重 active gate：先协调取消/等待，或等待 lease 到期，不能覆盖未过期的旧 generation gate。
- Task 21 需在 MySQL 8 验证 `SELECT ... FOR UPDATE`、lease expiry 与多实例顺序；SQLite 单连接测试只验证状态不变量。
