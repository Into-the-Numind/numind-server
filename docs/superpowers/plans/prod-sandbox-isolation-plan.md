# Prod 同机 Sandbox 隔离 — 实施计划

> Feature: `prod-sandbox-isolation`
> NDF stage: S3
> Source: `docs/superpowers/specs/2026-07-30-prod-sandbox-isolation-design.md`
> Repository: `numind-server`
> Prod deployment remains blocked until separate product-owner authorization.

## 执行规则

- 只在 feature worktree 修改；不修改 `config_prod.yaml`。
- 每个 task RED → GREEN → REFACTOR，完成后必须可编译、可独立验证、单独 commit。
- Tasks 串行执行；独立 reviewer逐 task做spec与质量审查。
- 不新增 Prod 业务表 migration。lease journal只使用Sandbox专用SQLite。
- 不新增LLM调用，因此不新增Langfuse generation；复用现有Agent run trace并补Sandbox metadata/metrics。

## 无环依赖图

```text
T1 protocol/client ───────────────┐
T2 journal ────────────────┐      │
T3 runtime policy/stream ──┼──> T4 scheduler ──> T5 Unix transport
                           │                         │
T6 capacity formula ───────┼──> T7 pressure/readiness
T2 + T3 ───────────────────┴──> T8 recovery
T5 + T7 + T8 ─────────────────> T9 observability
T5 + T7 + T8 + T9 ────────────> T10 sandboxd command
T2 ────────────────────────────> T11 reconcile command
T1 + T5 ───────────────────────> T12 user API wiring
独立 ──────────────────────────> T13 admin composition
T12 + T13 ─────────────────────> T14 deploy-role contract
T10 + T11 ─────────────────────> T15 artifact build
T6 ────────────────────────────> T16 Rootless provisioning
T14 + T15 + T16 ───────────────> T17 deploy/rollback/runbook
T1..T17 ───────────────────────> T18 integration/security gate
```

## T1. Broker 协议、配置与 Unix client

**目标**：锁定 `/v1` 协议与错误合同；新增 `broker` backend，但不切环境。

**文件**

- 新增 `internal/numind/biz/sandbox/broker_protocol.go`
- 新增 `internal/numind/biz/sandbox/broker_client.go`
- 新增 `internal/numind/biz/sandbox/broker_client_test.go`
- 修改 `go.mod`（将已锁定的 `golang.org/x/sys` 依赖标记为 direct）
- 修改 `internal/numind/biz/sandbox/config.go`
- 修改 `internal/numind/biz/sandbox/config_test.go`
- 修改 `internal/numind/biz/sandbox/errors.go`
- 修改 `internal/numind/biz/sandbox/pool.go`（关闭 broker idle connections）
- 修改 `internal/numind/biz/sandbox/pool_test.go`（in-flight spawn 关闭零泄漏）

**实现**

- `BackendBroker`、socket/metadata/连接/copy/输出/超时配置与spec默认值。
- HTTP-over-Unix client完整实现`DockerClient`；opaque lease id替代Docker id。
- owner与owner_boot分字段传递；owner来自必填稳定部署槽位配置，不从hostname推导；
  列表按稳定owner查询，确保重启后能发现上一次boot。
- 预热create显式发送`agent_run_id=0/sandbox_session_id=0`；提供
  activate/heartbeat/persisting lease lifecycle接口，真实ID在任务借出后绑定。
- strict JSON与必需语义；create expiry/state、inspect owner/status、list lease_ids
  对缺失/null/非法值fail-closed；请求模型中不存在
  image/mount/network/device/privileged/cap/cgroup字段。
- capacity/unavailable/policy/OOM/timeout/size错误映射。
- Copy使用有界stream，context取消关闭连接且不泄漏goroutine。

**RED/验收**

- fake Unix server覆盖所有方法、错误、超限、取消、stream close。
- `go test ./internal/numind/biz/sandbox -run 'Broker|Config' -race -count=1`
- 原docker/disabled测试保持通过；主服务默认行为不变。

## T2. SQLite lease journal 与状态机

**目标**：建立独立、崩溃可恢复、幂等的lease事实源。

**文件**

- 新增 `internal/numind/sandboxbroker/journal.go`
- 新增 `internal/numind/sandboxbroker/journal_test.go`
- 新增 `internal/numind/sandboxbroker/lease.go`
- 新增 `internal/numind/sandboxbroker/lease_test.go`

