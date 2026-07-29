# Prod 同机 Sandbox 隔离 — 详细技术设计

> Feature: `prod-sandbox-isolation`
> NDF stage: S2
> Date: 2026-07-30
> Status: awaiting product-owner confirmation

## 1. 这次要交付的产品结果

Dev 中已经可用的代码执行、插件技能、文档导出和文件生成能力进入 Prod：

- 首页 SOP 可以继续调用依赖 Sandbox 的步骤。
- AI 智能体可以运行 `run_python`、加载技能并生成 PPTX、DOCX、XLSX、PDF 等文件。
- 文档系统可以导出 DOCX/PDF。
- Sandbox 繁忙或故障时，只影响上述文件/代码任务，不影响登录、普通聊天、SOP 非 Sandbox 步骤、通知、积分、MySQL、Redis 和管理后台。
- 上线过程不导入 Dev 用户数据，不覆盖 Prod 用户资料、积分余额或历史记录。

本设计不启用会议副驾、说话人分离、`chatbot_query_rewrite` 或
`universal_rewriter`，也不包含 QA。

## 2. 不可违反的上线条件

1. 用户 API、管理 API 都不能拿到 Prod 主 Docker socket。
2. 用户 API 也不能拿到 Sandbox Rootless Docker socket，只能访问受限 broker。
3. 管理 API 连 broker socket 也不能访问，且不得初始化第二套 Sandbox Pool。
4. Sandbox Rootless Linux 用户不能读取 Prod 密钥、证书、用户上传目录、MySQL/Redis
   数据目录或主 Docker data-root。
5. broker 不接受调用方传入镜像、挂载、网络、设备、特权模式、namespace、
   capability 或 cgroup parent。
6. 全机最多 5 个 Sandbox 容器，最多 5 个同时任务；待命容器也计入总数。
7. 512 MiB 是单任务 ceiling，不是预占；五个轻任务允许同时执行。
8. 核心业务至少保留 1.25 GiB 内存余量；压力过高时先拒绝或结束 Sandbox。
9. Rootless data-root 只能使用独立 8 GiB 文件系统；挂载失败时 Sandbox
   fail-closed，不能改写到 Prod 根盘目录。
10. Prod 写操作和部署仍须产品负责人单独明确授权。

## 3. 系统结构

```text
用户浏览器
    |
    v
numind-server-prod（正式用户 API，主 Docker）
    |  只挂载 /run/numind-sandbox/sandboxd.sock
    |  没有 Docker CLI、没有任何 Docker socket
    v
sandboxd（宿主机普通用户 numind-sandbox）
    |  固定且受限的内部操作
    v
Rootless Docker（只属于 numind-sandbox）
    |
    +-- sandbox-skill@sha256:<固定 digest>，0..5 个

numind-admin-server-prod ──X── sandboxd
numind-admin-server-prod ──X── 主 Docker / Rootless Docker

Prod 主 Docker
    +-- numind-server-prod
    +-- numind-admin-server-prod
    +-- MySQL / Redis / 前端等

Sandbox Rootless Docker 不能看到、停止或修改上面任何主 Docker 容器。
```

### 3.1 两套 Docker 的职责

- Prod 当前 rootful Docker 保持不变，继续运行正式业务容器。
- 新建不可登录用户 `numind-sandbox`，仅它运行 Rootless Docker 与 `sandboxd`。
- 两套 daemon 使用不同 socket、不同 data-root、不同用户和不同 cgroup 子树。
- `sandboxd` 是唯一能访问 Rootless Docker socket的进程。
- 任何正式业务容器都不加入 `docker` group。

### 3.2 为什么 API 不能直接拿 Docker socket

Docker socket 相当于“这套 Docker 的总遥控器”。若把 socket 直接给 API，API
理论上可以自行改挂载和安全参数。broker 把能力缩小成固定的六件事：申请任务、
执行命令、传入文件、取出文件、查询状态、销毁任务。所有安全参数由 broker
自己填写，API 无法改变。

## 4. 进程与运行角色

### 4.1 用户 API

- `sandbox.backend=broker`。
- 使用 `BrokerDockerClient` 实现现有 `sandbox.DockerClient` 接口，尽量不改
  Agent、Skill 和 Document 的产品调用链。
- `pool_min=1`，本地 Pool 仍负责借出/归还待命 Sandbox；全局上限由单例 broker
  执行。
