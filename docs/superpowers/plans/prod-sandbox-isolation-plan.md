# Prod 同机 Sandbox 隔离 — 实施计划

> Feature: `prod-sandbox-isolation`
> NDF stage: S3
> Source of truth:
> `docs/superpowers/specs/2026-07-30-prod-sandbox-isolation-design.md`
> Scope: `numind-server`
> Prod deployment: explicitly excluded until a later product-owner authorization

## 1. 执行原则

- 全部代码只在 `/private/tmp/wt-prod-sandbox-isolation-numind-server` 完成。
- 每个 task 遵循 RED → GREEN → REFACTOR，完成后单独 commit。
- 每个 task 完成时系统必须可编译，相关测试必须通过。
- 不修改 `config_prod.yaml`。
- 不新增 Prod 业务表 migration；lease journal 使用 Sandbox 独立 SQLite。
- Dev 保留现有 `docker` backend作为回退，新增 `broker` backend用于同形态验收。
- 用户 API 不获得 Docker socket；管理 API 不获得 Docker socket或 broker socket。
- 所有任务串行执行，因为后续任务依赖前一任务锁定的协议/实现，不做重叠文件并行写。

## 2. 依赖图

```text
T1 Broker 协议与客户端
├── T3 Broker 服务
└── T6 用户 API wiring

T2 Lease journal
└── T3 Broker 服务

T3 Broker 服务
├── T4 压力、恢复与可观测性
├── T5 二进制与独立 reconcile
└── T6 用户/管理运行角色

T4 + T5 + T6
└── T7 构建、Rootless provisioning 与部署链路

T1..T7
└── T8 安全合同与同形态集成测试
```

该图无环。T1 与 T2 理论上可并行，但都将影响 T3 的核心接口；为避免协议与状态模型
漂移，主 session 串行完成。

## T1. Broker 配置、协议与 Unix client

### 目标

为现有 Sandbox Pool 增加 `broker` backend。先锁定版本化协议、错误合同和客户端，
但不切换任何现有环境。

### 涉及文件

新增：

- `internal/numind/biz/sandbox/broker_protocol.go`
- `internal/numind/biz/sandbox/broker_client.go`
- `internal/numind/biz/sandbox/broker_client_test.go`

修改：

- `internal/numind/biz/sandbox/config.go`
- `internal/numind/biz/sandbox/config_test.go`
- `internal/numind/biz/sandbox/errors.go`

### 实现

1. 新增 `BackendBroker`。
2. 配置新增 socket、metadata、连接、copy、输出和超时上限；默认值与 S2 spec 一致。
3. 定义 `/v1` request/response/error DTO；decoder strict unknown fields。
4. 实现 HTTP-over-Unix client并满足 `DockerClient`：
   - Spawn → create lease
   - Exec → lease exec
   - Destroy → delete lease
   - Inspect → normalized inspect
   - CopyTo/From → bounded stream
   - ExecMkdir → constrained mkdir
   - ListByLabel → owner-scoped lease list
5. container id在 broker模式下为 opaque lease id，禁止依赖 Docker id格式。
6. 映射容量、不可用、策略、OOM、超时、输入/输出过大错误。
7. client取消 request时关闭 body/connection，不泄漏 goroutine。

### RED

- broker配置默认/override测试。
- fake Unix server合同测试。
- 超限、错误映射、取消和stream close测试。
- 证明client request不包含 image/mount/network/privileged等字段。

### 验收

- `go test ./internal/numind/biz/sandbox -count=1`
- `go test ./internal/numind/biz/sandbox -run Broker -race -count=1`
- 现有 docker/disabled backend测试不回归。
- 本 task完成后主服务仍默认原行为，可编译。

## T2. 持久 lease journal 与状态机

### 目标

实现与 Prod 业务数据库完全独立的 SQLite lease事实源、幂等请求和合法状态迁移。

### 涉及文件

新增：

- `internal/numind/sandboxbroker/journal.go`
- `internal/numind/sandboxbroker/journal_test.go`
- `internal/numind/sandboxbroker/lease.go`
- `internal/numind/sandboxbroker/lease_test.go`

### 实现

1. SQLite WAL、FULL synchronous、busy timeout、单进程文件锁。
2. 创建 `lease` 与 append-only `lease_event` schema。
3. 实现状态：
   `queued/creating/ready/active/output_persisting/destroying/terminated/recovery_pending`。
4. 用显式 transition table拒绝非法跳转。
5. `request_id` unique，create/destroy/reconcile可重放。
6. 提供按 owner、state、heartbeat和reconcile状态的有界查询。
7. journal只保存数字业务关联ID，不保存用户内容、文件、prompt或密钥。

### RED

