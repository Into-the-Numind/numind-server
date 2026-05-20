# Agent 模式 Sandbox Integration — 技术设计

> NDF v2 S2 spec | Feature: agent-mode-sandbox-integration | #4/14

## §1 目标与不变量

把 Phase 0 V5 ADR 决策的 Docker pool 沙箱真实落地：

| 不变量 | 说明 |
|--------|------|
| I1 | `AgentRunner.Run` 签名 + `RunRequest` struct 不变（#2 稳定契约） |
| I2 | `RunHooks struct` 三字段（PreToolCall / PostToolCall）不变（#2 稳定契约） |
| I3 | `HookAction` enum 三值（Continue / Stop / BlockingStop）不变（#2 稳定契约） |
| I4 | `FullTool` 36 方法 + `BaseTool` 默认实现不变（#3 稳定契约） |
| I5 | `aiservice.Chat / Embed / Rerank / OCR / ASR` 5 个入口不变（aiservice 唯一入口） |
| I6 | prod 永不部署沙箱真实代码（`sandbox.backend=disabled` 即等价 #3 stub 行为） |
| I7 | `config_prod.yaml` 不修改（rules §3）— 通过 default=disabled 自动 prod 安全 |
| I8 | bash_exec 工具元数据不变（IsDestructive=true / IsEnabled gated by ToolConfig.EnableSandbox） |

## §2 数据模型

### §2.1 agent_sandbox_session 表

```sql
CREATE TABLE IF NOT EXISTS agent_sandbox_session (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id         INT UNSIGNED NOT NULL                         COMMENT 'FK to user.id',
  agent_run_id    BIGINT UNSIGNED NULL                          COMMENT 'FK to agent_run.id, #4 可空（PreToolCall 写入），#11/#12 后必填',
  container_id    VARCHAR(128) NOT NULL                         COMMENT 'Docker container ID (12+ char hash)',
  image_tag       VARCHAR(128) NOT NULL DEFAULT 'python:3.11-slim',
  status          VARCHAR(20)  NOT NULL DEFAULT 'running'       COMMENT 'running | terminated | failed',
  mem_limit_mb    INT          NOT NULL DEFAULT 512,
  cpu_quota       DECIMAL(3,1) NOT NULL DEFAULT 1.0,
  exit_code       INT NULL                                      COMMENT 'NULL = still running',
  error_msg       TEXT NULL                                     COMMENT 'destroy 失败原因或 Exec err',
  started_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  ended_at        DATETIME(3) NULL,
  created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_ass_user_started (user_id, started_at),
  KEY idx_ass_status (status),
  KEY idx_ass_run (agent_run_id),
  CONSTRAINT chk_ass_status CHECK (status IN ('running', 'terminated', 'failed'))
);
```

Migration 双文件命名：`20260522_120000_create_agent_sandbox_session.sql` + `20260522_120000_create_agent_sandbox_session_rollback.sql`。

GORM model：`internal/pkg/model/agent_sandbox_session.go`：

```go
type AgentSandboxSession struct {
    ID           uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
    UserID       uint       `gorm:"not null;index:idx_ass_user_started" json:"user_id"`
    AgentRunID   *uint64    `gorm:"index:idx_ass_run" json:"agent_run_id,omitempty"` // pointer = NULL allowed
    ContainerID  string     `gorm:"size:128;not null" json:"container_id"`
    ImageTag     string     `gorm:"size:128;not null;default:python:3.11-slim" json:"image_tag"`
    Status       string     `gorm:"size:20;not null;default:running;index:idx_ass_status" json:"status"`
    MemLimitMB   int        `gorm:"not null;default:512" json:"mem_limit_mb"`
    CPUQuota     float64    `gorm:"type:decimal(3,1);not null;default:1.0" json:"cpu_quota"`
    ExitCode     *int       `json:"exit_code,omitempty"` // pointer = NULL allowed
    ErrorMsg     string     `gorm:"type:text" json:"error_msg,omitempty"`
    StartedAt    time.Time  `gorm:"not null;index:idx_ass_user_started;default:CURRENT_TIMESTAMP(3)" json:"started_at"`
    EndedAt      *time.Time `json:"ended_at,omitempty"`
    CreatedAt    time.Time  `gorm:"not null;default:CURRENT_TIMESTAMP(3)" json:"created_at"`
    UpdatedAt    time.Time  `gorm:"not null;default:CURRENT_TIMESTAMP(3)" json:"updated_at"`
}

func (AgentSandboxSession) TableName() string { return "agent_sandbox_session" }
```

AutoMigrate 注册：在 `internal/numind/helper.go` 加 `db.AutoMigrate(&model.AgentSandboxSession{})`（紧跟 #2 / #3 已有的 AgentRun / ToolDefinition 注册之后）。

### §2.2 Store 层

```go
// internal/numind/store/agent_sandbox_session.go
type IAgentSandboxSessionStore interface {
    Create(ctx context.Context, sess *model.AgentSandboxSession) error
    UpdateState(ctx context.Context, id uint64, status string, exitCode *int, errMsg string, endedAt *time.Time) error
    GetByContainerID(ctx context.Context, containerID string) (*model.AgentSandboxSession, error)
    ListByUser(ctx context.Context, userID uint, limit int) ([]model.AgentSandboxSession, error)
}

type agentSandboxSessionStore struct {
    db *gorm.DB
}

func NewAgentSandboxSessionStore(db *gorm.DB) IAgentSandboxSessionStore { ... }
```

注册到 `IStore` 接口（沿用 #3 已有的 Users / Agents / Tools 模式）：
- `store.IStore` interface 加 `AgentSandboxSessions() IAgentSandboxSessionStore`
- `datastore` struct 字段 `agentSandboxSessions IAgentSandboxSessionStore`
- 在 `NewDataStore` 内 wire（#2 / #3 已有 AgentRun / ToolDefinition wire 模式）

## §3 Sandbox biz 子包

### §3.1 包结构

```
internal/numind/biz/sandbox/
├── pool.go            # Pool interface + impl
├── pool_test.go
├── config.go          # SandboxConfig + 默认值
├── config_test.go
├── docker_client.go   # DockerClient interface + os/exec impl
├── docker_client_test.go
├── runner.go          # ExecCommand / WriteFile / ReadFile 原语
├── runner_test.go
├── security.go        # Docker run-time 参数组装
├── security_test.go
├── network.go         # NetworkPolicy（None / Allowlist stub）
├── network_test.go
├── seccomp.json       # Docker default + 黑名单 syscalls
├── errors.go          # ErrSandboxDisabled / ErrPoolExhausted / ErrSandboxOOM / ErrImageGenProviderNotConfigured ...
└── integration_test.go  // +build dockerintegration
```