- broker 不可用时，Pool 为空但 API 正常启动。

### 4.2 管理 API

当前管理 API 会调用完整 `biz.NewBiz`，因此会错误初始化用户侧 Sandbox、技能目录、
飞书 CLI、narration、Agent worker、XHS worker 和 memory cron。它既造成无意义告警，
又可能形成重复后台任务。

实现时新增轻量 `NewAdminBiz` 组合根，仅构造管理端实际使用的：

- SOP 管理服务
- Credit 与 Pricing
- Monitor CRUD（不启动采集 worker）
- Announcement

Agent run 强制取消继续先写数据库的 `cancellation_requested_at`。管理 API 本来就在
独立进程，无法直接调用用户 API 内存中的 cancel，因此传入 nil runner 与真实架构
一致；用户 API 按现有持久取消路径收口。

管理 API 不初始化：

- Sandbox Pool / Document export runtime
- Agent tool registry / Skills
- Feishu personal workspace runtime
- narration provider
- XHS enrichment worker
- memory cron/extractor
- opinion-track seeder

因此 `Dockerfile.admin` 不需要复制技能、Docker CLI 或 lark-cli，也不挂载 broker。

### 4.3 sandboxd

- 单一 Unix socket listener，不监听 TCP。
- 运行用户：`numind-sandbox`。
- 自身没有 Prod MySQL、Redis、COS、LLM 或飞书密钥。
- 只访问 Rootless Docker socket、lease journal、独立 data-root 和固定 Seccomp 文件。
- 启动前校验 image digest、cgroup、data-root mount、磁盘水位和 socket 目录权限；
  任一硬条件不满足则不接任务。

## 5. API 与 broker 协议

协议使用 HTTP/1.1 over Unix domain socket，版本前缀 `/v1`。选择它是为了不引入
新的 RPC 框架，同时可以使用 Go 标准库完成取消、流式限长和健康检查。

### 5.1 身份校验

- socket：`/run/numind-sandbox/sandboxd.sock`
- 目录：root:`numind-sandbox-api`，mode `02770`
- socket：`numind-sandbox`:`numind-sandbox-api`，mode `0660`
- 用户 API 容器仅 `--group-add <numind-sandbox-api gid>`。
- broker 在 accept 时读取 `SO_PEERCRED`，只接受部署时固定的用户 API host UID。
- socket 父目录、socket inode 的 owner/mode 每次启动都检查；拒绝 symlink。
- request 中的 `owner_id` 只是 lease 分组字段，不用于认证。
- 不使用 bearer token；协议不经过网络，安全边界是 socket inode + peer credentials。

### 5.2 通用限制

- request metadata/body JSON：64 KiB。
- 全局已建立连接：32。
- 全局 Copy 流：4。
- 每个 lease、每个方向最多 1 条 Copy 流。
- Copy 聚合速率：100 MiB/s。
- 读写 buffer：64 KiB，禁止把整个大文件读入内存。
- 所有变更请求必须带 UUID `request_id`；broker journal 保证幂等。
- client context 取消时，排队请求立即移除，流和计数器立即释放。

### 5.3 端点

#### `POST /v1/leases`

请求：

```json
{
  "request_id": "uuid",
  "owner_id": "api-instance",
  "owner_boot_id": "boot-id",
  "agent_run_id": 0,
  "sandbox_session_id": 0
}
```

调用方不能提供镜像或 Docker 参数。broker 使用服务端固定模板创建容器。
`owner_id` 是跨进程重启稳定的 API 实例标识，`owner_boot_id` 是本次进程启动标识；
两者必须分字段保存。启动清理按稳定 `owner_id` 列出本次和历史 boot 的 lease，
再用 `owner_boot_id` 区分当前进程与上一次启动遗留项。
预热容器创建时任务尚未发生，因此两个业务关联 ID 固定为 0；这不是缺失值，
而是明确表示 `ready/unbound`。容器被真实任务借出后，必须通过下一节的
`activate` 原子绑定真实 ID，之后不可改绑。

响应：

```json
{
  "lease_id": "opaque-uuid",
  "state": "ready",
  "expires_at": "RFC3339"
}
```

容器达到 5 个时进入全局 FIFO，最多等待 30 秒。

#### `POST /v1/leases/{id}/activate`

Pool 把待命容器借给任务、且 `agent_sandbox_session` 审计行创建成功后调用：

