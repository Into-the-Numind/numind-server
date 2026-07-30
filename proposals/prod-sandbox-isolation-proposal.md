# Prod 同机 Sandbox 隔离 — 提案

## §1 方案概述 [客户可见]

在现有 Prod 云服务器上新增一套 Sandbox 专用 Rootless Docker。它由普通 Linux 用户运行，与当前管理 MySQL、Redis、正式 API 和前端的 Docker“总开关”完全分开。

正式 API 不直接连接任何 Docker socket，而是连接一个单用途 Sandbox 管家 `sandboxd`。这个管家只接受创建、执行、复制文件和销毁固定规格 Sandbox 的请求，拒绝宿主机目录挂载、开网络、特权容器、设备和额外权限。因此 API 即使被攻破，也不能直接发送任意 Docker 指令。

首发保留 1 个待命 Sandbox，standby 与 active 总容器数不超过 5，最多同时运行 5 个任务。512MB 是每个任务的使用上限，不是预先占用；五个轻任务可以并行。POC 使用 workload 2.25GiB、control 384MiB和总父级 2.75GiB ceiling；Prod 正式值必须根据至少 7 天历史监控或 72 小时新采样下调，并始终给核心业务保留至少 1.25GiB。资源紧张时优先拒绝或结束 Sandbox 任务，保护登录、SOP、聊天、积分、MySQL 和 Redis。

## §2 报价与周期 [客户可见]

- 预估工作量：6–9 个工程日，另加 1 个 Dev 产品验收日
- 报价：内部上线工程，不单独报价
- 交付时间线：S1 提案确认后进入 S2 详细设计；本地验证和 Dev 验收通过后，再申请 Prod 发布窗口

## §3 技术可行性 [AI 内部]

### 现有功能复用

- 复用 `internal/numind/biz/sandbox` 的 Pool、Docker CLI client、Seccomp、非 root、cap-drop、只读 rootfs、tmpfs、执行超时和审计逻辑。
- 复用 `sandbox-skill:skills-v1.5.3`；Dev 当前镜像约 685MB，5 个待命容器实测约 0.6MB/个。
- 复用 `DockerClient` 抽象，新增 `sandboxd` RPC client；正式 API 不再持有 Docker CLI 或 Docker socket。
- 复用部署机、TCR 和现有 Prod 容器替换/健康检查链路。
- 复用 `NUMIND_SANDBOX_BACKEND`、`NUMIND_SANDBOX_SKILLS_ENABLED` 等运行时开关作为快速回滚入口。
- 新增宿主机 `sandboxd` 单例，统一持有全局 lease、并发槽位、压力准入、orphan 对账和受限 Docker 参数。

### 生产机可行性实测

- OpenCloudOS 9.4、Linux 6.6、x86_64。
- 8 核 CPU、7.4GiB 内存、当前约 3.6GiB 可用、无 Swap。
- XFS 根盘约 34GiB 可用。
- Docker 29.1.3、systemd 255、cgroup v2。
- 已有 Rootless Docker 工具、rootlesskit、`newuidmap/newgidmap` 和 fuse-overlayfs。
- 当前缺少 slirp4netns；需要在实施阶段补齐并验证。
- Prod 无 AppArmor；实施必须允许受支持的 Seccomp + Rootless 基线，不把 AppArmor 写成不可满足的硬前置。

### 技术风险

1. **五个任务同时逼近 512MB。**
   - 512MB 是单任务上限，不是预留量。
   - POC workload `MemoryMax` ceiling为2.25GiB；control `MemoryHigh=256MiB`、`MemoryMax=384MiB`；父级另留128MiB，总 ceiling 2.75GiB。
   - Prod 正式父级上限取 `min(2.75GiB, 多日 MemAvailable P1 - 1.25GiB)`，workload使用剩余预算；并发槽位仍是5。
   - 所有阈值从实际 workload max派生：high=`min(2.0GiB, 90%)`、恢复=80%、主动回收=96%，不能继续使用高于正式上限的POC固定值。
   - broker 每 2 秒读取宿主机和 cgroup 指标，连续 3 次超过阈值后暂停准入或 best-effort 回收任务；不承诺内核 OOM victim 顺序。