- 所有合法/非法状态迁移table test。
- 同 request id并发创建只产生一个lease。
- crash reopen保留数据与event。
- 两个writer进程/实例争用时第二个fail。
- stale heartbeat和pending查询边界测试。

### 验收

- `go test ./internal/numind/sandboxbroker -run 'Journal|Lease' -race -count=1`
- SQLite文件mode与父目录权限可断言。
- 本 task不依赖Docker，可单独编译和验证。

## T3. sandboxd 核心服务、固定策略与全局五槽位

### 目标

实现只监听 Unix socket 的 broker server，持有 Rootless Docker client，严格执行
固定容器策略和全局 FIFO/五槽位。

### 涉及文件

新增：

- `internal/numind/sandboxbroker/config.go`
- `internal/numind/sandboxbroker/server.go`
- `internal/numind/sandboxbroker/server_test.go`
- `internal/numind/sandboxbroker/auth_linux.go`
- `internal/numind/sandboxbroker/auth_linux_test.go`
- `internal/numind/sandboxbroker/runtime.go`
- `internal/numind/sandboxbroker/runtime_test.go`
- `internal/numind/sandboxbroker/limits.go`
- `internal/numind/sandboxbroker/limits_test.go`

可能修改：

- `internal/numind/biz/sandbox/docker_client.go`
- `internal/numind/biz/sandbox/security.go`

### 实现

1. Unix socket目录/inode owner、mode、symlink检查。
2. accept时读取 `SO_PEERCRED`，仅允许配置的API host UID。
3. 无TCP listener。
4. broker配置验证：
   - 固定TCR image digest，不接受tag
   - 固定Seccomp绝对路径和checksum
   - 固定512MiB/1CPU/64PIDs/30s/300s
   - network none、non-root、cap-drop ALL、no-new-privileges、read-only
   - `/workdir`与`/skills`有界tmpfs
   - 固定cgroup parent
5. create lease使用全局FIFO：
   - pool_min由API管理
   - active最多5
   - ready+active+creating总容器最多5
   - 第6个最多等30秒
   - context取消立即出队
6. Activate/heartbeat/persisting/destroy状态写journal。
7. Exec固定user/workdir/env allowlist，输出总计4MiB。
8. CopyIn/Out使用64KiB buffer，校验路径、文件数、单文件和总字节。
9. tar解包拒绝absolute、`..`、symlink、hardlink、device。
10. response不暴露真实container id、host path或Docker错误细节。

### RED

- fake runtime驱动全部endpoint合同。
- UID allow/deny与socket symlink/mode负向测试。
- 恶意未知字段与所有禁止Docker字段测试。
- 5槽位、FIFO公平、30秒超时、取消、rolling两个owner共享上限。
- Exec/Copy所有spec上限与慢连接测试。
- 固定SpawnConfig逐字段断言。

### 验收

- `go test ./internal/numind/sandboxbroker -run 'Server|Auth|Runtime|Limit' -race -count=1`
- `go test ./internal/numind/biz/sandbox ./internal/numind/sandboxbroker -count=1`
- `go vet ./internal/numind/biz/sandbox/... ./internal/numind/sandboxbroker/...`
- 服务只存在Unix listener，禁止TCP配置。

## T4. 压力准入、磁盘/cgroup readiness、恢复与指标

### 目标

当Sandbox消耗过高时先停止文件任务，保护正式业务；broker重启后在60秒内恢复或进入
持久补偿，不无限卡死。

### 涉及文件

新增：

- `internal/numind/sandboxbroker/pressure.go`
- `internal/numind/sandboxbroker/pressure_test.go`
- `internal/numind/sandboxbroker/readiness.go`
- `internal/numind/sandboxbroker/readiness_test.go`
- `internal/numind/sandboxbroker/recovery.go`
- `internal/numind/sandboxbroker/recovery_test.go`
- `internal/numind/sandboxbroker/metrics.go`
- `internal/numind/sandboxbroker/metrics_test.go`

修改：

- `internal/numind/sandboxbroker/server.go`

### 实现

1. 读取host MemAvailable、workload cgroup memory/current/max/events、
   data-root bytes/inodes。
2. 启动时验证：
   - cgroup v2与memory/cpu/pids/io controllers
   - configured cgroup parent实际可用
   - data-root为指定UUID的独立mount
   - image digest存在
3. 实现2秒采样、3次迟滞：
   - workload 90% high停止准入
   - 80%恢复
   - 96% best-effort shed
   - host <1.5GiB停止准入
   - host <1.0GiB立即shed
4. output_persisting给10秒排空。
5. 启动recovery按journal+label有界对账：
   - known live恢复/销毁
   - unknown orphan销毁
   - missing container进入recovery_pending
   - 60秒后未收口项留持久补偿队列