```json
{
  "request_id": "uuid",
  "agent_run_id": 390,
  "sandbox_session_id": 123
}
```

状态由 `ready/unbound` 原子变成 `active/bound`，并开始 300 秒会话计时。
两个 ID 必须都大于 0；同一 `request_id` 可重放，但已绑定 lease 不允许换绑到
其他 run/session。若 activate 失败，API 立即归还/销毁该 lease，并把刚创建的审计
行收口为 failed，不能继续执行用户代码。

#### `POST /v1/leases/{id}/heartbeat`

活动任务每 10 秒调用。30 秒无心跳视为 stale，进入销毁和补偿流程。

#### `POST /v1/leases/{id}/exec`

只接收 argv、受限 env 和 request id：

```json
{
  "request_id": "uuid",
  "argv": ["/bin/sh", "-c", "python /workdir/task.py"],
  "env": ["NUMIND_ALLOWED_KEY=value"]
}
```

- workdir 固定 `/workdir`。
- user 固定 `1000:1000`。
- 单次执行最长 30 秒。
- stdout + stderr 合计最多 4 MiB，超出时终止 exec 并返回 `output_too_large`。
- env key 只允许 `LANG`、`LC_*`、`TZ` 和当前 Sandbox 工具合同显式列出的非密钥字段。
- 禁止调用方覆盖 HOME、PATH、Docker、代理或凭据相关变量。

`argv` 中的代码本身允许由 Agent 生成；隔离依赖容器边界、无网络、只读 rootfs、
非 root、资源限制和无宿主机挂载，而不是尝试猜测所有代码内容。

#### `PUT /v1/leases/{id}/files?path=<sandbox-path>`

- 目标只能位于 `/workdir/input`、`/workdir` 或 `/skills/<allowed-skill>`。
- 单文件 50 MiB。
- 每 lease CopyIn 最多 10 个文件、总计 100 MiB。
- 路径 canonicalize 后再次校验，拒绝 `..`、绝对逃逸、symlink 逃逸和特殊设备。
- 平台 skill 名称来自镜像内固定 allowlist，不接受任意宿主机路径。

#### `GET /v1/leases/{id}/files?path=<sandbox-path>`

- 只允许 `/workdir/output` 下普通文件。
- 每 lease CopyOut 最多 10 个文件、总计 200 MiB，单文件仍为 50 MiB。
- broker 以 tar stream 返回；client 使用安全解包，拒绝 symlink、hardlink、device、
  绝对路径和 `..`。
- 开始取输出前先调用 `POST /persisting`；压力回收给该状态最多 10 秒排空。

#### `POST /v1/leases/{id}/mkdir`

只允许为当前实现准备 `/workdir/input`、`/workdir/output` 和
`/skills/<allowed-skill>`，不提供通用 mkdir。

#### `GET /v1/leases/{id}`

返回 broker 归一化后的 running/exited/OOM/exit code，不返回 Rootless container id。

#### `GET /v1/leases?owner_id=...`

只返回当前 peer + 稳定 owner 的全部 lease（包括不同 `owner_boot_id`），用于 API Pool
启动时清理自己上一次 boot 的 orphan。
不会暴露 Rootless daemon 的通用 container list。

#### `DELETE /v1/leases/{id}`

幂等销毁。销毁完成释放全局 container/task slot，并唤醒 FIFO 第一个等待者。

#### `GET /healthz`、`GET /readyz`、`GET /metrics`

- health：进程活着、journal 可读写。
- ready：Rootless daemon、data-root、image、cgroup 和压力准入均可接任务。
- metrics：仅 Unix socket，Prometheus text format。

### 5.4 错误合同与产品文案

| broker code | Go sentinel | 用户看到 |
|---|---|---|
| `capacity` | `ErrPoolExhausted` | 当前文件处理任务较多，请稍后重试 |
| `unavailable` | `ErrSandboxDisabled` / `ErrBrokerUnavailable` | 文件处理服务暂时不可用，其他功能不受影响 |
| `policy_denied` | `ErrSandboxPolicyDenied` | 文件处理请求不符合安全规则 |
| `timeout` | 现有 timeout | 文件处理超时，请缩小任务后重试 |
| `oom` | `ErrSandboxOOM` | 文件处理使用资源过多，请缩小任务后重试 |
| `input_too_large` | `ErrInputTooLarge` | 文件过大，单个文件不能超过 50MB |
| `output_too_large` | `ErrOutputTooLarge` | 生成文件过大，请缩小内容后重试 |

