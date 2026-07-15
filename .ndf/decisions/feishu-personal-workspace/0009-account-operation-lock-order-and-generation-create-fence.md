# ADR 0009：账户到操作的固定锁序与创建代际栅栏

- 日期：2026-07-13
- 状态：Accepted
- 决策人：Michael / AI implementation review
- 阶段：S4 Task 7 dual review

## 背景

操作 claim 和 vault 写入已经通过账户当前 generation 防止旧工作区复活，但原有
`CreateOrGetOperation` 会先插入 operation，再依赖后续 claim 做 generation 检查。
当断开或重连并发推进 generation 时，旧请求仍可能留下永远不应存在的 operation
行。Task 13 还需要在断开连接时推进 generation；如果创建和推进采用相反的加锁
顺序，会把安全栅栏变成数据库死锁来源。

SQLite 不实现与 MySQL 等价的行级 `SELECT ... FOR UPDATE`。单元测试可以确定性验证
锁内 generation 重验和“拒绝时不插行”，但不能证明生产数据库的并发锁行为。

## 决策

1. 所有同时涉及飞书账户 generation 与 operation 的事务，固定使用
   **account → operation** 的锁序。
2. `CreateOrGetOperation` 必须在事务中的第一项业务读取上，以
   `(user_id, provider='lark')` 对账户行执行 `FOR UPDATE`。
3. 在该锁内重新验证账户存在、provider 为 `lark` 且 generation 与候选 operation
   完全一致；验证失败返回 not-found，并且不得插入或返回 operation。
4. 验证通过后，才允许插入 operation 或按 `(user_id, idempotency_key)` 返回已有行。
5. Task 13 的 generation bump 与 pending-operation 取消必须复用相同的
   account → operation 锁序；不得先锁 operation 再锁账户。
6. Task 21 增加 MySQL 集成 Gate，使用两个真实事务交错覆盖 create 与 generation
   bump，证明旧 generation 不插行、无逆序死锁。SQLite 测试只承担确定性语义测试，
   不作为行锁并发证明。

## 后果

- 操作创建本身成为 generation-fenced，而不是把拒绝推迟到 claim 阶段。
- 同一账户的操作创建会被账户行短暂串行化；这是换取断开/重连安全边界的可接受成本。
- Task 13 实现前必须先引用本 ADR；Task 21 未通过 MySQL Gate 前，不宣称 SQLite race
  测试验证了生产行锁语义。