6. `/healthz`、`/readyz`、Unix-only Prometheus metrics。
7. 日志字段完整但不记录文件内容、命令输出或密钥。

### RED

- fake metrics source覆盖high/recovery/shed所有阈值。
- 验证实际workload max派生阈值，不写死POC值。
- 单次<1GiB立即shed。
- mount/UUID/cgroup/controller/image缺失均not ready。
- recovery矩阵与60秒deadline。
- metrics无高基数字段和敏感值。

### 验收

- `go test ./internal/numind/sandboxbroker -run 'Pressure|Readiness|Recovery|Metrics' -race -count=1`
- 所有threshold使用实际发布max计算。
- broker不可ready时核心API设计上仍可启动。

## T5. sandboxd 与独立 reconcile 命令

### 目标

提供可部署进程和API停止时仍可运行的幂等补偿命令。

### 涉及文件

新增：

- `cmd/numind-sandboxd/main.go`
- `cmd/numind-sandbox-reconcile/main.go`
- `internal/numind/sandboxreconcile/service.go`
- `internal/numind/sandboxreconcile/service_test.go`

修改：

- `cmd/numind/main.go`或共享CLI bootstrap（仅在确有复用需要时）

### 实现

1. sandboxd解析独立配置，初始化journal、runtime、recovery和Unix server。
2. SIGTERM先drain，再最长300秒等待，最后有审计地取消。
3. reconcile只连接broker socket和Prod应用DB，不接Docker socket。
4. 使用已有store/账本接口收口：
   - sandbox session
   - agent run cancellation/termination
   - Reserve/Reconcile
   - 可重试输出状态
5. 不直接UPDATE用户余额；所有积分动作走现有ledger幂等接口。
6. dry-run默认或显式flag，apply需要明确参数；输出汇总不输出密钥。

### RED

- signal/drain生命周期测试。
- pending lease到session/run/ledger的状态矩阵。
- 同一补偿运行两次结果相同，无重复扣/退。
- API不可用的独立启动测试。
- 缺broker socket/DB时fail-closed且错误可诊断。

### 验收

- `go test ./internal/numind/sandboxreconcile ./cmd/numind-sandboxd ./cmd/numind-sandbox-reconcile -race -count=1`
- 两个命令均可构建。
- reconcile测试证明不直接写用户余额字段。

## T6. 用户 API broker wiring 与轻量管理端组合根

### 目标

只让用户 API使用broker；管理API不再初始化用户侧运行时或第二套Sandbox。

### 涉及文件

新增：

- `internal/numind/biz/admin_biz.go`
- `internal/numind/biz/admin_biz_test.go`

修改：

- `internal/numind/biz/biz.go`
- `internal/numind/admin_router.go`
- `internal/numind/admin_router_test.go`（或新增focused test）
- `scripts/cicd/deploy-remote.sh`
- `scripts/cicd/test-release-preflight.sh`

### 实现

1. 用户`NewBiz`按backend选择：
   - disabled → disabled Pool
   - docker →现有CLI client（Dev fallback）
   - broker →Unix Broker client
2. broker断开不阻止用户API启动；Sandbox工具软失败。
3. 新增`NewAdminBiz`只构造SOP、Credit、Pricing、Monitor CRUD、Announcement。
4. admin agent cancel传nil in-memory runner，继续写持久取消标志。
5. 管理进程不创建Pool、不加载skills/narration/lark、不启动XHS/memory/seeder worker。
6. deploy script：
   - 仅server挂broker socket和group-add
   - admin没有broker/socket/group
   - 两者都没有主/Rootless Docker socket
7. Prod env override走现有`NUMIND_` Viper合同，不修改`config_prod.yaml`。

### RED

- backend client选择table test。
- broker不可达时API composition仍成功。
- admin composition关键字段/runner为nil且无用户runtime构造调用。
- shell preflight断言server/admin docker run flags。
- 明确断言admin不含broker path、Docker group和Docker socket。

### 验收

- `go test ./internal/numind/biz ./internal/numind -run 'AdminBiz|SandboxBackend|Deploy' -race -count=1`
- `bash scripts/cicd/test-release-preflight.sh`
- `git grep`证明`config_prod.yaml`未改。
- admin启动日志不再出现Docker CLI、skills、narration、lark缺失告警。

## T7. 构建、Rootless provisioning、部署与回滚

### 目标

把同机隔离变成可重复、fail-closed且可回滚的基础设施，不要求用户手工操作服务器。

### 涉及文件

新增：

- `deploy/sandbox/numind-sandbox-control.slice`
- `deploy/sandbox/numind-sandbox-workload.slice`
- `deploy/sandbox/numind-sandboxd.service`
- `deploy/sandbox/numind-sandbox-data-root.mount`
- `deploy/sandbox/sandboxd.env.example`
- `scripts/cicd/provision-sandbox-host.sh`
- `scripts/cicd/deploy-sandboxd-remote.sh`
- `scripts/cicd/test-sandbox-provisioning.sh`
- `docs/superpowers/runbooks/prod-sandbox-isolation.md`