错误不得把 socket 路径、container id、宿主机路径或内部命令泄露给前端。

## 6. broker 固定的 Docker 参数

`sandboxd` 从只读配置读取唯一允许的镜像 digest，创建时固定：

```text
image=<TCR repository>@sha256:<verified digest>
user=1000:1000
cap-drop=ALL
cap-add=<empty>
security-opt=no-new-privileges
security-opt=seccomp=<verified absolute path>
read-only=true
network=none
memory=512m
cpus=1.0
pids-limit=64
tmpfs=/workdir:size=512m,uid=1000,gid=1000,nodev,nosuid
tmpfs=/skills:size=64m,uid=1000,gid=1000,nodev,nosuid
cgroup-parent=<verified delegated workload slice>
labels=numind.sandbox=1, lease_id, broker_instance
```

Prod 当前无 AppArmor，因此不伪造 AppArmor 已启用；Seccomp、Rootless、非 root、
cap-drop、no-new-privileges、只读 rootfs、无网络和无挂载是硬门槛。

以下字段在 broker API 中根本不存在：

- image/tag/digest
- bind/volume mount
- device
- privileged
- network mode
- pid/ipc/uts/user namespace mode
- cap-add
- security-opt
- cgroup parent
- entrypoint

即使构造恶意 JSON，也由 strict decoder 的 unknown-field rejection 拒绝。

## 7. 全局 lease、并发与恢复

### 7.1 SQLite journal

位置：`/opt/numind-sandbox/state/leases.db`，仅 `numind-sandbox` 可读写。

SQLite 设置：

- WAL
- `synchronous=FULL`
- `busy_timeout=5000`
- 单 broker 文件锁，第二实例启动失败

`lease` 表核心字段：

```text
lease_id TEXT PRIMARY KEY
request_id TEXT UNIQUE NOT NULL
peer_uid INTEGER NOT NULL
owner_id TEXT NOT NULL
agent_run_id INTEGER
sandbox_session_id INTEGER
container_id TEXT
state TEXT NOT NULL
created_at / updated_at / expires_at / last_heartbeat_at
copy_in_files / copy_in_bytes / copy_out_files / copy_out_bytes
termination_reason TEXT
reconcile_state TEXT
```

另有 append-only `lease_event`，记录状态迁移、压力拒绝、超限和恢复动作。

### 7.2 状态机

```text
queued -> creating -> ready -> active -> output_persisting
   |          |         |        |             |
   +----------+---------+--------+-------------+
                         v
                     destroying -> terminated
                         |
                         v
                   recovery_pending
```

- 每个状态迁移先写 journal，再做可重试外部动作。
- `request_id` 重放返回同一结果，不重复创建容器。
- `DELETE` 和 reconcile 都幂等。
- `ready` 容器计入总容器 5，但不计 active task。
- `activate` 后计 active task；最大同样是 5。
- active=5 时不补 standby。

### 7.3 broker 重启

1. 获取 journal 独占锁并进入 recovering。
2. 只在 Rootless daemon 内按固定 label 列出 Sandbox。
3. journal live + 容器存在：inspect 后恢复或销毁 stale lease。
4. journal live + 容器不存在：标记 `recovery_pending`。
5. 有 label 但 journal 不认识：销毁 orphan。
6. 60 秒内完成有界扫描；未完成项进入持久补偿队列，不无限阻塞健康检查。
7. 在 container count 可证明一致之前 `readyz=false`；一致后可接新任务，
   `recovery_pending` 仍由 reconcile 收口。

### 7.4 独立 reconcile

新增 `numind-sandbox-reconcile` 一次性命令：

- 在主 API 不健康时也能单独运行。
- 只挂 broker socket，不挂任何 Docker socket。
- 使用 Prod 应用现有数据库配置，读取 pending lease 与
  `agent_sandbox_session` / `agent_run`。
- 仅调用现有幂等终止、取消和 Reserve/Reconcile 账本接口，不直接改用户积分余额。
- 重跑不会重复退款、扣费或重复上传。
- 完成后输出按 lease/session/run 汇总的审计结果，不输出密钥。

## 8. CPU、内存和进程数

### 8.1 层级

专用用户的父级 cgroup：