### §3.2 Pool interface

```go
// internal/numind/biz/sandbox/pool.go

// Session represents one borrowed sandbox container.
type Session struct {
    ContainerID string
    ImageTag    string
    Config      SandboxConfig
    BorrowedAt  time.Time
    // mu protects status transitions (running → returned).
    mu       sync.Mutex
    returned bool
}

// Pool manages a warm pool of sandbox containers.
type Pool interface {
    Borrow(ctx context.Context) (*Session, error)
    Return(sess *Session, exitCode int, errMsg string) error
    Close() error
    Size() int // # currently in pool (excludes borrowed)
}

// Errors
var (
    ErrSandboxDisabled  = errors.New("sandbox backend disabled")
    ErrPoolExhausted    = errors.New("sandbox pool exhausted; try again later")
    ErrSessionReturned  = errors.New("session already returned")
    ErrSandboxOOM       = errors.New("sandbox container OOM killed")
)

// NewPool constructs a Pool given config and a DockerClient.
// If config.Backend == "disabled" returns a disabledPool whose Borrow returns ErrSandboxDisabled.
func NewPool(cfg SandboxConfig, dc DockerClient, logger Logger) Pool { ... }
```

实现：
- 启动时按 `cfg.PoolMin`（默认 5）调 `dc.Spawn(...)` 预热
- `Borrow` 从 channel 取一个；若耗尽 → 排队等 `cfg.PoolMaxWaitMs`（默认 30000ms）→ 超时返回 `ErrPoolExhausted`
- `Return` 调 `dc.Destroy(containerID)` + 异步 `dc.Spawn(...)` 补一个
- `Close` 关闭 channel + 等所有 borrowed session 归还（或强制销毁）

并发模型：buffered channel of `*Session`，size = `cfg.PoolMin * 2`（容纳 spike）；mu 保护 status 字段。

### §3.3 SandboxConfig

```go
// internal/numind/biz/sandbox/config.go

type Backend string

const (
    BackendDisabled Backend = "disabled"
    BackendDocker   Backend = "docker"
)

type NetworkPolicy string

const (
    NetworkPolicyNone      NetworkPolicy = "none"
    NetworkPolicyAllowlist NetworkPolicy = "allowlist" // v1 stub
)

type SandboxConfig struct {
    Backend         Backend
    PoolMin         int           // default 5
    PoolMaxWaitMs   int           // default 30000
    ImageTag        string        // default "python:3.11-slim"
    MemoryLimitMB   int           // default 512
    CPUQuota        float64       // default 1.0
    PIDsLimit       int           // default 64
    Timeout         time.Duration // default 30s (single exec)
    SessionTimeout  time.Duration // default 300s (whole sandbox session)
    NetworkPolicy   NetworkPolicy // default None
    AllowedDomains  []string      // empty for v1
    WorkdirSizeMB   int           // default 512 (tmpfs)
    ReadOnlyRootfs  bool          // default true
    Capabilities    []string      // default ["NET_BIND_SERVICE"]
    SeccompProfile  string        // path to seccomp.json (relative or absolute)
    AppArmorProfile string        // default "docker-default"
    UserSpec        string        // default "1000:1000"
}

// DefaultSandboxConfig 与蓝本 §4.6.2 对齐，BackendDisabled 是默认值。
var DefaultSandboxConfig = SandboxConfig{
    Backend:         BackendDisabled, // ★ prod safety
    PoolMin:         5,
    PoolMaxWaitMs:   30000,
    ImageTag:        "python:3.11-slim",
    MemoryLimitMB:   512,
    CPUQuota:        1.0,
    PIDsLimit:       64,
    Timeout:         30 * time.Second,
    SessionTimeout:  300 * time.Second,
    NetworkPolicy:   NetworkPolicyNone,
    AllowedDomains:  []string{},
    WorkdirSizeMB:   512,
    ReadOnlyRootfs:  true,
    Capabilities:    []string{"NET_BIND_SERVICE"},
    SeccompProfile:  "seccomp.json", // relative to biz/sandbox package dir
    AppArmorProfile: "docker-default",
    UserSpec:        "1000:1000",
}

// LoadFromViper reads viper.* keys "sandbox.backend" / "sandbox.pool_min" etc. and overrides defaults.
func LoadFromViper(v viperLike) SandboxConfig { ... }
```

`config_dev.yaml` 加新段 `sandbox:`（Backend=docker）；`config_prod.yaml` 不动 → Backend 仍是 disabled。

### §3.4 DockerClient

```go
// internal/numind/biz/sandbox/docker_client.go

// DockerClient is the minimal Docker primitive interface (mockable for tests).
type DockerClient interface {
    Spawn(ctx context.Context, cfg SpawnConfig) (containerID string, err error)
    Exec(ctx context.Context, containerID string, cmd []string, opts ExecOpts) (ExecResult, error)
    Destroy(ctx context.Context, containerID string) error
    Inspect(ctx context.Context, containerID string) (InspectResult, error)
}

type SpawnConfig struct {
    ImageTag        string
    SecurityOpts    []string // ["seccomp=...","apparmor=docker-default","no-new-privileges"]
    User            string   // "1000:1000"
    CapDrop         []string // ["ALL"]
    CapAdd          []string // ["NET_BIND_SERVICE"]
    Memory          string   // "512m"
    CPUs            string   // "1.0"
    PIDsLimit       int      // 64
    ReadOnly        bool
    Tmpfs           []string // ["/workdir:size=512m,uid=1000,gid=1000"]
    Network         string   // "none" | "bridge" | ""
    Detached        bool     // true (pool always detached)
}

type ExecOpts struct {
    Timeout time.Duration
    Workdir string
    Env     []string
}

type ExecResult struct {
    Stdout   string
    Stderr   string
    ExitCode int
    Duration time.Duration
}

type InspectResult struct {
    Status   string // "running" | "exited" | ...
    ExitCode int
    OOMKilled bool
}

// dockerCLIClient implements DockerClient by exec'ing the system "docker" binary.
type dockerCLIClient struct {
    dockerBin string // typically "docker"
    logger    Logger
}

func NewDockerCLIClient(logger Logger) DockerClient { ... }
```

`Spawn` 内部命令（伪代码）：