修改：

- `Dockerfile`
- `scripts/cicd/build-and-push.sh`
- `scripts/cicd/release.sh`
- `scripts/cicd/deploy-remote.sh`
- `scripts/cicd/test-release-preflight.sh`

### 实现

1. 单独Alpine builder静态构建sandboxd（musl + sqlite），避免Prod host glibc漂移。
2. server image包含reconcile命令；Prod API image仍不安装Docker CLI。
3. provisioning幂等创建：
   - non-login user/group/subuid/subgid
   - slirp4netns/rootless prerequisites
   - 8GiB ext4 image、UUID mount unit
   - systemd parent/control/workload limits
   - linger与Rootless daemon
   - broker socket目录和ACL
4. 脚本遇到不一致已有状态只报错，不覆盖。
5. cgroup POC必须实证controller和实际container path；失败阻止启用。
6. 部署：
   - 解析/验证Sandbox image digest
   - 保存旧broker binary/config
   - drain、原子替换、health/ready
   - broker失败恢复旧binary，用户API不升级
7. rollback：
   - backend disabled/旧API
   - 300秒drain
   - reconcile
   - 旧broker恢复
8. build机与部署机清理只删除未引用镜像，不删当前/回滚镜像或journal。

### RED

- 所有shell脚本在fake root下运行的幂等/失败测试。
- mount不是指定UUID时阻止daemon。
- API/admin flags负向合同。
- broker upgrade失败自动恢复旧binary。
- rollback顺序与reconcile必须先于清理。
- 构建结果用`file`证明sandboxd为host可运行静态binary。

### 验收

- `bash -n`所有新增/修改脚本。
- `bash scripts/cicd/test-sandbox-provisioning.sh`
- `bash scripts/cicd/test-release-preflight.sh`
- Docker build target成功，API镜像内无Docker CLI，sandboxd checksum固定。
- 不操作Prod；只在本地fixture和后续Dev执行。

## T8. 安全合同、集成与回归总检

### 目标

用可执行证据证明产品功能可用且隔离边界不能被绕过，为S5/S6准备。

### 涉及文件

新增：

- `scripts/cicd/test-sandbox-isolation.sh`
- `internal/numind/sandboxbroker/integration_test.go`
- `.ndf/features/prod-sandbox-isolation/security-contract.md`

可能修改：

- 本计划前七项的测试文件，仅限修复集成发现的问题。

### 实现与测试矩阵

1. 五个轻任务同时成功；第六个FIFO等待/超时。
2. 两个API owner共享全局五槽位。
3. 每任务512MiB/1CPU/64PIDs实际生效。
4. workload父级ceiling与high/recovery/shed按实际max。
5. 用户API访问主/Rootless Docker socket失败。
6. admin访问broker和两套Docker socket失败。
7. Rootless用户读取Prod secrets、data、cert、uploads、main Docker失败。
8. bind/network/device/privileged/namespace/cap/cgroup/image恶意请求拒绝。
9. Rootless daemon、broker、data-root、cgroup故障时核心API继续健康。
10. journal crash、orphan、broker/API重启和reconcile。
11. Exec/Copy/body/connection/stream/rate限制。
12. `go test ./...`、`go test -race` focused、`task lint`。

### 验收

- `go test ./... -count=1`
- `go test -race ./internal/numind/biz/sandbox ./internal/numind/sandboxbroker ./internal/numind/sandboxreconcile -count=1`
- `task lint`
- `bash scripts/cicd/test-sandbox-isolation.sh`
- `bash scripts/cicd/test-sandbox-provisioning.sh`
- `bash scripts/cicd/test-release-preflight.sh`
- 0 P0/P1安全合同失败。
- 生成的证据不包含Prod密钥或用户文件内容。

## 3. S4 完成条件

- T1–T8全部完成并各自有commit。
- 每项独立spec-compliance/code-quality review无P0。
- manifest `completed_tasks=8`、`reviewed_tasks=8`。
- `go test ./...`、focused race、`task lint`与所有shell合同测试通过。
- 尚未部署Prod。

## 4. 后续阶段

- S5：本地/同形态容器自动验收。
- S6：merge develop、部署Dev同形态broker，完成真实SOP、Agent、PPTX/DOCX/
  XLSX/PDF、文档系统、飞书、五并发和broker故障降级验收。
- S7：补齐Prod runtime secrets、只读预检、备份与最终执行清单；等待产品负责人
  单独明确授权后才写入/部署Prod。