```text
user-<sandbox_uid>.slice                       parent
└─ user@<sandbox_uid>.service
   ├─ numind-sandbox-control.slice             dockerd + sandboxd
   └─ numind-sandbox-workload.slice            0..5 Sandbox containers
```

父级硬上限：

- POC `MemoryMax=2.75 GiB`
- `CPUQuota=400%`
- `TasksMax=576`

control：

- `MemoryHigh=256 MiB`
- `MemoryMax=384 MiB`
- CPU/IO weight 高于 workload

父级另留 128 MiB headroom，workload POC `MemoryMax=2.25 GiB`。

### 8.2 Prod 正式内存公式

正式值不能直接照抄 POC。数据来源优先级：

1. 已有至少 7 天、包含业务高峰的 `MemAvailable` 采样；或
2. 没有历史时先采样 72 小时，不在证据不足时开启客户流量。

定义：

```text
baseline = 同业务时段 MemAvailable 的 P1
parent_max = floor64MiB(min(2.75GiB, baseline - 1.25GiB))
workload_max = parent_max - 384MiB control - 128MiB headroom
workload_high = min(2.0GiB, 90% * workload_max)
workload_recovery = 80% * workload_max
workload_shed = 96% * workload_max
```

若 `parent_max < 2.0 GiB` 或 `workload_max < 1.5 GiB`，判定当前机器余量不够，
不得在 Prod 开 Sandbox 客户流量，需要先减负或扩容。

### 8.3 准入与回收

broker 每 2 秒采样：

- workload 达 `high` 连续 3 次：停止新任务。
- workload 低于 `recovery` 且宿主机恢复，持续 3 次：重新准入。
- workload 达 `shed` 连续 3 次：best-effort 结束最近启动、非
  `output_persisting` 的任务。
- Host `MemAvailable < 1.5 GiB` 连续 3 次：停止新任务。
- Host `MemAvailable < 1.0 GiB` 单次：立即停止准入并回收 Sandbox。
- `output_persisting` 最多给 10 秒完成输出；超过后也可回收并进入可恢复状态。

不承诺 Linux 内核 OOM victim 的选择顺序；上线依据是 cgroup ceiling 与主动准入，
不是“希望内核先杀 Sandbox”。

## 9. 磁盘隔离

- 文件：`/opt/numind-sandbox/storage/rootless-data.img`
- 大小：预分配 8 GiB
- 文件系统：ext4
- 挂载点：`/opt/numind-sandbox/data-root`
- 专用 systemd mount unit 必须先于 Rootless Docker。
- daemon 启动脚本同时验证 `mountpoint` 和 filesystem UUID；失败即退出。
- Docker `data-root` 只允许该 mountpoint。
- 70% bytes 或 inode 使用率告警。
- 85% 停止新任务、停止预热和拉新镜像。
- 清理只删除已终止 lease 和未被当前/回滚版本引用的镜像，不删当前或回滚镜像。
- 部署新镜像前检查解压所需空间；不足时发布失败，不边拉边赌。

## 10. 网络、文件和密钥隔离

- Sandbox 容器固定 `network=none`，不能访问公网、业务 Docker network、MySQL、
  Redis、metadata service 或宿主机端口。
- 不挂载任何宿主机业务目录。
- Skill 文件由用户 API 通过 broker CopyIn 写入 Sandbox tmpfs；不把 `/app/skills`
  直接 bind mount给 Sandbox。
- 输入附件由用户 API 下载/读取后通过有界 stream传入；broker不能自行读取 COS。
- 输出经 broker stream回用户 API，再由现有 COS 代码上传；broker没有 COS密钥。
- Rootless 用户权限负向测试必须覆盖：
  `/opt/numind/prod`、`/opt/numind/config`、证书目录、MySQL/Redis数据目录、
  `/var/lib/docker`、`/var/run/docker.sock` 和 Prod secrets.env。

## 11. 配置与镜像

不修改 `config_prod.yaml`。用户 API 通过部署环境变量覆盖：

```text
NUMIND_SANDBOX_BACKEND=broker
NUMIND_SANDBOX_BROKER_SOCKET=/run/numind-sandbox/sandboxd.sock
NUMIND_SANDBOX_POOL_MIN=1
NUMIND_SANDBOX_POOL_MAX_WAIT_MS=30000
NUMIND_SANDBOX_MEMORY_LIMIT_MB=512
NUMIND_SANDBOX_CPU_QUOTA=1
NUMIND_SANDBOX_PIDS_LIMIT=64
NUMIND_SANDBOX_TIMEOUT_SECONDS=30
NUMIND_SANDBOX_SESSION_TIMEOUT_SECONDS=300
NUMIND_SANDBOX_OUTPUT_MAX_SIZE_MB=50
```