```
docker run \
  --detach \
  --security-opt seccomp=<absolute path to seccomp.json> \
  --security-opt apparmor=docker-default \
  --security-opt no-new-privileges \
  --user 1000:1000 \
  --cap-drop=ALL --cap-add=NET_BIND_SERVICE \
  --memory=512m --cpus=1.0 --pids-limit=64 \
  --read-only --tmpfs /workdir:size=512m,uid=1000,gid=1000 \
  --network=none \
  python:3.11-slim \
  /bin/sh -c "sleep 600"
```

`sleep 600` 让容器待命；后续 `docker exec` 跑用户命令。

`Exec`：

```
docker exec --workdir /workdir --user 1000:1000 <containerID> /bin/sh -c "<command>"
```

`Destroy`：

```
docker rm -f <containerID>
```

### §3.5 Runner 原语

```go
// internal/numind/biz/sandbox/runner.go

// ExecCommand runs a shell command inside the sandbox session and returns the result.
func ExecCommand(ctx context.Context, sess *Session, cmd string, dc DockerClient) (ExecResult, error) {
    return dc.Exec(ctx, sess.ContainerID, []string{"/bin/sh", "-c", cmd}, ExecOpts{
        Timeout: sess.Config.Timeout,
        Workdir: "/workdir",
        User:    sess.Config.UserSpec,
    })
}

// WriteFile / ReadFile: v1 仅占位（声明签名 + 单测验证存在），实现延迟到 follow-up（学员文件上传 / 输出下载）
func WriteFile(ctx context.Context, sess *Session, path string, content []byte, dc DockerClient) error {
    return ErrNotImplemented
}
func ReadFile(ctx context.Context, sess *Session, path string, dc DockerClient) ([]byte, error) {
    return nil, ErrNotImplemented
}
```

### §3.6 Security

```go
// internal/numind/biz/sandbox/security.go

// BuildSpawnConfig assembles SpawnConfig for a sandbox container per SandboxConfig + V5 ADR Q2.
func BuildSpawnConfig(cfg SandboxConfig, absSeccompPath string) SpawnConfig {
    return SpawnConfig{
        ImageTag: cfg.ImageTag,
        SecurityOpts: []string{
            "seccomp=" + absSeccompPath,
            "apparmor=" + cfg.AppArmorProfile,
            "no-new-privileges",
        },
        User:      cfg.UserSpec,
        CapDrop:   []string{"ALL"},
        CapAdd:    cfg.Capabilities,
        Memory:    fmt.Sprintf("%dm", cfg.MemoryLimitMB),
        CPUs:      fmt.Sprintf("%.1f", cfg.CPUQuota),
        PIDsLimit: cfg.PIDsLimit,
        ReadOnly:  cfg.ReadOnlyRootfs,
        Tmpfs:     []string{fmt.Sprintf("/workdir:size=%dm,uid=1000,gid=1000", cfg.WorkdirSizeMB)},
        Network:   string(cfg.NetworkPolicy), // "none"
        Detached:  true,
    }
}

// ValidateSecurityChecklist asserts the SpawnConfig matches the V5 ADR Q2 minimum hardening list.
// Returns the missing checks (empty = OK).
func ValidateSecurityChecklist(s SpawnConfig) []string {
    var missing []string
    if !contains(s.SecurityOpts, "seccomp=") { missing = append(missing, "seccomp profile") }
    if !contains(s.SecurityOpts, "apparmor=") { missing = append(missing, "apparmor profile") }
    if !contains(s.SecurityOpts, "no-new-privileges") { missing = append(missing, "no-new-privileges") }
    if s.User != "1000:1000" { missing = append(missing, "non-root user") }
    if !slices.Contains(s.CapDrop, "ALL") { missing = append(missing, "cap-drop ALL") }
    if !s.ReadOnly { missing = append(missing, "read-only rootfs") }
    if !hasTmpfs(s.Tmpfs, "/workdir") { missing = append(missing, "tmpfs /workdir") }
    if s.Memory == "" { missing = append(missing, "memory limit") }
    if s.CPUs == "" { missing = append(missing, "cpu limit") }
    if s.PIDsLimit == 0 { missing = append(missing, "pids limit") }
    return missing
}
```

### §3.7 seccomp.json

embed via `//go:embed seccomp.json` ，运行时拷贝到临时路径供 docker --security-opt seccomp= 引用（docker CLI 需要文件路径不是 stdin）。

内容（基础结构）：
```json
{
  "defaultAction": "SCMP_ACT_ALLOW",
  "architectures": ["SCMP_ARCH_X86_64", "SCMP_ARCH_X86", "SCMP_ARCH_X32"],
  "syscalls": [
    {"names": ["ptrace","mount","umount","umount2","pivot_root","unshare","keyctl","add_key","request_key","bpf","clone3","personality","userfaultfd","perf_event_open","kexec_load","init_module","delete_module","finit_module","reboot","sethostname","setdomainname","iopl","ioperm","quotactl"], "action": "SCMP_ACT_ERRNO"}
  ]
}
```

理由：默认 ALLOW + 显式 deny 危险 syscall。Docker default profile 已 deny 大多数，但显式追加保险；后续 #6 可深化。

### §3.8 Network

```go
// internal/numind/biz/sandbox/network.go

// NetworkPolicyForBackend resolves the docker --network flag value.
func NetworkPolicyForBackend(p NetworkPolicy) (string, error) {
    switch p {
    case NetworkPolicyNone:
        return "none", nil
    case NetworkPolicyAllowlist:
        return "", ErrAllowlistNotImplemented // v1 stub; #14 落地
    default:
        return "none", nil
    }
}
```

## §4 bashvalidator 子包

### §4.1 来源与提取

Phase 0 V3 已实现 8 个 P0 Bash validator，位于 `numind-server/cmd/agent-phase0-bash-validator/`（main 包不可 import）。**S4 编码阶段**：

1. 读 `cmd/agent-phase0-bash-validator/main.go`（及拆分的 validator.go 等）
2. 把 validator 函数（非 main 函数）拷到 `internal/numind/biz/agent/bashvalidator/validator.go`
3. 把对应单测拷到 `internal/numind/biz/agent/bashvalidator/validator_test.go`（attack vectors 单测保留）
4. **保留 cmd/agent-phase0-bash-validator** 作 Phase 0 历史 demo 引用（删除会破坏 Phase 0 acceptance record 引用）
5. 在子包内提供 entry `Validate(command string) (allow bool, reason string)` 调用 8 个 validator

### §4.2 8 个 P0 Validator 清单（V3 验证物）