2. **Rootless socket 权限配置错误。**
   - API 只访问 broker socket；Rootless Docker socket永不挂载进 API。
   - broker socket 使用专用 GID、mode 0660 和 Unix peer credentials；部署测试断言两套 Docker socket均不可访问。
3. **Rootless 与现有 Seccomp/AppArmor 参数不兼容。**
   - 在同形态环境验证每项 Docker run 参数。
   - Prod AppArmor 设为空；Seccomp、cap-drop、no-new-privileges、非 root、只读和断网仍为硬门槛。
4. **Rootless data-root 写满 Prod 根盘。**
   - 使用 `/opt/numind-sandbox/storage.img` 预分配 8GiB ext4 loopback，挂载到专用 data-root；mount失败时拒绝启动，不能退化到根盘目录。
   - 70% 告警，85% 停止新任务；发布前校验 bytes、inodes 与新镜像解压峰值。
5. **Rootless daemon 故障导致正式 API 启动失败。**
   - Pool warm-up 保持异步；daemon 不可用时 Sandbox 工具软失败，核心 API 继续启动。
6. **同机共享内核仍有残余风险。**
   - Rootless 降低宿主机权限，父级 cgroup 限制资源；不宣称达到独立服务器/VM 的物理隔离强度。
7. **当前 Pool 缺少全局并发上限。**
   - broker 单例维护 `active_task_max=5`、`total_container_max=5`、FIFO 等待、取消、lease 心跳和重启 label 对账；滚动部署两个 API 实例仍共享全局 5。
8. **完整 Docker socket 是可绕过应用限制的高权限控制面。**
   - 原始 socket只给 broker；broker 使用固定镜像 digest和服务端固定参数，拒绝 bind mount、device、privileged、host namespace、网络、额外 capability和任意 cgroup parent。
9. **Rootless cgroup 拓扑在 OpenCloudOS 上未实证。**
   - S2 前置阻断式 POC：验证 systemd delegation、实际容器 cgroup 路径及 memory/cpu/pids/io controller；不满足则禁止上线。
10. **回滚中断在途任务。**
   - 回滚先 drain 最多 300 秒，再审计取消；验证积分 Reserve/Reconcile、COS 上传幂等和 orphan session 收口后才能移除 broker socket并停止 daemon。
   - 发布独立、幂等的 `numind-sandbox-reconcile` one-shot；API 不健康时也能依据持久 lease journal和审计表完成取消、积分对账与上传状态收口。
11. **大文件/慢连接绕过五任务限制拖垮 broker。**
   - RPC metadata 64KiB；Exec输出 4MiB；CopyIn 10文件/100MiB，CopyOut 10文件/200MiB且单文件50MiB；64KiB流式缓冲、全局4条 Copy stream、每 lease每方向1条、32连接和100MiB/s聚合限速。
12. **broker 重启丢失动态 lease 状态。**
   - 使用 SQLite WAL + FULL sync lease journal；启动时只与Rootless容器 label有界对账，60秒内恢复或进入持久补偿队列。broker不持有Prod数据库凭据；API恢复路径或独立reconcile命令再对账审计、积分与上传状态。
13. **control plane被 workload饿死。**
   - control slice设256/384MiB软硬内存限制并使用高于 workload的 CPU/IO weight；父级另留128MiB headroom。

### 涉及仓库

- [x] numind-server
- [ ] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性（如功能涉及 LLM 调用）