这些值由 Prod secrets/config 完整性检查器校验，但镜像 digest、Rootless socket 和
cgroup 参数只在 broker 的 root-owned部署配置中定义，不能由 API 环境变量控制。

镜像必须使用：

```text
ccr.ccs.tencentyun.com/youshunumind/sandbox-skill@sha256:<发布时验证并记录>
```

禁止 Prod 使用可漂移 tag 作为最终运行标识。

## 12. 构建与部署

### 12.1 代码产物

同一 server release image 额外包含静态二进制：

- `/app/numind-sandboxd`
- `/app/numind-sandbox-reconcile`

普通 API 容器不执行这两个二进制。部署脚本从已 pull 且已校验的 release image
原子提取 broker binary 到 `/opt/numind-sandbox/bin`，避免新增第三套 artifact
分发链路。

### 12.2 一次性宿主机 provisioning

幂等脚本完成：

1. 创建不可登录 `numind-sandbox` 用户和 `numind-sandbox-api` group。
2. 安装/验证 rootless prerequisites，包括 `slirp4netns`。
3. 创建 subuid/subgid。
4. 创建、格式化并挂载 8 GiB data-root。
5. 安装 root/user systemd slice、mount、Rootless Docker、broker units。
6. 写入固定 image digest 和安全配置。
7. 验证 cgroup v2 delegation 与 memory/cpu/pids/io controllers。
8. 只把用户 API host UID 加入 broker socket allowlist。
9. 运行全部 socket、目录、网络和 Docker 权限负向测试。

脚本遇到已有不一致配置只报错，不静默覆盖。

### 12.3 日常发布顺序

1. pull release image。
2. 从 image 提取新 broker/reconcile binary 到临时路径并校验 SHA256。
3. 保存旧 broker binary 与配置快照。
4. broker 进入 drain；最长等待 300 秒。
5. 原子替换 binary，重启 broker，验证 `/healthz` 与 `/readyz`。
6. broker 不 ready：恢复旧 binary，用户 API 保持旧版本。
7. broker ready：部署用户 API，仅挂 broker socket。
8. 用户 API 健康检查与 Sandbox canary 均通过后提交发布成功。
9. 管理 API 单独部署轻量 admin runtime，不挂 broker。

### 12.4 回滚

- 先把用户 API 的 `NUMIND_SANDBOX_BACKEND` 切为 `disabled` 或回滚旧 API。
- broker drain 最多 300 秒，拒绝新任务。
- `output_persisting` 优先短暂排空，其余任务取消。
- 运行 `numind-sandbox-reconcile`，确认：
  - 无悬挂 running sandbox session
  - agent run 状态可恢复或已终止
  - Reserve/Reconcile 无重复扣费/退款
  - COS 上传可重试
- 恢复旧 broker binary（若协议/实现回滚需要）。
- 不回滚数据库 schema，因为本 feature 不新增 schema。
- Rootless daemon可保留停止状态；不删除 journal/data-root，便于审计和再次启用。

## 13. 可观测性

broker 指标：

- `sandbox_leases{state}`
- `sandbox_active_tasks`
- `sandbox_total_containers`
- `sandbox_queue_depth`
- `sandbox_borrow_wait_seconds`
- `sandbox_admission_rejected_total{reason}`
- `sandbox_exec_total{result}`
- `sandbox_exec_duration_seconds`
- `sandbox_copy_bytes_total{direction}`
- `sandbox_rpc_limit_rejected_total{limit}`
- `sandbox_workload_memory_bytes`
- `sandbox_host_mem_available_bytes`
- `sandbox_cgroup_events_total{event}`
- `sandbox_data_root_bytes_used_ratio`
- `sandbox_reconcile_pending`

结构化日志字段：

`request_id`、`lease_id`、`owner_id`、`agent_run_id`、
`sandbox_session_id`、`state_from`、`state_to`、`wait_ms`、
`termination_reason`、`pressure_state`。

日志不包含用户文件内容、模型 prompt、API key、env value 或完整命令输出。

## 14. 测试与验收

### 14.1 单元/合同测试