**driver/build决定**

- 使用仓库已有 `github.com/mattn/go-sqlite3 v1.14.34`，不修改`go.mod/go.sum`。
- T2即加入`CGO_ENABLED=1 go test`与reopen测试；T15再验证musl static artifact。

**实现**

- WAL、`synchronous=FULL`、busy timeout、单实例文件锁。
- `lease` + append-only `lease_event`。
- queued/creating/ready/active/output_persisting/destroying/terminated/recovery_pending。
- 显式transition table、request_id unique/idempotent、stale/pending有界查询。
- journal不保存文件、prompt、命令输出或密钥。

**RED/验收**

- 合法/非法迁移、并发幂等、crash reopen、双实例锁、stale边界。
- `CGO_ENABLED=1 go test ./internal/numind/sandboxbroker -run 'Journal|Lease' -race -count=1`
- `go test`结束后重新打开文件并核对event与lease。

## T3. 固定 Rootless runtime policy 与流式文件原语

**目标**：形成唯一固定的Docker参数和常量内存Copy能力，不实现排队或HTTP。

**文件**

- 新增 `internal/numind/sandboxbroker/runtime.go`
- 新增 `internal/numind/sandboxbroker/runtime_test.go`
- 新增 `internal/numind/sandboxbroker/stream.go`
- 新增 `internal/numind/sandboxbroker/stream_test.go`
- 修改 `internal/numind/biz/sandbox/docker_client.go`
- 修改 `internal/numind/biz/sandbox/docker_client_test.go`

**实现**

- 服务端固定digest、Seccomp checksum、512MiB/1CPU/64PIDs、30s/300s。
- non-root、cap-drop ALL、cap-add empty、no-new-privileges、read-only、network none。
- 固定`/workdir`、`/skills`、`/tmp` tmpfs与cgroup parent。
- Docker CLI CopyTo/From改64KiB streaming，不再全量`io.ReadAll`/buffer tar。
- Copy path canonicalization；拒绝absolute/`..`/symlink/hardlink/device。
- Exec固定user/workdir/env allowlist，stdout+stderr总上限4MiB。

**RED/验收**

- SpawnConfig逐字段测试；任何可变危险参数都不存在。
- 50/100/200MiB与10文件边界、tar攻击、慢reader/cancel。
- `go test ./internal/numind/biz/sandbox ./internal/numind/sandboxbroker -run 'Runtime|Stream|Copy' -race -count=1`

## T4. 全局 FIFO lease scheduler 与五槽位

**目标**：单broker全局限制ready+creating+active容器和active任务，兼容滚动双API。

**文件**

- 新增 `internal/numind/sandboxbroker/scheduler.go`
- 新增 `internal/numind/sandboxbroker/scheduler_test.go`

**实现**

- total_container_max=5、active_task_max=5，ready计总容器不计active。
- FIFO等待30秒；取消立即出队并释放计数。
- active=5不补standby；destroy后仅唤醒FIFO头。
- 两个owner共享同一计数；request_id重放不重复占slot。

**RED/验收**

- 五轻任务成功、第六等待/接棒/超时；并发取消无slot/goroutine泄漏。
- 两owner共享5；race下计数不超过5。
- `go test ./internal/numind/sandboxbroker -run Scheduler -race -count=1`

## T5. Unix transport、SO_PEERCRED 与 RPC limits

**目标**：把T2–T4通过唯一Unix socket暴露，不监听TCP。

**文件**

- 新增 `internal/numind/sandboxbroker/config.go`
- 新增 `internal/numind/sandboxbroker/server.go`
- 新增 `internal/numind/sandboxbroker/server_test.go`
- 新增 `internal/numind/sandboxbroker/auth_linux.go`
- 新增 `internal/numind/sandboxbroker/auth_linux_test.go`
- 新增 `internal/numind/sandboxbroker/limits.go`
- 新增 `internal/numind/sandboxbroker/limits_test.go`

**实现**

- socket父目录/inode owner/mode/symlink校验，mode 0660。
- accept时`SO_PEERCRED`仅允许配置API host UID；owner_id不作认证。
- 仅Unix listener；strict decoder；metadata 64KiB、32连接、4全局copy stream、
  每lease/方向1、100MiB/s聚合。