| # | Validator | 拦截 |
|---|-----------|------|
| 1 | NoDangerousPath | `rm -rf /` / `cat /etc/passwd` |
| 2 | NoFork / NoExec / NoChroot | `bash -c "while :; do :; done"` |
| 3 | NoCurlPipeShell | `curl example.com \| bash` |
| 4 | NoBashOperators | `&& ; \|\| \| > <` 元符号（受限白名单）|
| 5 | NoSSH | `ssh user@host` |
| 6 | NoNetcat | `nc -lvp 1234` |
| 7 | NoCommandSubstitution | `` `cmd` `` / `$(cmd)` |
| 8 | NoFileWrite | `> /sensitive_path` |

**精确清单 grep cmd/agent-phase0-bash-validator/ 实际函数名定稿**（S4 编码阶段第一个 task）。

### §4.3 调用路径（"hook 做 infra / Execute 做 business" 职责切分）

**关键**：避免 PreToolCall 和 bash_exec.Execute 都调 Pool.Borrow 导致**双重 Borrow + 资源泄漏**。设计上 **hook = 沙箱生命周期 + 审计**，Execute = **业务逻辑（解析 + 校验 + 调 ExecCommand）**。Execute 从 ctx 取 session（hook 注入）。

```go
// internal/numind/biz/agent/sandbox_ctx.go (new file)

type sandboxSessionCtxKey struct{}

// WithSandboxSession returns ctx with sandbox session attached.
// Used by SandboxHookManager.preToolCall to hand off the borrowed session to bash_exec.Execute.
func WithSandboxSession(ctx context.Context, sess *sandbox.Session) context.Context {
    return context.WithValue(ctx, sandboxSessionCtxKey{}, sess)
}

// SandboxSessionFromContext returns the session stored in ctx, or nil if absent.
func SandboxSessionFromContext(ctx context.Context) *sandbox.Session {
    if v, ok := ctx.Value(sandboxSessionCtxKey{}).(*sandbox.Session); ok {
        return v
    }
    return nil
}
```

```go
// internal/numind/biz/agent/tool_bash_exec.go (升级)

type bashExecTool struct {
    BaseTool
    dc sandbox.DockerClient // injected at factory_platform.go construction time
}

func (t *bashExecTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
    var args struct {
        Command string `json:"command"`
    }
    if err := json.Unmarshal(input, &args); err != nil {
        return nil, fmt.Errorf("bash_exec: parse input: %w", err)
    }

    // 1) Bash validator gate (8 P0 from Phase 0 V3)
    if allow, reason := bashvalidator.Validate(args.Command); !allow {
        return errorResult(fmt.Sprintf("命令被拒绝（安全策略）: %s", reason)), nil
    }

    // 2) Get sandbox session — borrowed by SandboxHook.PreToolCall
    sess := SandboxSessionFromContext(ctx)
    if sess == nil {
        // Hook didn't borrow (pool disabled / exhausted / runID=0) → friendly error.
        // bash_exec gracefully degrades to "Sandbox unavailable" message that LLM
        // can read and decide next action.
        return errorResult("沙箱当前不可用，请稍后重试"), nil
    }

    // 3) Exec command inside sandbox
    res, execErr := sandbox.ExecCommand(ctx, sess, args.Command, t.dc)
    // Note: Pool.Return is called by SandboxHook.PostToolCall (M8), not here.
    // bash_exec.Execute does NOT call Pool.Return — separation of concerns.

    if execErr != nil {
        // Return execErr so PostToolCall sees it and marks audit row 'failed'.
        return errorResult(fmt.Sprintf("沙箱执行失败: %v", execErr)), execErr
    }

    out, _ := json.Marshal(map[string]interface{}{
        "stdout":      res.Stdout,
        "stderr":      res.Stderr,
        "exit_code":   res.ExitCode,
        "duration_ms": res.Duration.Milliseconds(),
    })
    return out, nil
}
```

**职责切分**：

| 角色 | 干什么 |
|------|--------|
| SandboxHook.PreToolCall | (1) 判定是否 bash_exec（按 tool.Info().Name）;(2) pool.Borrow 拿一个 session;(3) 写 agent_sandbox_session row（status=running）;(4) `ctx = WithSandboxSession(ctx, sess)` 注入到 ctx 给 Execute 用;(5) 记录到 sync.Map[runID+toolName] 便于 PostToolCall 关闭 |
| bash_exec.Execute | (1) 解析 input;(2) bashvalidator 校验;(3) 从 ctx 取 session;(4) sandbox.ExecCommand 跑命令;(5) 序列化结果返回 |
| SandboxHook.PostToolCall | (1) 判定是否 bash_exec;(2) 从 sync.Map 取 sess;(3) pool.Return（destroy + 异步补 spawn）;(4) 更新 agent_sandbox_session row（status=terminated/failed + exit_code + error_msg） |

**异常路径**：
- PreToolCall borrow 失败 → 不写 ctx session → Execute 见 sess == nil → 返回友好错误 → PostToolCall 见 sync.Map 没条目 → 直接 Continue
- bash_exec.Execute panic → defer 在 SandboxHookManager 中的 LoadAndDelete 不会触发（panic 跨过 PostToolCall）→ **sync.Map 残留**。**缓解**：`Pool.Close()` 时遍历 sync.Map 强制销毁所有遗留 session；同时 sandbox.Session 自带 `SessionTimeout` (300s)，超时由 Pool 主动销毁
- PostToolCall sessStore.UpdateState 失败 → log + Pool.Return 仍执行（容器先销毁）→ 审计 row 残留 status='running'，但 ended_at 不会更新；监控可识别（agent_sandbox_session.status='running' 且 started_at < now-10min）作为 "孤儿 session" 报警

**ctx propagation 注意点**：Eino adapter `InvokableRun` 调 hook PreToolCall 时拿到的 ctx 与调 Execute 时拿到的 ctx 是**同一个**（adapter 内部不 derive 新 ctx）。`WithSandboxSession(ctx, sess)` 必须在 PreToolCall 内 `ctx = ...` 赋值并 **return 给 adapter 让其传给 Execute**。但 RunHooks.PreToolCall 当前签名是 `func(ctx, t, input) (HookAction, error)` — **不能返回新 ctx**。

**解决**：扩展 RunHooks 签名？不行，违反 I2 不变量。**真实方案**：

(a) **adapter 内部维护"shared ctx"**：adapter 调 PreToolCall 前 derive ctx，PreToolCall 用 `*context.Context` 指针？不符合 Go ctx 习惯。