- Broker client完整实现 `DockerClient`。
- strict JSON拒绝所有未知/危险字段。
- SO_PEERCRED allow/deny。
- request id幂等。
- lease状态机合法/非法迁移。
- FIFO公平、30秒超时、context取消无slot泄漏。
- 总容器/active全局上限均为5。
- Copy文件数、单文件、总字节、连接、stream、速率、慢连接限制。
- tar path traversal、symlink、hardlink、device拒绝。
- Exec输出4MiB、超时、取消、OOM映射。
- journal crash/restart与60秒有界恢复。
- `NewAdminBiz` 不创建 Sandbox、不加载 Skills、不启动用户worker。

### 14.2 同形态集成测试

- cgroup v2路径、delegation、memory/cpu/pids/io实际值。
- 五个轻任务并发成功。
- 五个任务同时冲高，workload父级ceiling生效且宿主机不OOM。
- 第六个FIFO等待、成功接棒和超时。
- 两个 API client共享全局5槽位。
- Rootless daemon/broker停止时核心 API健康。
- Rootless用户读取Prod目录和主 Docker socket失败。
- 用户 API访问两套Docker socket失败。
- 管理API访问broker和两套Docker socket均失败。
- 恶意broker请求全部被拒绝并有审计。
- 8GiB data-root 70%/85%、inode、mount失败。
- API/broker重启、orphan、reconcile和回滚排空。

### 14.3 Dev 产品验收

QA 不参与。Dev 必须改成同形态 broker 链路后重新验收：

- 首页运行一个真实 SOP。
- AI 聊天普通消息正常。
- AI 智能体执行 `run_python`。
- 依次生成 PPTX、DOCX、XLSX、PDF并下载打开。
- 文档系统打开并导出。
- 插件/技能市场的依赖 Sandbox 功能。
- 飞书连接仍正常。
- 5个轻任务并发。
- 第6个任务显示产品文案。
- 人为停 broker 后普通聊天/SOP非Sandbox步骤继续。
- 浏览器5xx=0、核心容器restart=0、MySQL/Redis restart=0。

### 14.4 Prod 上线前证据

- 至少7天历史或72小时新内存采样与正式公式结果。
- 完整 Prod 数据库备份、校验和与可恢复性验证属于总上线流程，不能因本 feature
  无 schema 而省略。
- broker/socket/cgroup/data-root负向证据。
- Dev 同形态产品验收报告。
- 发布镜像、broker binary、Sandbox image digest全部记录。
- 产品负责人明确下达 Prod 执行授权。

## 15. 文件级实施范围

预计新增：

- `cmd/numind-sandboxd/main.go`
- `cmd/numind-sandbox-reconcile/main.go`
- `internal/numind/biz/sandbox/broker_client.go`
- `internal/numind/biz/sandbox/broker_protocol.go`
- `internal/numind/sandboxbroker/`（server、journal、lease、limits、pressure、recovery）
- `deploy/sandbox/`（systemd units、broker config、provision脚本）
- `scripts/cicd/test-sandbox-isolation.sh`

预计修改：

- `internal/numind/biz/sandbox/config.go`：新增 `broker` backend和socket/limit配置。
- `internal/numind/biz/biz.go`：按 backend选择client；新增轻量admin composition。
- `internal/numind/admin_router.go`：使用admin composition，Agent cancel传nil runner。
- `Dockerfile`：构建并包含两个静态二进制，Prod仍不安装Docker CLI。
- `scripts/cicd/deploy-remote.sh`：只给用户API挂broker socket，管理API不挂；broker
  install/health/rollback。
- `scripts/cicd/release.sh`与preflight tests：校验broker artifact、digest、Prod gate。

不修改：

- `config_prod.yaml`
- Prod 用户资料/积分/历史表数据
- 用户端与管理端前端布局
- 会议副驾与说话人分离
- QA部署链路

## 16. S2 人工确认点

进入 S3/S4 实现前，只需要确认以下三点：

1. 同一台 Prod 服务器新增 Rootless Docker + broker；不新增云服务器。
2. 只有用户 API 可访问 broker，管理 API完全不访问 Sandbox，并改成轻量运行角色。
3. 最多5个并发、单任务512MiB/1CPU/64进程；实际总内存上限按监控公式下调，
   内存紧张时优先暂停文件任务。

确认后进入实现；确认本设计不等于授权写入或部署 Prod。