- create/activate/heartbeat/persisting/exec/copy/mkdir/inspect/list/delete endpoints。
- response只返回lease id和归一状态，不泄漏container id/host path。

**RED/验收**

- UID allow/deny、symlink/mode、未知字段、连接/stream/速率与全部endpoint合同。
- 配置TCP地址或危险字段时启动失败。
- `go test ./internal/numind/sandboxbroker -run 'Server|Auth|Limit' -race -count=1`

## T6. Prod 容量公式与发布阻断

**目标**：把7天/72小时证据和正式总内存公式做成受测代码，而非人工口算。

**文件**

- 新增 `internal/numind/sandboxbroker/capacity.go`
- 新增 `internal/numind/sandboxbroker/capacity_test.go`
- 新增 `scripts/cicd/calculate-sandbox-capacity.sh`
- 新增 `scripts/cicd/test-sandbox-capacity.sh`

**实现**

- 输入至少7天历史，或明确标记72小时新采样；不足即阻断。
- 计算同业务时段MemAvailable P1。
- `parent_max=floor64MiB(min(2.75GiB, P1-1.25GiB))`。
- `workload_max=parent_max-384MiB-128MiB`。
- high=`min(2GiB,90%)`、recovery=80%、shed=96%。
- parent<2GiB或workload<1.5GiB阻断。
- 输出机器可读systemd values与人类可读证据摘要，不含敏感数据。

**RED/验收**

- P1、floor64、cap、保留量、低边界、历史时长、异常/缺样本测试。
- `go test ./internal/numind/sandboxbroker -run Capacity -count=1`
- `bash scripts/cicd/test-sandbox-capacity.sh`

## T7. 压力准入与 readiness

**目标**：按T6正式值保护核心业务，基础设施不满足时Sandbox fail-closed。

**文件**

- 新增 `internal/numind/sandboxbroker/pressure.go`
- 新增 `internal/numind/sandboxbroker/pressure_test.go`
- 新增 `internal/numind/sandboxbroker/readiness.go`
- 新增 `internal/numind/sandboxbroker/readiness_test.go`
- 修改 `internal/numind/sandboxbroker/scheduler.go`
- 修改 `internal/numind/sandboxbroker/scheduler_test.go`
- 修改 `internal/numind/sandboxbroker/journal.go`
- 修改 `internal/numind/sandboxbroker/journal_test.go`
- 修改 `internal/numind/sandboxbroker/server.go`
- 修改 `internal/numind/sandboxbroker/server_test.go`

**实现**

- 2秒采样；workload high连续3次停准入、80%连续3次恢复、96%连续3次shed。
- Host<1.5GiB连续3次停准入；<1.0GiB单次立即shed。
- output_persisting最多10秒排空。
- cgroup v2/controller/cgroup parent、data-root mount+UUID、disk bytes/inodes、
  image digest readiness。
- 70%告警，85%停准入；mount失败不退化根盘。
- scheduler生产启动默认关闭；只有新鲜且完整的pressure/readiness结果才能打开。
- 准入状态在FIFO真正获得slot时原子重检；已排队任务在门关闭后不得spawn，
  恢复后重新检查再按原顺序放行。
- Ready/warm lease激活同样在发布Active前原子门禁；关闭时保留Ready。
- 新请求在写journal前先做只读replay查询和准入预检，slot grant仍二次检查；
  并发readiness同步串行发布，旧快照不能覆盖新快照。
- 采样中断/无效样本映射503；真实内存压力映射429。

**RED/验收**

- 所有迟滞、单次紧急、实际max派生、mount/UUID/controller/image故障。
- `go test ./internal/numind/sandboxbroker -run 'Pressure|Readiness' -race -count=1`

## T8. Journal 与 Rootless container 恢复

**目标**：broker重启后60秒内恢复或留下持久补偿，清理仅限专用daemon。

**文件**

- 新增 `internal/numind/sandboxbroker/recovery.go`
- 新增 `internal/numind/sandboxbroker/recovery_test.go`

**实现**

- journal live+container存在：inspect后恢复/销毁stale。
- journal live+container缺失：recovery_pending。
- 固定label orphan：销毁；不扫描主Docker。
- 60秒有界；未完成进入持久补偿，不无限期fail-closed。
- container count可证明一致后才重新ready。

