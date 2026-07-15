# ADR 0004：Vault 使用版本化 keyring，执行期 generation fencing 归 operation/lifecycle

- 日期：2026-07-13
- 状态：Accepted
- 决策人：Michael / AI implementation review
- 阶段：S4 Task 2

## 背景

首版 Vault 将 `key_version` 放入 AAD，却始终用当前单一 Cipher 解密。主密钥轮换后，旧快照会永久不可读。质量审查还指出：callback 执行期间发生解绑时，单次 generation 预检不能阻止已经开始的远端副作用；同 UID 的主动恶意进程也能制造路径式文件操作竞态。

后两项不能通过在 Vault 内重复增加一套锁或更多路径复检正确解决。计划架构已经把业务执行租约放在 OperationService，把解绑等待/unknown 收口放在 lifecycle service；固定 hash 的官方 CLI 和非 shell runner 是首版的本地执行信任边界。

## 决策

1. Vault 冻结版本到 Cipher 的 keyring；按快照 `key_version` 选择历史 Cipher 解密，所有新写入使用 current version/Cipher。缺失历史 key 时 fail closed。
2. 单 key 构造器保留为兼容 wrapper；key version 限制为 1–32 字节 `[A-Za-z0-9._-]`。
3. Vault 提供 startup-only 残留清理，只处理 runtime base 直属 `lark-home-*`；Task 12 必须在发布 service/worker 前调用，失败则飞书能力不启动。
4. Vault 保留 callback 前 current-generation 校验与写回时 Task 1 account-row/CAS 校验，不跨 callback 持长事务。
5. 执行期间的 generation fencing 由 Task 7 operation lease 与 Task 13 unbind 状态机共同完成，并在 Task 21 以真实并发交错验证。
6. 静态 tar symlink/path traversal 已在 Vault fail closed。同 UID 主动 symlink/root rename 属 defense-in-depth P2：Task 3 验证 runner 取消不留子进程，Task 23 明确评估是否需要独立 UID/sandbox；在未实现前不得宣称能抵御恶意本地进程。

## 理由

- keyring 让轮换时旧数据可读、变更时自然迁移到 current key，且 key 不进入数据库。
- 远端副作用的执行租约必须与 operation 状态和解绑事务共享；Vault 自造第二套锁会产生双锁、长事务和死锁风险。
- 同 UID 主动重命名无法靠 `defer RemoveAll(path)` 或额外 Lstat 根治，真正边界是执行身份/sandbox，而非更多表面路径检查。

## 后果

- Task 12 的配置/组合必须同时提供 current key 和仍处于解密窗口内的历史 key；移除旧 key 前必须完成重封或确认无旧快照。
- Task 7、13、21 增加 generation bump 与 executing write 的 barrier 测试。
- Task 3/23 保留本地执行隔离 P2；固定 CLI、无 shell、进程取消与启动残留清理是首版最低 Gate。