- [x] 涉及 LLM 调用：间接涉及。Sandbox 工具由现有 Agent LLM 工具调用触发，本功能不新增 LLM API 调用。
- Trace 起点：复用现有 Agent run trace。
- Generation 点：N/A；不新增 generation。
- 关键元数据：`agent_run_id`、`user_id`、`sandbox_session_id`、`lease_id`、`container_id`、`borrow_wait_ms`、`active_count`、`queue_depth`、`active_task_max`、`memory_pressure_state`、`cgroup_events`、`termination_reason`。

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事

- 作为正式客户，我需要在 SOP 和 AI 智能体中运行 Dev 已有的代码、插件技能和文件生成功能，以便在正式工作中完成 PPT、Word、Excel、PDF 和数据处理任务。
- 作为正式客户，我需要 Sandbox 繁忙或故障时得到明确提示，而不是让整个聊天、SOP 或登录不可用。
- 作为产品负责人，我需要 Sandbox 与正式业务容器和客户数据隔离，避免上线新功能时破坏 Prod 原有用户资料、积分和记录。
- 作为运维人员，我需要看到并发、等待、拒绝、OOM、超时、内存和磁盘指标，并能只关闭 Sandbox 而不回滚用户数据。

### 验收标准

- [ ] 正式 API 镜像不需要 Docker CLI，也不包含 Docker daemon。
- [ ] 正式 API 不挂载主 Docker socket或 Rootless Docker socket，不加入任何 Docker group。
- [ ] 正式 API 只挂载 `sandboxd` broker Unix socket；直接访问两套 Docker socket均失败。
- [ ] broker只允许固定镜像 digest与固定安全参数；恶意 bind mount、network、device、privileged、host namespace、额外 capability和任意 cgroup parent请求全部被拒绝。
- [ ] Rootless 用户读取 Prod secrets、MySQL/Redis 目录、证书、用户上传目录和主 Docker data-root 均失败。
- [ ] Sandbox 容器保持非 root、cap-drop ALL、no-new-privileges、只读 rootfs、Seccomp、network none。
- [ ] 单任务保持 512MB、1 CPU、64 PIDs、30 秒命令、300 秒会话和 50MB 单文件限制。
- [ ] `pool_min=1`、`active_task_max=5`、`total_container_max=5`；standby 计入总容器上限，active=5 时不补 standby。
- [ ] 第 6 个任务进入全局 FIFO，等待最多 30 秒；客户端取消后立即移出，不泄漏 slot。
- [ ] 两个 API 实例滚动部署期间仍共享全局 5 个任务槽位。
- [ ] 五个轻任务可同时进入执行；任务槽位不会因为 512MB ceiling 被预占。
- [ ] POC workload hard ceiling为2.25GiB；Prod workload high=`min(2.0GiB, 90%×实际max)`、恢复=80%、主动回收=96%；control为256MiB soft/384MiB hard；父级另留128MiB，ceiling为2.75GiB、4 CPU、576 Tasks。
- [ ] Prod memory.max有至少7天历史监控或72小时新采样依据，按保留1.25GiB核心业务余量的公式计算并记录。
- [ ] 同形态 POC 证明 daemon、broker和每个 Sandbox 的实际 cgroup路径及 memory/cpu/pids/io限制生效。
- [ ] broker连续3次采样发现 workload达到实际90% high或宿主机可用内存 <1.5GiB时停止新任务；workload低于实际80%且宿主机恢复后按迟滞窗口重新开放。
- [ ] broker连续3次采样发现 workload达到实际96% shed时best-effort回收；宿主机可用内存 <1.0GiB则单次立即回收。
- [ ] 宿主机可用内存 <1.0GiB时单次采样立即关闭准入并回收，不等待连续窗口。
- [ ] Rootless Docker data-root使用预分配 8GiB ext4 loopback；挂载失败拒绝 daemon启动；70% 告警、85% 停止接新任务。
- [ ] 同时运行 PPT、PDF、Excel、Python 和轻脚本时，正式 API、登录、SOP、聊天、积分、MySQL 和 Redis保持正常，宿主机不发生 OOM。
- [ ] 压测时核心 API p95不超过测试前 15 分钟基线 2 倍、业务 smoke错误率 <1%、`/healthz` 100% 成功、MySQL/Redis不重启。
- [ ] 停止 Rootless daemon或 broker 后，正式 API 继续健康，Sandbox 工具给出可理解的降级提示。
- [ ] socket ACL、恶意 broker请求、cgroup OOM、data-root ENOSPC、API/broker重启、滚动部署、等待取消和回滚排空均有负向测试证据。
- [ ] 回滚演练证明在途任务状态收口、积分 Reserve/Reconcile正确、COS上传可重试且无悬挂 running session。
- [ ] API停止时独立 `numind-sandbox-reconcile` 仍能幂等完成 lease、积分和上传状态收口。
- [ ] broker对RPC body、Exec输出、Copy文件数/字节数、连接数、流数和速率的限制全部有超限/取消/慢连接测试。
- [ ] broker重启通过持久lease journal在60秒内完成恢复或补偿队列收口，不会无限期fail-closed。
- [ ] backend/skills 开关可以在不修改数据库和用户数据的情况下关闭 Sandbox。
- [ ] 本地测试、Go 全量测试、lint、Dev 同形态部署与人工五并发验收全部通过。
- [ ] Prod 部署仍须产品负责人单独明确授权。