**RED/验收**

- 完整recovery矩阵、超时、daemon失败、重复运行幂等。
- `go test ./internal/numind/sandboxbroker -run Recovery -race -count=1`

## T9. Health、metrics 与无敏感日志

**目标**：完成Unix-only health/readiness/Prometheus与结构化审计。

**文件**

- 新增 `internal/numind/sandboxbroker/metrics.go`
- 新增 `internal/numind/sandboxbroker/metrics_test.go`
- 新增 `internal/numind/sandboxbroker/audit.go`
- 新增 `internal/numind/sandboxbroker/audit_test.go`
- 修改 `internal/numind/sandboxbroker/server.go`

**实现**

- `/healthz` journal可用；`/readyz` runtime/pressure可接任务。
- spec列出的lease/queue/reject/exec/copy/memory/disk/reconcile指标。
- 日志包含request/lease/run/session/state/wait/termination，过滤文件、prompt、
  env value、命令输出和密钥。
- metrics不使用user/run/lease等高基数label。

**RED/验收**

- health/ready状态矩阵、指标字段、高基数/敏感字符串负向测试。
- `go test ./internal/numind/sandboxbroker -run 'Metrics|Audit|Health' -race -count=1`

## T10. `numind-sandboxd` 进程与 drain

**目标**：提供单一可部署broker入口。

**文件**

- 新增 `cmd/numind-sandboxd/main.go`
- 新增 `cmd/numind-sandboxd/main_test.go`
- 新增 `internal/numind/sandboxbroker/runtime_adapter.go`
- 新增 `internal/numind/sandboxbroker/runtime_adapter_test.go`
- 新增 `internal/numind/sandboxbroker/pressure_runner_linux.go`
- 新增 `internal/numind/sandboxbroker/pressure_runner_linux_test.go`
- 新增 `internal/numind/sandboxbroker/readiness_linux.go`
- 新增 `internal/numind/sandboxbroker/readiness_linux_test.go`

**实现**

- 实现 `ContainerRuntime` 的固定 Docker CLI adapter；只能调用 T3
  `RuntimePolicy` 已校验的 spawn/exec/copy/mkdir/inspect/delete 模板，不新增
  RPC 可控 Docker 参数。
- 读取独立非业务配置，初始化journal/runtime adapter/scheduler/recovery/server。
- 以可信时钟每2秒读取`/proc/meminfo`、cgroup v2
  `memory.current/memory.max/cgroup.controllers`、data-root挂载点/UUID、
  `statfs`字节与inode、固定镜像digest；先更新pressure/readiness，再通过
  `ReadinessChecker.SyncAdmission`同步实际scheduler准入门。独立watchdog在采样
  ticker停止或超过4秒未刷新时必须关闭scheduler准入；恢复时重新检查FIFO队首。
- runner无论`Observe`是否返回采样错误都必须继续执行`SyncAdmission`；若
  decision含`ShedLeaseID`则必须先执行回收，`SamplingGap`作为独立告警保留。
- SIGTERM先关闭准入，最长300秒drain，再审计取消。
- 无Prod DB/COS/LLM/飞书配置和网络listener。

**RED/验收**

- startup依赖失败、signal/drain、deadline、第二实例测试。
- `go test ./cmd/numind-sandboxd -race -count=1`
- `go build ./cmd/numind-sandboxd`

## T11. 独立 `numind-sandbox-reconcile`

**目标**：主API不健康时仍可幂等收口session/run/积分账本。

**文件**

- 新增 `internal/numind/sandboxreconcile/service.go`
- 新增 `internal/numind/sandboxreconcile/service_test.go`
- 新增 `cmd/numind-sandbox-reconcile/main.go`
- 新增 `cmd/numind-sandbox-reconcile/main_test.go`

**实现**

- 只接broker socket与应用DB，不接Docker socket。
- dry-run默认；apply显式参数。
- 调用现有store/ledger接口，不直接UPDATE用户余额。
- session/run/Reserve-Reconcile/输出状态均幂等；输出无密钥。
- 独立main自行读取既有CLI config helper，不修改`cmd/numind/main.go`。

**RED/验收**