(b) **PreToolCall 不直接 ctx.WithValue**：改用 sync.Map[runID+toolName]，bash_exec.Execute 在 ctx 中找不到时**降级**到 sync.Map 查找。但 Execute 不知道 toolName 名字（自己已知道 "bash_exec"），知道 runID（从 ctx），可以查 `sync.Map["<runID>|bash_exec"]`。**这个方案可行**。

**最终选 (b)**：sync.Map 不仅给 PostToolCall 用，也给 bash_exec.Execute 用作"session lookup"。

```go
// internal/numind/biz/agent/sandbox_ctx.go (revised — no ctx.WithValue for session)

// SandboxSessionFor looks up the borrowed session for the current runID + toolName.
// Used by bash_exec.Execute to get the session that PreToolCall borrowed.
func (m *SandboxHookManager) SandboxSessionFor(runID uint64, toolName string) *sandbox.Session {
    v, ok := m.borrows.Load(m.key(runID, toolName))
    if !ok { return nil }
    return v.(*sandboxBorrow).sess
}

// Package-level helper: bash_exec.Execute uses this to find its session via the
// default hook manager (set at biz.go wire time via SetDefaultHookManager).
var defaultHookManager *SandboxHookManager
var defaultHookManagerMu sync.RWMutex

func SetDefaultHookManager(m *SandboxHookManager) {
    defaultHookManagerMu.Lock()
    defaultHookManagerMu.Unlock() // ★ defer in real code
    defaultHookManager = m
}

func sandboxSessionForCurrentCall(ctx context.Context, toolName string) *sandbox.Session {
    defaultHookManagerMu.RLock()
    defer defaultHookManagerMu.RUnlock()
    if defaultHookManager == nil { return nil }
    runID := RunIDFromContext(ctx)
    if runID == 0 { return nil }
    return defaultHookManager.SandboxSessionFor(runID, toolName)
}
```

bash_exec.Execute 用：
```go
sess := sandboxSessionForCurrentCall(ctx, "bash_exec")
if sess == nil { return errorResult("沙箱当前不可用..."), nil }
```

biz.go wire：
```go
sandboxHooksManager := agent.NewSandboxHookManager(sandboxPool, deps.Store.AgentSandboxSessions())
agent.SetDefaultHookManager(sandboxHooksManager)
sandboxHooks := sandboxHooksManager.AsRunHooks()
// ...
```

`NewSandboxHooks(pool, sessStore) *RunHooks` 简化为 `NewSandboxHookManager(pool, sessStore) *SandboxHookManager` + `m.AsRunHooks() *RunHooks` — manager 可被多处复用 + 提供 SandboxSessionFor 给 Execute。

**这是 #4 设计的精髓**，**S4 编码必须按此 spec 实现**。

## §5 RunHooks 工厂与 ctx 传递

### §5.1 ctx helper

```go
// internal/numind/biz/agent/context.go (new file)

type runIDCtxKey struct{}

// WithRunID returns ctx with agent runID stored.
func WithRunID(ctx context.Context, runID uint64) context.Context {
    return context.WithValue(ctx, runIDCtxKey{}, runID)
}

// RunIDFromContext returns the runID stored in ctx, or 0 if absent.
func RunIDFromContext(ctx context.Context) uint64 {
    if v, ok := ctx.Value(runIDCtxKey{}).(uint64); ok {
        return v
    }
    return 0
}
```

### §5.2 runner.go 改造（1 行 ctx 注入）

```go
// runner.go Run() 内部，在 r.runStore.Create(ctx, run) 之后：
ctx = WithRunID(ctx, run.ID)
```

`Run` 函数签名不变。

### §5.3 factory_sandbox_hooks.go

```go
// internal/numind/biz/agent/factory_sandbox_hooks.go

import (
    "context"
    "fmt"
    "sync"
    "time"
    einotool "github.com/cloudwego/eino/components/tool"
    "numind-server/internal/numind/biz/sandbox"
    "numind-server/internal/numind/store"
    "numind-server/internal/pkg/log"
    "numind-server/internal/pkg/middleware"
    "numind-server/internal/pkg/model"
)

// sandboxBorrow tracks an in-flight sandbox session keyed by runID+toolName.
type sandboxBorrow struct {
    sess      *sandbox.Session
    sessionID uint64 // agent_sandbox_session.id
}

// SandboxHookManager holds the sync.Map and exposes a *RunHooks + session lookup helper.
type SandboxHookManager struct {
    pool      sandbox.Pool
    sessStore store.IAgentSandboxSessionStore
    borrows   sync.Map // key=string(runID+"|"+toolName), val=*sandboxBorrow
}

// NewSandboxHookManager constructs the manager.
func NewSandboxHookManager(pool sandbox.Pool, sessStore store.IAgentSandboxSessionStore) *SandboxHookManager {
    return &SandboxHookManager{pool: pool, sessStore: sessStore}
}

// AsRunHooks returns a *RunHooks bound to this manager.
func (m *SandboxHookManager) AsRunHooks() *RunHooks {
    return &RunHooks{
        PreToolCall:  m.preToolCall,
        PostToolCall: m.postToolCall,
    }
}

// SandboxSessionFor returns the borrowed session for (runID, toolName), or nil.
// Used by bash_exec.Execute via package-level sandboxSessionForCurrentCall().
func (m *SandboxHookManager) SandboxSessionFor(runID uint64, toolName string) *sandbox.Session {
    v, ok := m.borrows.Load(m.key(runID, toolName))
    if !ok { return nil }
    return v.(*sandboxBorrow).sess
}

func (m *SandboxHookManager) key(runID uint64, toolName string) string {
    return fmt.Sprintf("%d|%s", runID, toolName)
}

func (m *SandboxHookManager) preToolCall(ctx context.Context, t einotool.BaseTool, _ string) (HookAction, error) {
    info, err := t.Info(ctx)
    if err != nil {
        log.Warnw("SandboxHook.PreToolCall: tool.Info failed", "error", err)
        return HookActionContinue, nil
    }
    if info.Name != "bash_exec" {
        return HookActionContinue, nil
    }
    runID := RunIDFromContext(ctx)
    if runID == 0 {
        log.Warnw("SandboxHook.PreToolCall: no runID in ctx; bash_exec without runID — skip sandbox audit")
        return HookActionContinue, nil
    }
    sess, err := m.pool.Borrow(ctx)
    if err != nil {
        // Pool exhausted / disabled — let bash_exec.Execute decide friendly error
        log.Warnw("SandboxHook.PreToolCall: Pool.Borrow failed", "error", err)
        return HookActionContinue, nil
    }
    var arid *uint64
    if runID > 0 {
        arid = &runID
    }
    userID, _ := middleware.UserIDFromCtx(ctx) // returns (uint, bool); ignoring ok = 0 default
    record := &model.AgentSandboxSession{
        UserID:      userID,
        AgentRunID:  arid,
        ContainerID: sess.ContainerID,
        ImageTag:    sess.ImageTag,
        Status:      "running",
        MemLimitMB:  sess.Config.MemoryLimitMB,
        CPUQuota:    sess.Config.CPUQuota,
        StartedAt:   sess.BorrowedAt,
    }
    if err := m.sessStore.Create(ctx, record); err != nil {
        log.Warnw("SandboxHook.PreToolCall: sessStore.Create failed", "error", err)
        // Return session to pool to avoid leak
        _ = m.pool.Return(sess, -1, "audit Create failed")
        return HookActionContinue, nil
    }
    m.borrows.Store(m.key(runID, info.Name), &sandboxBorrow{sess: sess, sessionID: record.ID})
    return HookActionContinue, nil
}

func (m *SandboxHookManager) postToolCall(ctx context.Context, t einotool.BaseTool, _ string, execErr error) (HookAction, error) {
    info, err := t.Info(ctx)
    if err != nil {
        log.Warnw("SandboxHook.PostToolCall: tool.Info failed", "error", err)
        return HookActionContinue, nil
    }
    if info.Name != "bash_exec" {
        return HookActionContinue, nil
    }
    runID := RunIDFromContext(ctx)
    if runID == 0 {
        return HookActionContinue, nil
    }
    val, ok := m.borrows.LoadAndDelete(m.key(runID, info.Name))
    if !ok {
        // Pre wasn't called (e.g. pool was exhausted) — nothing to do
        return HookActionContinue, nil
    }
    borrow := val.(*sandboxBorrow)
    status := "terminated"
    var exitCode *int
    var errMsg string
    if execErr != nil {
        status = "failed"
        errMsg = execErr.Error()
        ec := -1
        exitCode = &ec
    }
    _ = m.pool.Return(borrow.sess, intDeref(exitCode), errMsg)
    now := time.Now()
    if err := m.sessStore.UpdateState(ctx, borrow.sessionID, status, exitCode, errMsg, &now); err != nil {
        log.Warnw("SandboxHook.PostToolCall: sessStore.UpdateState failed", "error", err)
    }
    return HookActionContinue, nil
}

func intDeref(p *int) int {
    if p == nil {
        return 0
    }
    return *p
}
```