### 边界情况

- 5 个任务全部短时冲高到 512MB：workload总电闸优先，允许至少一个 Sandbox 失败，不允许宿主机 OOM。
- 第 6 个任务进入时前 5 个即将结束：broker FIFO应公平接棒，不能忙等、泄漏 goroutine或重复授予 slot。
- broker/API重启或滚动部署：按 lease label和心跳对账；未知状态 fail-closed，不能形成每实例各 5 个槽位。
- broker重启动态阶段恢复：journal为事实源，stale heartbeat超过30秒后销毁并补偿；对账最多60秒。
- Rootless daemon 重启：启动清理只能清理专用 daemon 中的 Sandbox orphan，不能访问主 daemon。
- broker socket不存在或权限错误：API 启动成功、Pool 为空、Sandbox 工具软失败并报警。
- API尝试直接访问 Rootless socket或发送恶意 broker参数：稳定拒绝并审计。
- 8GiB data-root 达到 85%：不再拉起新任务；清理不得删除当前或回滚镜像。
- data-root mount失败：Rootless daemon拒绝启动，不能把普通目录当成 data-root继续写根盘。
- Seccomp 文件、技能目录或镜像缺失：不得以降低安全参数的方式继续运行。
- COS 上传已开始时触发内存压力：先给 `output_persisting` 10 秒排空；仍需回收时必须保证任务、积分和上传状态可恢复。
- CopyIn/CopyOut慢速客户端、超大文件、超过10文件或连接中断：broker保持常量级缓冲并及时释放lease/stream计数。

### 权限规则

- 产品内 Sandbox 使用权限沿用现有 Agent/Skill/Permission Pipeline，不新增用户等级。
- 宿主机仅 `numind-sandbox` 用户运行 Rootless daemon和 broker。
- 正式 API 仅获得 `numind-sandbox-api` group访问 broker socket的权限。
- 管理端、用户前端、MySQL、Redis 和其他容器均不得获得 broker socket或任何 Docker socket。
- Sandbox 容器内用户固定为非 root，不能自行添加 capability、网络或宿主机挂载。

### UI 行为规格

- 页面位置：沿用首页 SOP、AI 智能体和插件技能现有入口，不新增页面。
- 布局要求：不改布局。
- 交互模式：前 5 个任务正常执行；第 6 个等待最多 30 秒。
- loading：沿用现有工具执行进度。
- empty：N/A。
- error：容量不足显示“当前文件处理任务较多，请稍后重试”；运行时不可用显示“文件处理服务暂时不可用，其他功能不受影响”。
- success：沿用现有 Sandbox 产物卡片和下载流程。
