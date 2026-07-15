# ADR 0003：持久化更新绑定租户代际并实施租约与 Vault fencing

- 日期：2026-07-13
- 状态：Accepted
- 决策人：Michael / AI implementation review
- 阶段：S4 Task 1

## 背景

S3 计划中的首版 store 接口只给 claim/transition 传入全局 UUID 与 worker owner。实现虽然会检查记录对应某个当前 generation，但无法在同一条 UPDATE 中证明它属于调用方预期的用户和代际；先读取归属再更新会形成 TOCTOU。初版实现还允许租约过期但尚未被新 worker 重领的旧 owner 提交状态，并允许 transition 的通用 fields 改写受保护列。

Vault 的 revision CAS 也必须与账号 generation 变化串行，否则解绑删除旧 Vault 后，旧 worker 可能再次插入旧 generation 快照并占住每用户主键。

## 决策

1. `ClaimSession`、`UpdateSessionState`、`ClaimOperation`、`TransitionOperation` 显式接收调用方 `userID + generation`，并在同一原子 UPDATE 的 WHERE 中绑定 `id + user_id + generation`。
2. 状态提交除匹配 lease owner 外，还必须满足 `lease_until > now`；过期 worker 即使尚未被重领也不能收口。
3. `TransitionOperation` 只允许更新 attempt、时间、结构化错误和受控结果字段；主键、租户、generation、租约、请求与幂等字段一律拒绝。
4. `PutVaultCAS` 在事务内先 `SELECT ... FOR UPDATE` 锁定 `(user_id, provider='lark')` 的账号行，核对当前 generation 后再执行 revision create/update。
5. 用户主键及所有关联 `user_id` 明确使用 `BIGINT UNSIGNED`；Vault 用户主键禁止 AutoIncrement。Forward migration 显式升级既有连接表列，local rollback 对称恢复并警告 UINT32 溢出风险。
6. SQLite 测试覆盖逻辑不变量；真实 MySQL 8 的 schema 与 `FOR UPDATE` 并发语义在 Task 21 作为集成 Gate 验证。

## 理由

- 多租户隔离和 generation 失效必须由单条数据库写条件保证，不能依赖调用顺序或 UUID 难猜。
- owner 字符串不是 fencing token；至少用未过期租约条件阻止旧 worker 提交。
- 通用 map 更新若不设白名单，会绕过所有结构化状态机约束。
- 账号行是解绑和 Vault 写入共同的串行化点，能阻止旧代际快照复活。

## 后果

- 后续 operation/auth service 必须传递经过当前账号解析的 userID、generation 和显式 now。
- 解绑事务必须沿用账号行作为 generation 变化的锁顺序，保持 account → vault，禁止引入反向持锁路径。
- Task 21 必须在 MySQL 8 而非仅 SQLite 上验证 migration/AutoMigrate 一致性及 generation bump 与 Vault CAS 并发。