### §5.4 userID 来源

`middleware.UserIDFromCtx(ctx) (uint, bool)` 已存在（`internal/pkg/middleware/context_keys.go` line 19）。#2 runner.go Run() 开头已调 `ctx = middleware.NewContextWithUserID(ctx, req.UserID)`。

PreToolCall 内直接 `userID, _ := middleware.UserIDFromCtx(ctx)`；若返回 0（罕见 — ctx 未注入），写 audit row 时 user_id = 0（DB chk_status 不约束 user_id 范围；运维侧识别 user_id=0 为"无主 sandbox 调用"作监控）。

## §6 adapter 升级（方案 A）

### §6.1 升级 adaptFullToEinoTool

```go
// internal/numind/biz/agent/adapter_full_to_eino.go (升级)

import (
    "context"
    "fmt"
    "io"
    einotool "github.com/cloudwego/eino/components/tool"
    "github.com/cloudwego/eino/schema"
    "numind-server/internal/pkg/log"
)

// adaptFullToEinoTool wraps a FullTool as Eino's tool.InvokableTool.
// hooks may be nil (no-hook behavior, identical to #3 original).
func adaptFullToEinoTool(ft FullTool, hooks *RunHooks) einotool.InvokableTool {
    return &fullToolEinoAdapter{ft: ft, hooks: hooks}
}

type fullToolEinoAdapter struct {
    ft    FullTool
    hooks *RunHooks
}

var _ einotool.InvokableTool = (*fullToolEinoAdapter)(nil)

func (a *fullToolEinoAdapter) Info(_ context.Context) (*schema.ToolInfo, error) {
    return &schema.ToolInfo{
        Name: a.ft.Name(),
        Desc: a.ft.Description(),
        ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
    }, nil
}

func (a *fullToolEinoAdapter) InvokableRun(ctx context.Context, args string, _ ...einotool.Option) (string, error) {
    input := ToolInput(args)

    // PreToolCall
    if a.hooks != nil && a.hooks.PreToolCall != nil {
        action, err := a.hooks.PreToolCall(ctx, a, args)
        if err != nil {
            return "", fmt.Errorf("PreToolCall: %w", err)
        }
        if action != HookActionContinue {
            // Stop / BlockingStop short-circuit Execute
            return "", fmt.Errorf("tool execution stopped by hook: action=%d", action)
        }
    }

    // Execute
    result, execErr := a.ft.Execute(ctx, input)
    var output string
    if result != nil {
        output = string(result)
    }

    // PostToolCall (always called for cleanup, even on Execute error)
    if a.hooks != nil && a.hooks.PostToolCall != nil {
        if _, postErr := a.hooks.PostToolCall(ctx, a, output, execErr); postErr != nil {
            log.Warnw("PostToolCall failed", "tool", a.ft.Name(), "post_err", postErr, "exec_err", execErr)
            // PostToolCall error doesn't override execErr; if execErr is nil, raise postErr
            if execErr == nil {
                return output, fmt.Errorf("PostToolCall: %w", postErr)
            }
        }
    }

    if execErr != nil {
        return output, execErr
    }
    return output, nil
}
```

### §6.2 runner.go 装配升级

```go
// runner.go Run() 内 (#3 已有 loop)
for _, name := range req.ToolNames {
    if ft, ok := r.registry.GetTool(name); ok {
        einoTools = append(einoTools, adaptFullToEinoTool(ft, req.Hooks)) // ★ 加 hooks 参数
    }
}
```

仅装配代码 1 处改动。`RunRequest.Hooks *RunHooks` 字段已在 #2 存在；本 feature 通过 `biz.go` wire `NewSandboxHooks(pool, sessStore)` 作为默认 hooks。

### §6.3 biz.go wire