- pending矩阵、双跑幂等、API停止、broker/DB缺失、无直接余额写。
- `go test ./internal/numind/sandboxreconcile ./cmd/numind-sandbox-reconcile -race -count=1`

## T12. 用户 API broker wiring 与软降级

**目标**：只在用户API按backend选择client，broker故障不拖垮核心API。

**文件**

- 新增 `internal/numind/biz/sandbox/client_factory.go`
- 新增 `internal/numind/biz/sandbox/client_factory_test.go`
- 修改 `internal/numind/biz/biz.go`
- 新增 `internal/numind/biz/sandbox_wiring_test.go`

**实现**

- disabled→disabled Pool，docker→现有CLI，broker→Unix client。
- broker不可达时Pool异步预热失败但NewBiz/API仍启动。
- Agent/Skill/Document继续复用现有Pool/DockerClient路径。
- SandboxHook创建审计行后调用activate绑定run/session；绑定失败立即销毁lease并
  收口审计行，不能执行代码。

**RED/验收**

- backend table test、broker不可达、产品服务仍被构造。
- `go test ./internal/numind/biz/sandbox ./internal/numind/biz -run 'ClientFactory|SandboxWiring' -race -count=1`

## T13. 轻量管理端组合根

**目标**：管理API不初始化第二套Sandbox或任何用户侧worker。

**文件**

- 新增 `internal/numind/biz/admin_biz.go`
- 新增 `internal/numind/biz/admin_biz_test.go`
- 修改 `internal/numind/admin_router.go`
- 新增 `internal/numind/admin_router_sandbox_test.go`

**实现**

- `NewAdminBiz`只构造SOP、Credit、Pricing、Monitor CRUD、Announcement。
- Agent admin cancel传nil runner，继续写持久取消标志。
- 不构造Pool/Document runtime/Agent registry/Skills/Feishu workspace/narration/
  XHS worker/memory worker/seeder。

**RED/验收**

- 依赖存在/nil矩阵；构造probe证明用户runtime factory零调用。
- `go test ./internal/numind/biz ./internal/numind -run 'AdminBiz|Admin.*Sandbox' -race -count=1`

## T14. Server/Admin 部署角色权限合同

**目标**：部署命令精确保证只有用户API获得broker socket。

**文件**

- 修改 `scripts/cicd/deploy-remote.sh`
- 修改 `scripts/cicd/test-release-preflight.sh`

**实现**

- server只挂broker socket并group-add专用GID；无任一Docker socket/group。
- admin无broker、无Docker socket/group。
- broker backend使用现有`NUMIND_` env override；不修改`config_prod.yaml`。
- server必须显式设置稳定且每副本唯一的`NUMIND_SANDBOX_BROKER_OWNER_ID`；
  禁止从容器hostname推导，旧副本排空停止后才可复用同一部署槽位ID。
- broker socket缺失时部署可按显式开关disabled；禁止误挂主socket。

**RED/验收**

- fake docker run精确断言server/admin flags与负向字符串。
- `bash scripts/cicd/test-release-preflight.sh`
- `git diff -- config_prod.yaml`为空。

## T15. 可移植 artifact build

**目标**：生成Prod host可执行sandboxd与容器内reconcile，API镜像仍无Docker CLI。

**文件**

- 修改 `Dockerfile`
- 修改 `scripts/cicd/build-and-push.sh`
- 新增 `scripts/cicd/test-sandbox-artifacts.sh`

**实现**

- Alpine/musl + sqlite静态构建sandboxd，`file`验证static。
- reconcile留在server image内运行。
- API Prod stage不安装Docker CLI。
- 输出sandboxd SHA256供部署核对。

**RED/验收**

- artifact脚本验证架构、static、checksum、两个命令可运行、API无docker。
- `bash scripts/cicd/test-sandbox-artifacts.sh`
- Docker build相关targets成功。

## T16. Rootless、cgroup 与8GiB data-root provisioning

**目标**：幂等建设同机隔离底座；任何不一致fail-closed。

**文件**

- 新增 `deploy/sandbox/numind-sandbox-control.slice`
- 新增 `deploy/sandbox/numind-sandbox-workload.slice`
- 新增 `deploy/sandbox/numind-sandboxd.service`
- 新增 `deploy/sandbox/numind-sandbox-data-root.mount`
- 新增 `deploy/sandbox/sandboxd.env.example`
- 新增 `scripts/cicd/provision-sandbox-host.sh`
- 新增 `scripts/cicd/test-sandbox-provisioning.sh`

