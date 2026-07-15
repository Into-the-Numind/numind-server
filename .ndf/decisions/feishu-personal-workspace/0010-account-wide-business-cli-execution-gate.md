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
3. Service 必须先拿 gate，再 claim operation 并转为 `executing`。等待 gate 被取消或超时时，operation 保持原状态且没有 operation lease。claim 后立即启动 execution guard；guard 以 30 秒 heartbeat 续租，持有范围覆盖 proof usable recheck、Vault、runner、最多一次 read retry，以及 terminal/waiting transition。结束时必须先 cancel 并 join heartbeat，再使用 detached、owner+generation CAS 释放，禁止 release 后仍有续租 goroutine。
4. `IsOperationProofUsable` 在 gate 内重验 proof ledger tuple 与 source。不同服务实例的应用时钟不能作为执行顺序真相源，因此同一 `user + generation + agent_run_id` 只要存在 consumer 自身之外任何状态的 `docs +update`，proof reservation 和 recheck 都必须保守退回 confirmation；同一幂等 consumer 的重放仍读取已绑定结果，不重复消费 proof。
5. active lease 无论属于当前还是旧 generation 都阻止其他 owner。claim 的 active same-owner 复用和 renew 都必须匹配同一个 `generation + owner + operation_id`，不得把 lease 从 operation A 改写到 B。renew 固定使用 `account -> gate` 行锁，并以 `user_id + generation + owner + operation_id + lease_until > now` 做严格 CAS；已过期 lease 即使尚未被别人接管也不得复活。账号 generation 变化不会使仍可能运行的旧 CLI 自动失效；新 generation 只能等待 owner 释放或 lease expiry。旧 generation 自身因 active account generation 校验不能续租。
6. gate lease 为 120 秒。每次 Vault 解包完成、真实 `runner.Run` 开始前必须同步 renew/verify；read retry 的第二次 invocation 也必须重新 renew。renew false 或错误会取消 execution context，尚未开始的 runner 不得启动，已开始的 runner 必须收到取消。runner 由 OperationService 再施加 30 秒硬 deadline，严格短于已续租的 120 秒窗口；等待 gate 总上限 125 秒且服从调用方 context。进程崩溃、heartbeat 失败或 detached release 失败时依赖 expiry 恢复。
7. gate 表使用 `DATETIME(3)`。claim 将 `now` 向下截断到毫秒，确保到期前最后 1ms 不会提前接管；renew 将 `now` 向上、`lease_until` 向下对齐到毫秒，宁可最多提前 1ms 判定续租失败，也不能让 Go 纳秒比较与 MySQL 条件更新分歧后误报续租成功。
8. 本任务不实现 confirmation 后的实际执行，也不追加 CLI `--yes`；该边界仍归 Task 13。

## 后果

- 同一账号不同 operation 会串行执行，换取明确的写顺序、proof 安全和 CLI HOME/Vault 一致性；不同用户仍可并行。
- operation row 可以在 gate 外幂等创建，但不得在 gate 前 claim/transition 为 `executing`。
- execution guard 的 context 同时传给 Vault 与 runner。失租发生在 callback/runner 阶段会阻止后续 seal；若失租恰好发生在 Vault 已完成 context 检查并开始 pack 之后，pack/CAS 可能结束，但不会再产生外部 CLI 副作用，且 snapshot revision CAS 会拒绝并发旧版本覆盖。
- Task 13 的解绑/generation bump 必须尊重 active gate：先协调取消/等待，或等待 lease 到期，不能覆盖未过期的旧 generation gate。
- Task 21 需在 MySQL 8 验证 `SELECT ... FOR UPDATE`、lease expiry 与多实例顺序；SQLite 单连接测试只验证状态不变量。