```go
// internal/numind/biz/biz.go (升级 NewBiz 或类似入口)

func NewBiz(deps Deps) *Biz {
    // ...
    sandboxConfig := sandbox.LoadFromViper(viper.GetViper())
    dockerClient := sandbox.NewDockerCLIClient(deps.Logger)
    sandboxPool := sandbox.NewPool(sandboxConfig, dockerClient, deps.Logger)
    sandboxHooks := agent.NewSandboxHooks(sandboxPool, deps.Store.AgentSandboxSessions())
    agentRunner := agent.NewAgentRunner(deps.Store.AgentRuns(), deps.AgentToolRegistry, agent.WithDefaultHooks(sandboxHooks))
    // ...
}
```

**新增 `agent.WithDefaultHooks(hooks)` option pattern**（agentRunner 字段加 defaultHooks *RunHooks）：runner.Run() 调用时若 `req.Hooks == nil` 则使用 `r.defaultHooks`，否则使用 req.Hooks。这是 #4 新增的"装配方便"路径，**不改 AgentRunner.Run 签名 / RunRequest 结构**。

```go
// runner.go 加 option
type RunnerOption func(*agentRunner)

func WithDefaultHooks(h *RunHooks) RunnerOption {
    return func(r *agentRunner) { r.defaultHooks = h }
}

func NewAgentRunner(runStore store.IAgentRunStore, registry AgentToolRegistry, opts ...RunnerOption) AgentRunner {
    r := &agentRunner{ /* existing */ }
    for _, o := range opts { o(r) }
    return r
}

// Run() 内：
effectiveHooks := req.Hooks
if effectiveHooks == nil {
    effectiveHooks = r.defaultHooks
}
// adapter 装配用 effectiveHooks 而非 req.Hooks
```

## §7 image_gen 处理

```go
// internal/numind/biz/agent/tool_image_gen.go

func (t *imageGenTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
    // v1: image provider registration deferred to follow-up
    return nil, ErrImageGenProviderNotConfigured
}

// errors.go
var ErrImageGenProviderNotConfigured = errors.New("image generation provider not configured; please contact admin")
```

**不**新增 aiservice.ImageGenerate / ImageProvider — scope 控制。FullTool 接口不变（IsEnabled / NarrationVerb 等保留 #3 实现）。

## §8 决策点（spec 阶段）

| # | 决策 | 选项 | 选定 |
|---|------|------|------|
| D1 | DooD vs DinD | (a) bind mount `/var/run/docker.sock` 进 numind-server 容器 + 装 docker CLI（DooD）;(b) Docker-in-Docker（嵌套 daemon） | **(a) DooD** — 简单 + dev 风险可控；prod 不上 |
| D2 | Docker SDK Go vs CLI | (a) `github.com/docker/docker/client` SDK;(b) `os/exec docker` | **(b) CLI** — 零依赖膨胀；命令字符串易审计 |
| D3 | image_gen 范围 | (a) #4 内完整实现含 aiservice.ImageGenerate;(b) #4 友好错误 + 完整实现 follow-up | **(b) 友好错误** — 范围控制 |
| D4 | seccomp profile 严格度 | (a) Docker default 不改;(b) Docker default + 追加黑名单;(c) 完全白名单 | **(b) default + 黑名单** — 保守起点，按需收紧 |
| D5 | bashvalidator 来源 | (a) 在 #4 重写;(b) 从 V3 cmd 提取代码到子包 | **(b) 提取** — 复用 V3 验证 + 测试 |
| D6 | Pool Return 死锁防护 | (a) Pool.Return 内部 once-semantic（sess.returned bool）;(b) 调用方负责单调用 | **(a) once-semantic** — bash_exec defer Return + PostToolCall Return 双调用安全 |
| D7 | hook 跨 Pre/Post state 传递 | (a) ctx.WithValue;(b) sync.Map[runID+toolName];(c) RunRequest.Hooks 闭包 | **(b) sync.Map[runID+toolName]** — ctx 中无法存可变状态；闭包不支持多 tool 实例 |
| D8 | runner.go ctx runID 注入 | (a) WithRunID(ctx, run.ID) 1 行;(b) RunRequest 加 RunID 字段;(c) AgentRunner 接口加 RunID 参数 | **(a) WithRunID** — 不改签名 |
| D9 | adapter 升级签名兼容性 | (a) 加 hooks 参数;(b) 新建 adaptFullToEinoToolWithHooks;(c) wrap pattern | **(a) 加参数** — 简单清晰；#3 测试传 nil 不破坏 |
| D10 | 默认 SANDBOX_BACKEND | (a) disabled（prod 安全） | **disabled** — config 不写 = 安全 |

## §9 配置（config_dev.yaml 新增段）

```yaml
sandbox:
  backend: docker            # disabled | docker
  pool_min: 5
  pool_max_wait_ms: 30000
  image_tag: python:3.11-slim
  memory_limit_mb: 512
  cpu_quota: 1.0
  pids_limit: 64
  timeout_seconds: 30
  session_timeout_seconds: 300
  network_policy: none       # none | allowlist (stub)
  workdir_size_mb: 512
  read_only_rootfs: true
  user_spec: "1000:1000"
  capabilities: ["NET_BIND_SERVICE"]
  apparmor_profile: "docker-default"
  seccomp_profile: "seccomp.json"   # relative to biz/sandbox; absolute path resolved at startup
```

`config_prod.yaml` 不动；缺 `sandbox` 段 = LoadFromViper 用 default = backend=disabled = prod 安全。

## §10 Dockerfile / 部署脚本变更

### §10.1 Dockerfile

dev 阶段需要装 docker CLI（client only，不是 daemon）。方案：

(a) **多 stage 单 Dockerfile + build arg**：
```dockerfile
FROM golang:1.24-alpine AS builder
# ... existing build ...

FROM alpine:latest AS runtime
ARG WITH_DOCKER_CLI=false
COPY --from=builder /app/numind /app/numind
COPY --from=builder /app/seccomp.json /app/seccomp.json
RUN if [ "$WITH_DOCKER_CLI" = "true" ]; then \
      apk add --no-cache docker-cli; \
    fi
CMD ["/app/numind"]
```

dev 构建：`docker build --build-arg WITH_DOCKER_CLI=true ...`
prod 构建：默认 false → 不装

(b) **dev / prod 两个 Dockerfile**：维护成本高，不选

**选 (a)**。**S4 编码**改 Dockerfile + dev 部署脚本（scripts/cicd/release.sh dev 路径）传 build arg。

### §10.2 dev 部署脚本