**实现**

- non-login user/group/subuid/subgid、slirp4netns/rootless prerequisites、linger。
- 8GiB ext4 image、UUID mount、专用Docker data-root。
- 应用T6生成的parent/control/workload memory、4CPU、576Tasks。
- 实证cgroup v2 delegation与memory/cpu/pids/io以及实际container path。
- broker socket ACL；Rootless用户无Prod目录权限。
- 已有状态不一致只报错，不覆盖。

**RED/验收**

- fake root幂等、mount UUID、controller、目录ACL、已有冲突测试。
- `bash -n scripts/cicd/provision-sandbox-host.sh`
- `bash scripts/cicd/test-sandbox-provisioning.sh`

## T17. Broker发布、回滚、清理与runbook

**目标**：在Dev/未来Prod可自动升级和回滚broker，不要求用户手工SSH。

**文件**

- 新增 `scripts/cicd/deploy-sandboxd-remote.sh`
- 修改 `scripts/cicd/release.sh`
- 修改 `scripts/cicd/deploy-remote.sh`
- 修改 `scripts/cicd/test-release-preflight.sh`
- 新增 `docs/superpowers/runbooks/prod-sandbox-isolation.md`

**实现**

- 校验Sandbox image digest、sandboxd binary checksum、cgroup/data-root/ready。
- 保存旧binary/config；300秒drain；原子替换；失败自动恢复旧binary。
- broker ready后才部署用户API；admin单独部署且无socket。
- rollback：backend disabled/旧API→drain→reconcile→旧broker。
- 清理仅未引用镜像；不删当前/回滚镜像、journal或data-root。
- runbook只描述AI执行的自动化和证据，不把SSH命令甩给用户。

**RED/验收**

- fake remote覆盖成功、broker失败恢复、API失败回滚、reconcile顺序、清理保护。
- `bash -n scripts/cicd/deploy-sandboxd-remote.sh scripts/cicd/release.sh scripts/cicd/deploy-remote.sh`
- `bash scripts/cicd/test-release-preflight.sh`

## T18. 最终安全合同与集成门禁

**目标**：只新增总检，不在本task混入实现修复。

**文件**

- 新增 `internal/numind/sandboxbroker/integration_test.go`
- 新增 `scripts/cicd/test-sandbox-isolation.sh`
- 新增 `.ndf/features/prod-sandbox-isolation/security-contract.md`

**规则**

- 如果总检发现实现bug，暂停T18并新增明确文件清单的follow-up task/commit；T18
  不允许开放修改T1–T17文件。

**覆盖**

- 五并发、第六FIFO、双owner全局5。
- 512MiB/1CPU/64PIDs与父级动态ceiling。
- user API不能访问两套Docker；admin不能访问broker/两套Docker。
- Rootless用户不能读Prod secrets/data/certs/uploads/main Docker。
- 所有危险broker字段/路径/tar/limit/慢连接拒绝。
- daemon/broker/data-root/cgroup故障时核心API健康。
- journal crash/orphan/recovery/reconcile/rollback。

**验收**

- `go test ./... -count=1`
- `go test -race ./internal/numind/biz/sandbox ./internal/numind/sandboxbroker ./internal/numind/sandboxreconcile -count=1`
- `task lint`
- `bash scripts/cicd/test-sandbox-capacity.sh`
- `bash scripts/cicd/test-sandbox-artifacts.sh`
- `bash scripts/cicd/test-sandbox-provisioning.sh`
- `bash scripts/cicd/test-sandbox-isolation.sh`
- `bash scripts/cicd/test-release-preflight.sh`
- 0 P0/P1；证据无Prod密钥/用户文件。

## S4 完成条件与后续

- T1–T18全部独立commit并review，manifest completed/reviewed均为18。
- S5本地同形态自动验收。
- S6 merge develop并部署Dev，真实验收SOP、Agent、PPTX/DOCX/XLSX/PDF、
  文档系统、飞书、5并发与broker故障降级。
- S7补Prod配置/密钥、只读预检、备份和最终执行清单。
- 只有产品负责人再次明确授权，才执行Prod写入和部署。