需要在 docker run 时加 `-v /var/run/docker.sock:/var/run/docker.sock`。看现有 release.sh 是否已通过 env 配置 docker run flags。

**S4 编码阶段任务**：grep `scripts/cicd/release.sh`（或 dev 部署机上的 run 脚本），找到 `docker run` 行，加 volume mount。

## §11 测试策略

| 模块 | 单测 | race | docker integration |
|------|------|------|------|
| sandbox.config | ✅ | — | — |
| sandbox.docker_client (mock) | ✅ | — | — |
| sandbox.docker_client (real CLI) | — | — | ✅ build tag `dockerintegration` |
| sandbox.pool | ✅ | ✅ 10 goroutine Borrow | — |
| sandbox.runner | ✅ | — | ✅ docker integration |
| sandbox.security | ✅（编译期断言 ValidateSecurityChecklist） | — | — |
| sandbox.network | ✅ | — | — |
| store.agent_sandbox_session | ✅（SQLite） | — | — |
| agent.bashvalidator | ✅（继承 V3 attack vectors） | — | — |
| agent.tool_bash_exec | ✅（mock pool） | — | ✅ docker integration（echo / ls / python -c） |
| agent.tool_image_gen | ✅（friendly error） | — | — |
| agent.factory_sandbox_hooks | ✅（mock pool + mock store） | ✅ | — |
| agent.adapter_full_to_eino（升级） | ✅（验证 hooks 调用时机） | — | — |
| agent.runner（ctx 注入） | ✅（验证 WithRunID 在 Run 内调用） | — | — |
| biz.go wire（NewSandboxHooks + WithDefaultHooks） | ✅（mock pool + mock registry） | — | — |

CI 跑 `go test -race ./...`；`-tags dockerintegration` 仅 dev / 本地手工跑。

## §12 不变量验证

| # | 不变量 | 验证方式 |
|---|--------|----------|
| I1 | AgentRunner.Run 签名不变 | grep diff：`func (r *agentRunner) Run(ctx context.Context, req RunRequest)` 完全相同 |
| I2 | RunHooks struct 不变 | grep diff：hooks.go 字段 = #2 原版 |
| I3 | HookAction enum 不变 | grep diff：HookActionContinue/Stop/BlockingStop 顺序+值 |
| I4 | FullTool 36 方法不变 | grep diff：tool_full.go interface 完整 |
| I5 | aiservice 5 入口不变 | grep diff：ai.go 函数签名 |
| I6 | prod 不部署沙箱代码 | runtime config sandbox.backend=disabled → Pool 退化 |
| I7 | config_prod.yaml 不动 | git diff config_prod.yaml 应为空 |
| I8 | bash_exec 元数据不变 | tool_bash_exec.go 除 Execute 外，其他方法字段不变（IsDestructive / IsEnabled） |

## §13 风险与缓解

| ID | 风险 | 缓解 |
|----|------|------|
| R1 | DooD 容器内 docker CLI 缺失 | Dockerfile build arg + dev 构建时装；如缺失 → backend=disabled 降级 |
| R2 | seccomp 误伤 python 启动 | dev 集成 test 必跑 `python -c "print(2+2)"`；遇误伤调 deny list（保留 commit） |
| R3 | Pool 启动时 Spawn 5 容器开销大 | 启动时 goroutine pool 异步预热（main 不阻塞）；启动后 1-2s 内有沙箱可用 |
| R4 | Pool.Return 与 bash_exec.Execute defer 双调用 | sess.returned bool + sync.Mutex once-semantic |
| R5 | sync.Map 内存泄漏（Pre 写入 + Post 未删除） | 每次 PostToolCall 调 LoadAndDelete；外加 Pool.Close() 时清空 borrow map |
| R6 | runID=0 时 PreToolCall fallthrough | 不阻断（HookActionContinue），但**不写 audit row**——降级运行，log warn |
| R7 | image_gen friendly error 用户感知 | NarrationVerb "生成图像" + 错误文案优化（"图像生成服务尚未配置，请联系管理员"） |
| R8 | dev 部署后 sandbox.backend=docker 但 docker daemon 未运行 | Pool.Borrow 失败 → tool 友好错误；启动时 health-check Spawn 一个测试容器 + log |

## §14 跨 feature 接口稳定性

| 接口 | 谁会用 | 稳定性承诺 |
|------|--------|-----------|
| `sandbox.Pool` interface | #5 / #6 / #11 / #13 | 字段 / 方法签名稳定；新增 method 不破坏 |
| `sandbox.Session` struct | #5 / #6 / #11 / #13 | ContainerID / ImageTag / Config / BorrowedAt 稳定；新增 field 不破坏 |
| `agent_sandbox_session` 表 schema | #6（写 sandbox_id）/ #11（按 user 查）/ #13（按月聚合） | 字段稳定；新增列允许（PR-friendly） |
| `NewSandboxHooks(pool, sessStore)` 工厂 | biz.go wire（#5 / #11 可能 wire） | 签名稳定 |
| `adaptFullToEinoTool(ft, hooks)` | #5 / #6 / #8（narration hook） | 签名稳定；hooks 字段未来可能扩展（变成 struct）但 nil 仍兼容 |

## §15 部署 checklist（S6 后）

1. **dev 服务器 docker pull `python:3.11-slim`**（手工，一次性）
2. **dev MySQL 跑 migration**：`20260522_120000_create_agent_sandbox_session.sql`
3. **dev Dockerfile build arg**：构建脚本传 `--build-arg WITH_DOCKER_CLI=true`
4. **dev 部署脚本**：docker run 加 `-v /var/run/docker.sock:/var/run/docker.sock`
5. **dev config_dev.yaml**：新增 `sandbox:` 段（如未自动 deploy 同步则手工 update）
6. **dev `/healthz` 验证**：服务启动日志含"sandbox pool warmed N containers"
7. **dev 手工 smoke**：用 dev curl / API 触发 bash_exec → 验证 echo / ls / python -c 跑通
8. **autopilot 协议**：止步于此；prod 等用户决定

## §16 不在范围

- prod 部署
- tenant isolation
- 权限 pipeline（#6）
- 沙箱镜像精装包（pandas/numpy/matplotlib/ffmpeg/whisper）
- 网络 Allowlist 真实落地（#14）
- CubeSandbox 升级（v2）
- 容器逃逸渗透测试
- aiservice.ImageGenerate 新增 / wanx 注册
- cmd/agent-mode-sandbox-smoke demo
- API endpoints / Controller 层

---

*最后更新：2026-05-22*
