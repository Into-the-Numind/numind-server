# Agent 模式 Sandbox Integration — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** 实施 10 模块（M1-M10）：agent_sandbox_session DB + Store + sandbox 子包 (Pool/Config/DockerClient/Runner/Security/Network/seccomp.json/Errors) + bashvalidator 子包 (V3 8 P0 提取) + bash_exec 真实实现 + image_gen friendly error + RunHooks 工厂 (SandboxHookManager 模式) + adapter 升级 + biz.go wire + 测试。

**Architecture:** numind-server 单仓库；新 `internal/numind/biz/sandbox/` 子包 + `internal/numind/biz/agent/bashvalidator/` 子包 + `internal/numind/biz/agent/` 包内升级。无前端改动。

**Tech Stack:** Go 1.24 + GORM v2 + MySQL 8.0 + cloudwego/eino v0.8.13（已含）+ 标准库 `os/exec`（docker CLI）+ `//go:embed`（seccomp.json）

**Spec 引用**：[2026-05-22-agent-mode-sandbox-integration-design.md](../specs/2026-05-22-agent-mode-sandbox-integration-design.md)（S2 gate 通过，self-review 已修正 hook 注入路径 + ctx 限制 + middleware API 名）

---

## 文件清单

### 新建（28 文件）

| 路径 | 职责 |
|---|---|
| `migrations/20260522_120000_create_agent_sandbox_session.sql` | Forward migration |
| `migrations/20260522_120000_create_agent_sandbox_session_rollback.sql` | Rollback |
| `internal/pkg/model/agent_sandbox_session.go` | AgentSandboxSession GORM model |
| `internal/numind/store/agent_sandbox_session.go` | IAgentSandboxSessionStore + impl |
| `internal/numind/store/agent_sandbox_session_test.go` | store unit tests (SQLite) |
| `internal/numind/biz/sandbox/config.go` | SandboxConfig + DefaultSandboxConfig + LoadFromViper |
| `internal/numind/biz/sandbox/config_test.go` | config defaults + viper load |
| `internal/numind/biz/sandbox/errors.go` | ErrSandboxDisabled / ErrPoolExhausted / ErrSandboxOOM / ErrNotImplemented / ErrAllowlistNotImplemented |
| `internal/numind/biz/sandbox/docker_client.go` | DockerClient interface + dockerCLIClient os/exec impl |
| `internal/numind/biz/sandbox/docker_client_test.go` | mock impl unit tests |
| `internal/numind/biz/sandbox/pool.go` | Pool interface + agentSandboxPool impl + disabledPool |
| `internal/numind/biz/sandbox/pool_test.go` | race detector concurrent Borrow + lifecycle |
| `internal/numind/biz/sandbox/runner.go` | ExecCommand / WriteFile / ReadFile primitives |
| `internal/numind/biz/sandbox/runner_test.go` | mock DockerClient runner test |
| `internal/numind/biz/sandbox/security.go` | BuildSpawnConfig + ValidateSecurityChecklist + seccomp file resolution |
| `internal/numind/biz/sandbox/security_test.go` | checklist completeness + seccomp path resolution |
| `internal/numind/biz/sandbox/network.go` | NetworkPolicyForBackend (None real / Allowlist stub) |
| `internal/numind/biz/sandbox/network_test.go` | None / Allowlist tests |
| `internal/numind/biz/sandbox/seccomp.json` | embedded seccomp profile (Docker default + deny syscalls) |
| `internal/numind/biz/sandbox/integration_test.go` | // +build dockerintegration  真实 docker run / exec 跑 echo / ls / python -c |
| `internal/numind/biz/agent/bashvalidator/validator.go` | Validate(command) → 8 P0 函数集合（从 V3 提取） |
| `internal/numind/biz/agent/bashvalidator/validator_test.go` | 20 attack vectors（V3 继承） |
| `internal/numind/biz/agent/sandbox_ctx.go` | WithRunID / RunIDFromContext + SetDefaultHookManager / sandboxSessionForCurrentCall package-level helpers |
| `internal/numind/biz/agent/sandbox_ctx_test.go` | ctx helpers + race detector |
| `internal/numind/biz/agent/factory_sandbox_hooks.go` | SandboxHookManager + NewSandboxHookManager + AsRunHooks + SandboxSessionFor + preToolCall / postToolCall |
| `internal/numind/biz/agent/factory_sandbox_hooks_test.go` | mock pool + mock store integration |
| `docs/agent-mode/deploy-checklist-feature-4.md` | dev 部署 checklist（S6 后写） |

### 修改（10 文件）

| 路径 | 改动 |
|---|---|
| `internal/numind/helper.go` | AutoMigrate 列表加 `&model.AgentSandboxSession{}`（紧跟 AgentRun / ToolDefinition / ToolFactoryRegistryRow 之后） |
| `internal/numind/store/store.go` | IStore interface 加 `AgentSandboxSessions()`；datastore struct 字段 + NewDataStore wire |
| `internal/numind/biz/agent/tool_bash_exec.go` | Execute 真实实现：parse → bashvalidator.Validate → sandboxSessionForCurrentCall(ctx, "bash_exec") → sandbox.ExecCommand → 序列化结果。增加 `dc sandbox.DockerClient` 字段 |
| `internal/numind/biz/agent/tool_bash_exec_test.go` | 升级测试：validator 拦截 / 无 session 友好降级 / 成功路径（mock DockerClient） |
| `internal/numind/biz/agent/tool_image_gen.go` | Execute 返回 ErrImageGenProviderNotConfigured（取代 stub error） |
| `internal/numind/biz/agent/tool_image_gen_test.go` | 升级测试：ErrImageGenProviderNotConfigured 检查 |
| `internal/numind/biz/agent/factory_platform.go` | LoadTools 内构造 bashExecTool 时注入 dc；新增 dc 参数；调用方 biz.go 传入 |
| `internal/numind/biz/agent/factory_platform_test.go` | factory 测试用 nil dc（验证 LoadTools 不 panic） |
| `internal/numind/biz/agent/adapter_full_to_eino.go` | 签名升级：`adaptFullToEinoTool(ft FullTool, hooks *RunHooks)`；InvokableRun 增加 Pre/PostToolCall 包装（spec §6） |
| `internal/numind/biz/agent/adapter_full_to_eino_test.go` | 升级测试：nil hooks 旧行为 + hooks 非空时调用时机 |
| `internal/numind/biz/agent/runner.go` | (1) Run() 开头紧跟 r.runStore.Create 之后加 `ctx = WithRunID(ctx, run.ID)`;(2) 工具装配 loop 加 hooks 参数：`adaptFullToEinoTool(ft, req.Hooks)` → 若 req.Hooks nil 用 `r.defaultHooks`;(3) `agentRunner` struct 加 `defaultHooks *RunHooks` 字段;(4) 新增 `WithDefaultHooks(h *RunHooks) RunnerOption`（`type RunnerOption func(*agentRunner)`）;(5) `NewAgentRunner` 加可变参数 `opts ...RunnerOption` |
| `internal/numind/biz/agent/runner_test.go` | 验证 ctx WithRunID 注入 + WithDefaultHooks option |
| `internal/numind/biz/biz.go` | NewBiz 内：(1) 读 config 构造 SandboxConfig（LoadFromViper）;(2) NewDockerCLIClient;(3) NewPool;(4) NewSandboxHookManager;(5) SetDefaultHookManager 调用;(6) `agentRunner := agent.NewAgentRunner(deps.Store.AgentRuns(), deps.AgentToolRegistry, agent.WithDefaultHooks(hookManager.AsRunHooks()))` |
| `config_dev.yaml` | 新增 `sandbox:` section（spec §9） |
| `Dockerfile`（或现有的 `numind-server/Dockerfile`） | ARG WITH_DOCKER_CLI=false + RUN 安装 docker-cli when true |
| `scripts/cicd/release.sh`（dev 路径） | (1) build 时传 `--build-arg WITH_DOCKER_CLI=true`（仅 dev）;(2) docker run 时加 `-v /var/run/docker.sock:/var/run/docker.sock`（仅 dev） |

> **零变更**：controller / router / API 端点 / config_prod.yaml / 前端 / 其他业务包。

---

## TOC（按 Phase 拆分）

### Phase 1：基础设施 + 解耦核心抽象（Tier 3 并行，4 路）

- **Task 1**: M1 DB schema + GORM model + AutoMigrate （`migrations/` + `internal/pkg/model/agent_sandbox_session.go` + `helper.go`）
- **Task 2**: M3 sandbox 子包基础（无 docker 依赖部分：config / errors / network / security / seccomp.json embedding）
- **Task 3**: M3 sandbox docker_client interface + os/exec impl + mock impl 的 _test 文件（独立文件，便于复用）
- **Task 4**: bashvalidator 子包提取（从 cmd/agent-phase0-bash-validator/ 拷贝代码 + 测试）

### Phase 2：Pool + Store + 工厂（依赖 Phase 1）

- **Task 5**: M2 store agent_sandbox_session.go + _test + IStore wire + datastore（依赖 Task 1 model）
- **Task 6**: M3 sandbox.Pool（pool.go + pool_test.go + runner.go + runner_test.go）（依赖 Task 2+3）

### Phase 3：Agent 集成（依赖 Phase 1+2 部分）

- **Task 7**: agent 包 ctx helpers + SandboxHookManager（sandbox_ctx.go + sandbox_ctx_test.go + factory_sandbox_hooks.go + factory_sandbox_hooks_test.go）（依赖 Task 5 store + Task 6 pool）

### Phase 4：工具升级 + adapter 升级 + 集成

- **Task 8**: tool_bash_exec 真实实现 + tool_image_gen friendly error + factory_platform 加 dc 注入（依赖 Task 4 bashvalidator + Task 7 hookmanager）
- **Task 9**: adapter_full_to_eino 升级 + runner.go ctx 注入 + WithDefaultHooks option（依赖 Task 7）
- **Task 10**: biz.go wire + Dockerfile + release.sh + config_dev.yaml（依赖所有前置）

### Phase 5：集成测试

- **Task 11**: 跨包 integration 测试 + race detector 全套 + // +build dockerintegration test（需要本地有 docker）

### Phase 6（可选，单独 S5 / S6 后）

- **Task 12**: deploy-checklist-feature-4.md（S6 后写）

---

## 并行 Tier 评估

### Phase 1 Tier 3 disjoint（4 路并行）

| Agent | 文件归属（每组逗号分隔） |
|-------|---------|
| Agent A (Task 1) | `migrations/20260522_120000_create_agent_sandbox_session.sql,migrations/20260522_120000_create_agent_sandbox_session_rollback.sql,internal/pkg/model/agent_sandbox_session.go,internal/numind/helper.go` |
| Agent B (Task 2) | `internal/numind/biz/sandbox/config.go,internal/numind/biz/sandbox/config_test.go,internal/numind/biz/sandbox/errors.go,internal/numind/biz/sandbox/network.go,internal/numind/biz/sandbox/network_test.go,internal/numind/biz/sandbox/security.go,internal/numind/biz/sandbox/security_test.go,internal/numind/biz/sandbox/seccomp.json` |
| Agent C (Task 3) | `internal/numind/biz/sandbox/docker_client.go,internal/numind/biz/sandbox/docker_client_test.go` |
| Agent D (Task 4) | `internal/numind/biz/agent/bashvalidator/validator.go,internal/numind/biz/agent/bashvalidator/validator_test.go` |

**ndf-check-disjoint 命令（Phase 1）**：
```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "migrations/20260522_120000_create_agent_sandbox_session.sql,migrations/20260522_120000_create_agent_sandbox_session_rollback.sql,internal/pkg/model/agent_sandbox_session.go,internal/numind/helper.go" \
  "internal/numind/biz/sandbox/config.go,internal/numind/biz/sandbox/config_test.go,internal/numind/biz/sandbox/errors.go,internal/numind/biz/sandbox/network.go,internal/numind/biz/sandbox/network_test.go,internal/numind/biz/sandbox/security.go,internal/numind/biz/sandbox/security_test.go,internal/numind/biz/sandbox/seccomp.json" \
  "internal/numind/biz/sandbox/docker_client.go,internal/numind/biz/sandbox/docker_client_test.go" \
  "internal/numind/biz/agent/bashvalidator/validator.go,internal/numind/biz/agent/bashvalidator/validator_test.go"
```

预期 exit 0（无文件重叠）。如失败 → 立刻降级串行。

### Phase 2 串行（2 task 互不阻塞，但强 dependency on Phase 1）

- Task 5 依赖 Task 1（model）→ 直接 Phase 1 完后启动
- Task 6 依赖 Task 2（config / security / network） + Task 3（docker_client）→ 直接 Phase 1 完后启动

Task 5 / Task 6 文件不重叠（store 包 vs sandbox 子包）→ 可 Tier 3 并行（同 Phase 2 内）：

| Agent | 文件归属 |
|-------|---------|
| Agent E (Task 5) | `internal/numind/store/agent_sandbox_session.go,internal/numind/store/agent_sandbox_session_test.go,internal/numind/store/store.go` |
| Agent F (Task 6) | `internal/numind/biz/sandbox/pool.go,internal/numind/biz/sandbox/pool_test.go,internal/numind/biz/sandbox/runner.go,internal/numind/biz/sandbox/runner_test.go` |

**ndf-check-disjoint 命令（Phase 2）**：
```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "internal/numind/store/agent_sandbox_session.go,internal/numind/store/agent_sandbox_session_test.go,internal/numind/store/store.go" \
  "internal/numind/biz/sandbox/pool.go,internal/numind/biz/sandbox/pool_test.go,internal/numind/biz/sandbox/runner.go,internal/numind/biz/sandbox/runner_test.go"
```

### Phase 3 单 Task（Task 7）

依赖 Phase 2 完成（pool + store）；单 task 不需并行。

### Phase 4 Tier 3（Task 8 + Task 9 disjoint），然后 Task 10 串行

| Agent | 文件归属 |
|-------|---------|
| Agent G (Task 8) | `internal/numind/biz/agent/tool_bash_exec.go,internal/numind/biz/agent/tool_bash_exec_test.go,internal/numind/biz/agent/tool_image_gen.go,internal/numind/biz/agent/tool_image_gen_test.go` |
| Agent H (Task 9) | `internal/numind/biz/agent/adapter_full_to_eino.go,internal/numind/biz/agent/adapter_full_to_eino_test.go,internal/numind/biz/agent/runner.go,internal/numind/biz/agent/runner_test.go` |

**ndf-check-disjoint 命令（Phase 4）**：
```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "internal/numind/biz/agent/tool_bash_exec.go,internal/numind/biz/agent/tool_bash_exec_test.go,internal/numind/biz/agent/tool_image_gen.go,internal/numind/biz/agent/tool_image_gen_test.go" \
  "internal/numind/biz/agent/adapter_full_to_eino.go,internal/numind/biz/agent/adapter_full_to_eino_test.go,internal/numind/biz/agent/runner.go,internal/numind/biz/agent/runner_test.go"
```

Task 10 单独串行（biz.go wire + Dockerfile + release.sh + config_dev.yaml 各自独立，但需在 Task 8/9 之后才能正确 wire）。

### Phase 5 单 Task

Task 11 集成测试需要所有前置 commit 完成。

---

## Task 详细说明

### Task 1: M1 DB schema + GORM model + AutoMigrate

**文件归属**：见上 Agent A

**Spec 引用**：§2.1

**实施步骤**：

1. 创建 `migrations/20260522_120000_create_agent_sandbox_session.sql`（DDL 见 spec §2.1）
2. 创建 `migrations/20260522_120000_create_agent_sandbox_session_rollback.sql`（DROP TABLE IF EXISTS）
3. 创建 `internal/pkg/model/agent_sandbox_session.go`（GORM model 见 spec §2.1）
4. 修改 `internal/numind/helper.go`：在现有 `db.AutoMigrate(&model.ToolFactoryRegistryRow{})` 之后加 `db.AutoMigrate(&model.AgentSandboxSession{})`

**验收**：
- migration 文件命名 + 内容 = spec §2.1
- AgentSandboxSession 含 11 字段 + TableName + GORM tags
- AutoMigrate 列表含 AgentSandboxSession
- 同包 go vet / go build 干净

**测试**：本 task 不写单测（store 单测在 Task 5）

**Commit message**:
```
feat(agent-sandbox): M1 agent_sandbox_session migration + GORM model + AutoMigrate
```

---

### Task 2: sandbox 子包基础（config / errors / network / security / seccomp）

**文件归属**：见上 Agent B

**Spec 引用**：§3.3, §3.6, §3.7, §3.8

**实施步骤**：

1. 创建 `internal/numind/biz/sandbox/config.go`：SandboxConfig struct + DefaultSandboxConfig + LoadFromViper（spec §3.3）
2. 创建 `internal/numind/biz/sandbox/errors.go`：ErrSandboxDisabled / ErrPoolExhausted / ErrSandboxOOM / ErrSessionReturned / ErrNotImplemented / ErrAllowlistNotImplemented / ErrImageGenProviderNotConfigured（spec §3.2 + §7）
3. 创建 `internal/numind/biz/sandbox/network.go`：NetworkPolicy enum + NetworkPolicyForBackend（spec §3.8）
4. 创建 `internal/numind/biz/sandbox/security.go`：BuildSpawnConfig + ValidateSecurityChecklist + ResolveSeccompPath（spec §3.6 + embed seccomp.json + tmp file path resolution）
5. 创建 `internal/numind/biz/sandbox/seccomp.json`：JSON 内容（spec §3.7）
6. 单测：config_test (defaults + viper override) / network_test (None / Allowlist stub) / security_test (BuildSpawnConfig + ValidateSecurityChecklist 0 missing on default)

**关键约束**：
- BackendDisabled 是 DefaultSandboxConfig 默认值（prod safety）
- seccomp.json embed 用 `//go:embed seccomp.json` + 启动时拷到 `os.TempDir()` 下供 docker --security-opt seccomp= 引用（docker 需文件路径）
- ResolveSeccompPath 返回绝对路径 + 错误（写文件失败）；返回路径在 sandbox 包初始化时 sync.Once 调用

**验收**：
- 4 个 .go 文件 + seccomp.json + 3 个 _test.go 文件
- ValidateSecurityChecklist 在 BuildSpawnConfig(DefaultSandboxConfig, "...")  时返回空 missing 列表（断言 V5 ADR Q2 默认达标）
- go build + go vet + go test 全干净

**测试**：
- TestDefaultSandboxConfig_PassesValidateSecurityChecklist
- TestLoadFromViper_OverridesDefaults
- TestNetworkPolicyForBackend_NoneReturnsString
- TestNetworkPolicyForBackend_AllowlistReturnsErrNotImplemented
- TestBuildSpawnConfig_FromDefaults
- TestValidateSecurityChecklist_MissingSeccompFlagged

**Commit message**:
```
feat(sandbox): M3 config + errors + network + security + seccomp.json base
```

---

### Task 3: sandbox docker_client interface + os/exec impl

**文件归属**：见上 Agent C

**Spec 引用**：§3.4

**实施步骤**：

1. 创建 `internal/numind/biz/sandbox/docker_client.go`：
   - DockerClient interface（Spawn / Exec / Destroy / Inspect）
   - SpawnConfig / ExecOpts / ExecResult / InspectResult struct
   - dockerCLIClient impl（用 `os/exec.Command("docker", ...)`）
   - NewDockerCLIClient 工厂
2. 创建 `internal/numind/biz/sandbox/docker_client_test.go`：
   - mockDockerClient impl（in-memory map of containerID → state）
   - 编译期断言 `var _ DockerClient = (*mockDockerClient)(nil)`
   - 调用方在其他 task 复用 mockDockerClient（spec §3.4 行为）

**关键约束**：
- Spawn 命令拼接见 spec §3.4 伪代码（注意 --security-opt 引号 / 路径斜杠）
- Exec 用 `docker exec --workdir /workdir --user 1000:1000 <containerID> /bin/sh -c "<command>"`；ctx Deadline 传给 os/exec 的 CommandContext
- Destroy 用 `docker rm -f <containerID>`；幂等（容器已删时 swallow stderr "No such container"）
- Inspect 用 `docker inspect --format='{{.State.Status}} {{.State.ExitCode}} {{.State.OOMKilled}}' <containerID>` → parse output

**验收**：
- DockerClient interface 4 方法
- dockerCLIClient impl 编译期断言
- mockDockerClient 可用于其他 task 测试
- 不真实调 docker（仅命令字符串构造测试）

**测试**：
- TestDockerCLIClient_SpawnCommandString（用 fake exec 验证命令字符串）—— 用 `os/exec.Command` 替代法（或测试 cmd.Args 字段）
- TestMockDockerClient_LifecycleHappyPath

**Commit message**:
```
feat(sandbox): M3 docker_client interface + os/exec impl + mock for tests
```

---

### Task 4: bashvalidator 子包提取（V3 8 P0）

**文件归属**：见上 Agent D

**Spec 引用**：§4

**实施步骤**：

1. 读 `numind-server/cmd/agent-phase0-bash-validator/` 全部 .go 文件，找到 8 个 P0 validator 函数（不是 main）
2. 创建 `internal/numind/biz/agent/bashvalidator/validator.go`：拷贝 V3 函数 + 提供顶级 `Validate(command string) (allow bool, reason string)` 入口
3. 创建 `internal/numind/biz/agent/bashvalidator/validator_test.go`：拷贝 V3 attack vectors 测试（20+ cases）
4. **保留 cmd/agent-phase0-bash-validator** 原 main.go（Phase 0 acceptance reference 不删）

**关键约束**：
- 函数命名与 V3 保持一致（reviewer 可对比 V3 acceptance）
- attack vectors 100% 命中（V3 已验证 20/20）
- 不引入新依赖

**验收**：
- bashvalidator.Validate 调用所有 8 个 validator，任一拒绝 → 返回 (false, "reason: <validator name>")
- 20 attack vector 测试 100% PASS
- 单测覆盖率 ≥ V3 的 98.8%

**测试**：
- TestValidate_AllowsSafeCommands（echo / ls / cat /tmp/test 等）
- TestValidate_RejectsDangerousPath（rm -rf /, cat /etc/passwd, ...）
- TestValidate_RejectsCurlPipeShell
- TestValidate_RejectsSSH
- TestValidate_RejectsNetcat
- TestValidate_RejectsCommandSubstitution
- TestValidate_RejectsBashOperators
- TestValidate_RejectsFileWrite
- TestValidate_RejectsFork

**Commit message**:
```
feat(agent-sandbox): M6 bashvalidator subpackage — extract Phase 0 V3 8 P0 validators
```

---

### Task 5: Store agent_sandbox_session + IStore wire

**文件归属**：见上 Agent E

**Spec 引用**：§2.2

**实施步骤**：

1. 创建 `internal/numind/store/agent_sandbox_session.go`：
   - IAgentSandboxSessionStore interface (Create / UpdateState / GetByContainerID / ListByUser)
   - agentSandboxSessionStore impl + NewAgentSandboxSessionStore
2. 创建 `internal/numind/store/agent_sandbox_session_test.go`：in-memory SQLite tests
3. 修改 `internal/numind/store/store.go`：IStore interface 加 `AgentSandboxSessions() IAgentSandboxSessionStore`；datastore 字段 + NewDataStore wire

**关键约束**：
- 错误处理：返回 fmt.Errorf("Create: %w", err) / etc.
- UpdateState 用 `db.Model(&AgentSandboxSession{}).Where("id = ?", id).Updates(map[string]interface{}{...})`（避免 GORM default:bool 坑 — agent_sandbox_session 没有 default:true bool，但养成习惯）

**验收**：
- 4 个 store 方法 + 单测
- IStore 注册 + datastore wire
- in-memory SQLite test：Create → UpdateState → GetByContainerID 全链路

**测试**：
- TestAgentSandboxSessionStore_Create
- TestAgentSandboxSessionStore_UpdateState_Terminated
- TestAgentSandboxSessionStore_UpdateState_Failed
- TestAgentSandboxSessionStore_GetByContainerID
- TestAgentSandboxSessionStore_ListByUser

**依赖**：Task 1（model）

**Commit message**:
```
feat(agent-sandbox): M2 IAgentSandboxSessionStore + impl + IStore wire
```

---

### Task 6: sandbox.Pool + Runner primitives

**文件归属**：见上 Agent F

**Spec 引用**：§3.2, §3.5

**实施步骤**：

1. 创建 `internal/numind/biz/sandbox/pool.go`：
   - Session struct + Pool interface + agentSandboxPool impl
   - **Pool interface 含 `DockerClient() DockerClient` 方法**（供 SandboxHookManager 暴露给 bash_exec.Execute 用 — spec §3.4 调整）
   - disabledPool impl（all methods return ErrSandboxDisabled；DockerClient() return nil）
   - NewPool 工厂（read SandboxConfig.Backend → return real or disabled）
   - 启动时预热（goroutine + Spawn × cfg.PoolMin）
   - Borrow 从 channel 取；超时 → ErrPoolExhausted
   - Return 调 Destroy + 异步 Spawn 补一个；sess.returned bool once-semantic
2. 创建 `internal/numind/biz/sandbox/pool_test.go`：
   - race detector 10 goroutine 并发 Borrow / Return
   - 池耗尽超时
   - disabled backend 路径
3. 创建 `internal/numind/biz/sandbox/runner.go`：ExecCommand / WriteFile / ReadFile 原语（spec §3.5）
4. 创建 `internal/numind/biz/sandbox/runner_test.go`：mock DockerClient 调用 ExecCommand

**关键约束**：
- Pool 内部 chan size = cfg.PoolMin × 2（容纳异步补 spawn）
- 启动预热在 NewPool 内 goroutine + wg.Wait 控制（启动时间 1-2s 内完成）
- ctx Deadline 传给 Borrow 等待（用 select 包含 ctx.Done()）

**验收**：
- Pool 接口 + 2 个 impl（real / disabled）
- ExecCommand / WriteFile / ReadFile 3 个原语
- race detector 干净
- pool_test ≥ 80% line coverage

**依赖**：Task 2（config / errors / security / network）+ Task 3（DockerClient + mockDockerClient）

**测试**：
- TestNewPool_DisabledBackendReturnsDisabledPool
- TestPool_BorrowReturnConcurrent（race）
- TestPool_BorrowExhaustedTimeout
- TestPool_ReturnSpawnsReplacement
- TestPool_ReturnTwiceNoOp
- TestExecCommand_DelegatesToDockerClient
- TestWriteFile_ReturnsErrNotImplemented
- TestReadFile_ReturnsErrNotImplemented

**Commit message**:
```
feat(sandbox): M3 Pool + ExecCommand/WriteFile/ReadFile primitives
```

---

### Task 7: agent ctx helpers + SandboxHookManager

**文件归属**：见上

**Spec 引用**：§5

**实施步骤**：

1. 创建 `internal/numind/biz/agent/sandbox_ctx.go`：
   - `type runIDCtxKey struct{}` + WithRunID / RunIDFromContext
   - `var defaultHookManager *SandboxHookManager` + sync.RWMutex
   - SetDefaultHookManager / sandboxSessionForCurrentCall package-level helpers
   - **dockerClientForCurrentCall(ctx) sandbox.DockerClient**：从 defaultHookManager.DockerClient() 取（供 tool_bash_exec.Execute 用）
2. 创建 `internal/numind/biz/agent/sandbox_ctx_test.go`：
   - ctx put/get + SetDefaultHookManager race detector + sandboxSessionForCurrentCall lookup + dockerClientForCurrentCall lookup
3. 创建 `internal/numind/biz/agent/factory_sandbox_hooks.go`：
   - sandboxBorrow struct
   - SandboxHookManager struct + NewSandboxHookManager + AsRunHooks + SandboxSessionFor + key
   - **DockerClient() sandbox.DockerClient**：return m.pool.DockerClient()
   - preToolCall / postToolCall methods
4. 创建 `internal/numind/biz/agent/factory_sandbox_hooks_test.go`：
   - mock pool + mock IAgentSandboxSessionStore
   - PreToolCall happy path: Borrow + Create row + sync.Map insert
   - PreToolCall pool.Borrow err → fallthrough Continue（no row, no map）
   - PostToolCall happy path: LoadAndDelete + Return + UpdateState
   - PostToolCall no entry → Continue
   - Non-bash_exec → Continue both Pre/Post

**关键约束**：
- ctx WithRunID 类型断言：`v, ok := ctx.Value(runIDCtxKey{}).(uint64); if ok return v else 0`
- SandboxHookManager 是 process 单例（biz.go 一次构造）；sync.Map 跨 run 安全
- key 格式 `fmt.Sprintf("%d|%s", runID, toolName)`
- preToolCall 调 tool.Info(ctx) 失败 → log + Continue 不阻断
- postToolCall LoadAndDelete 而非 Load（消费 + 删除原子）
- 错误处理：log.Warnw 而非 panic / return error；不阻断业务

**验收**：
- 2 个新文件 + 2 个测试文件
- 编译期断言 `var _ store.IAgentSandboxSessionStore = (*mockSandboxStore)(nil)` 在测试包内
- race detector 干净（hook manager 在并发场景）

**依赖**：Task 5（store interface 在 Task 5 已就位）+ Task 6（sandbox.Pool）

**测试**：
- TestWithRunID_Roundtrip
- TestRunIDFromContext_AbsentReturnsZero
- TestSetDefaultHookManager_RaceDetector
- TestSandboxSessionForCurrentCall_NoManagerReturnsNil
- TestSandboxSessionForCurrentCall_NoRunIDReturnsNil
- TestSandboxHookManager_PreToolCall_HappyPath
- TestSandboxHookManager_PreToolCall_PoolBorrowFails
- TestSandboxHookManager_PreToolCall_NonBashExec
- TestSandboxHookManager_PostToolCall_HappyPath
- TestSandboxHookManager_PostToolCall_NoEntry

**Commit message**:
```
feat(agent-sandbox): M5+M8 sandbox_ctx + SandboxHookManager (Pre/PostToolCall hooks)
```

---

### Task 8: tool_bash_exec / tool_image_gen 升级（dc 通过 SandboxHookManager 提供）

**文件归属**：见上 Agent G —— **简化：不修改 factory_platform.go**

**Spec 引用**：§4.3, §7

**设计调整（vs spec §3.4 / §4.3）**：避免 tool_bash_exec.go 携带 `dc` 字段（需要传 sandbox.DockerClient 到 factory_platform，会引入跨 commit 编译性问题），改为：
- `SandboxHookManager` 暴露 `DockerClient() sandbox.DockerClient`（manager 本身已持 Pool；Pool 持 dc）
- Pool interface 加 `DockerClient() sandbox.DockerClient` 方法（Task 6 范围 — 已 plan 内调整）
- bash_exec.Execute 用 `defaultHookManager.DockerClient()` 取 dc（与 sandboxSessionForCurrentCall 同包路径）
- factory_platform.go **不改签名**（仍是 `(rag, ds)` 两参数），bashExecTool struct **不加 dc 字段**
- biz.go wire 也不改 PlatformToolFactory 调用

**实施步骤**：

1. 修改 `internal/numind/biz/sandbox/pool.go`（**Task 6 范围内调整**，不在 Task 8 中改）：Pool interface 加 `DockerClient() DockerClient` 方法。agentSandboxPool / disabledPool 都 impl。**Task 6 plan 之后追加此 method**
2. 修改 `internal/numind/biz/agent/factory_sandbox_hooks.go`（**Task 7 范围内调整**）：SandboxHookManager 加 `DockerClient() sandbox.DockerClient { return m.pool.DockerClient() }`。**Task 7 plan 之后追加此 method**
3. 修改 `internal/numind/biz/agent/sandbox_ctx.go`（**Task 7 范围内调整**）：暴露 package-level `dockerClientForCurrentCall(ctx) sandbox.DockerClient { return defaultHookManager.DockerClient() if not nil else nil }`
4. 修改 `internal/numind/biz/agent/tool_bash_exec.go`（Task 8）：
   - Execute 真实实现：parse → bashvalidator.Validate → sandboxSessionForCurrentCall → 取 dc → sandbox.ExecCommand → 序列化
   - 不加 dc 字段
   - errorResult helper（封装 friendly error JSON）
5. 修改 `internal/numind/biz/agent/tool_bash_exec_test.go`：
   - 升级 stub 测试为新行为
   - 验证 validator 拦截
   - 验证无 session 时友好降级
   - 验证 mock DockerClient 成功路径（test 内 SetDefaultHookManager 注入 mock manager）
6. 修改 `internal/numind/biz/agent/tool_image_gen.go`：
   - Execute 返回 ErrImageGenProviderNotConfigured（替换 stub error）
7. 修改 `internal/numind/biz/agent/tool_image_gen_test.go`：
   - 升级 stub 测试为 ErrImageGenProviderNotConfigured 断言

**factory_platform 完全不动**（向后兼容 #3）。这避免 Task 8 ↔ Task 10 跨 commit 编译断链。

**关键约束**：
- bash_exec.Execute 错误返回：sandbox 执行失败 → 返回 `errorResult(...)` + execErr（让 PostToolCall hook 标记 audit 'failed'）；validator 失败 → 返回 errorResult + nil（不算 sandbox 错，是用户输入错）
- errorResult 序列化 JSON：`{"error": "...", "stderr": "..."}` 之类 — LLM 可读
- bashvalidator import `numind-server/internal/numind/biz/agent/bashvalidator`（Task 4 提供）
- sandboxSessionForCurrentCall import 自同包（agent 包内）
- sandbox.ExecCommand import `numind-server/internal/numind/biz/sandbox`
- factory_platform 老签名 `NewPlatformToolFactory(rag, ds)` → 新签名 `NewPlatformToolFactory(rag, ds, dc)`；biz.go wire 跟着改

**验收**：
- 3 个升级文件 + 3 个测试文件
- 所有 #3 已有 test 仍 PASS（验证向后兼容）

**依赖**：Task 4（bashvalidator）+ Task 6（sandbox.ExecCommand）+ Task 7（sandboxSessionForCurrentCall）

**测试**：
- TestBashExecTool_Execute_ValidatorRejects
- TestBashExecTool_Execute_NoSessionFriendlyError
- TestBashExecTool_Execute_HappyPath（mock DockerClient + 预注入 sync.Map session）
- TestImageGenTool_Execute_ReturnsProviderNotConfigured
- TestPlatformToolFactory_LoadToolsWithNilDc_NoPanic

**Commit message**:
```
feat(agent-sandbox): M6+M7 tool_bash_exec real impl + tool_image_gen friendly error + factory_platform dc injection
```

---

### Task 9: adapter_full_to_eino 升级 + runner.go 改造

**文件归属**：见上 Agent H

**Spec 引用**：§6

**实施步骤**：

1. 修改 `internal/numind/biz/agent/adapter_full_to_eino.go`：
   - adaptFullToEinoTool 签名加 `hooks *RunHooks`（nil 兼容）
   - fullToolEinoAdapter struct 加 `hooks *RunHooks`
   - InvokableRun 改造（spec §6 伪代码）
2. 修改 `internal/numind/biz/agent/adapter_full_to_eino_test.go`：
   - 升级原测试：现有 test 传 nil hooks（向后兼容）
   - 新增 test：mock hooks，验证 PreToolCall / PostToolCall 调用时机 + HookActionStop 短路 + PostToolCall 错误优先级
3. 修改 `internal/numind/biz/agent/runner.go`：
   - agentRunner struct 加 `defaultHooks *RunHooks`
   - `type RunnerOption func(*agentRunner)`
   - `func WithDefaultHooks(h *RunHooks) RunnerOption { return func(r *agentRunner) { r.defaultHooks = h } }`
   - NewAgentRunner 签名加 `opts ...RunnerOption`
   - Run() 内 r.runStore.Create 之后加 `ctx = WithRunID(ctx, run.ID)`
   - 工具装配 loop 改 `adaptFullToEinoTool(ft, effectiveHooks)`（effectiveHooks = req.Hooks || r.defaultHooks）
4. 修改 `internal/numind/biz/agent/runner_test.go`：
   - 新增 TestNewAgentRunner_WithDefaultHooks
   - 新增 TestRun_InjectsRunIDIntoCtx（通过自定义 mock tool 验证 ctx.Value）
   - 现有 test 仍 PASS（NewAgentRunner 旧 2 参数调用 + variadic options 空 → 等价）

**关键约束**：
- adaptFullToEinoTool 旧调用（nil hooks）保持 100% 向后兼容；#3 test 不破坏
- WithDefaultHooks 是 functional option，可重复调用（last write wins）
- ctx WithRunID 仅注入 1 次（在 Create 之后）；之后所有传播都用 derived ctx

**验收**：
- 4 个升级文件
- #3 所有 test 仍 PASS
- 新 test 覆盖 hooks 调用时机

**依赖**：Task 7（RunHooks struct 已在 #2，本任务不改 hooks.go；但 effectiveHooks 选择逻辑参考 Task 7 manager）

**测试**：
- TestAdaptFullToEinoTool_NilHooks_BackCompat
- TestAdaptFullToEinoTool_HooksCalledOnSuccess
- TestAdaptFullToEinoTool_HookStopShortCircuits
- TestAdaptFullToEinoTool_PostToolCallOnExecError
- TestNewAgentRunner_NoOptions
- TestNewAgentRunner_WithDefaultHooks
- TestRun_InjectsRunIDIntoCtx
- TestRun_EffectiveHooksReqOverridesDefault

**Commit message**:
```
feat(agent-sandbox): M9 adapter_full_to_eino hooks param + runner WithDefaultHooks + ctx WithRunID injection
```

---

### Task 10: biz.go wire + Dockerfile + release.sh + config_dev.yaml

**文件归属**（Agent I 串行）：
- `internal/numind/biz/biz.go`
- `config_dev.yaml`
- `Dockerfile`
- `scripts/cicd/release.sh`

**Spec 引用**：§6.3, §9, §10

**实施步骤**：

1. 修改 `internal/numind/biz/biz.go` NewBiz：
   ```go
   sandboxConfig := sandbox.LoadFromViper(viper.GetViper())
   dockerClient := sandbox.NewDockerCLIClient(deps.Logger)
   sandboxPool := sandbox.NewPool(sandboxConfig, dockerClient, deps.Logger)
   sandboxStore := deps.Store.AgentSandboxSessions()
   sandboxHookManager := agent.NewSandboxHookManager(sandboxPool, sandboxStore)
   agent.SetDefaultHookManager(sandboxHookManager)
   sandboxHooks := sandboxHookManager.AsRunHooks()
   // existing platform tool factory now needs dc injected
   ptf := agent.NewPlatformToolFactory(deps.SalesRag, deps.Store, dockerClient)
   // existing AgentToolRegistry construction unchanged
   // existing AgentRunner construction adds WithDefaultHooks
   agentRunner := agent.NewAgentRunner(
       deps.Store.AgentRuns(),
       agentToolRegistry,
       agent.WithDefaultHooks(sandboxHooks),
   )
   ```
2. 修改 `config_dev.yaml`：新增 `sandbox:` section（spec §9）
3. 修改 `Dockerfile`：添加 `ARG WITH_DOCKER_CLI=false` 和 `RUN if [ "$WITH_DOCKER_CLI" = "true" ]; then apk add --no-cache docker-cli; fi`（spec §10.1）
4. 修改 `scripts/cicd/release.sh` dev path：build 时加 `--build-arg WITH_DOCKER_CLI=true`；docker run 加 `-v /var/run/docker.sock:/var/run/docker.sock`

**关键约束**：
- biz.go 改动是新代码 + 修改 PlatformToolFactory 调用方
- Dockerfile / release.sh 仅 dev 分支；prod 路径 100% 不变
- config_prod.yaml 不动

**验收**：
- biz.go 编译 + go vet + go test 干净
- Dockerfile build：本地 `docker build --build-arg WITH_DOCKER_CLI=true .` 成功 → 含 docker CLI；不传 arg → 不含
- release.sh dev 路径正确
- config_dev.yaml YAML 有效

**依赖**：所有前置 task

**测试**：
- biz.go: TestNewBiz_WiresSandboxComponents（确保 NewBiz 不 panic 且 hook manager 已 SetDefault）

**Commit message**:
```
feat(agent-sandbox): M10 biz.go wire SandboxHookManager + Dockerfile WITH_DOCKER_CLI + dev release.sh socket mount + config_dev.yaml
```

---

### Task 11: integration tests + race detector + dockerintegration build tag

**文件归属**（Agent J 单独）：
- `internal/numind/biz/sandbox/integration_test.go`

**Spec 引用**：§11

**实施步骤**：

1. 创建 `internal/numind/biz/sandbox/integration_test.go` 顶部：
   ```go
   //go:build dockerintegration
   ```
2. 测试用例（spec §11）：
   - TestDockerSpawnDestroy_RealDocker
   - TestExecCommandEchoHello_RealDocker
   - TestExecCommandListWorkdir_RealDocker（验证 tmpfs /workdir 存在）
   - TestExecCommandPythonPrint_RealDocker（python:3.11-slim 启动 + python -c）
   - TestPoolWarmup5_RealDocker（启动 + 等预热 + Size() == 5）
3. 不在 CI 跑（CI 默认不传 `-tags dockerintegration`）

**关键约束**：
- 所有真实 docker 调用包在 build tag 之后
- 不引入 docker SDK 依赖（仍用 dockerCLIClient）
- 跳过条件：`if testing.Short() { t.Skip(...) }`（双重保险）

**验收**：
- 5 个集成测试用例
- 不影响 CI（CI build 默认不含 dockerintegration tag）

**依赖**：所有前置 task

**测试**：
- 本机有 docker → `go test -tags dockerintegration ./internal/numind/biz/sandbox/...` PASS
- 无 docker → 默认 `go test ./...` 跳过这个文件

**Commit message**:
```
test(agent-sandbox): M11 dockerintegration build-tag tests (echo/ls/python-c + pool warmup)
```

---

### Task 12（可选 / S5 后）: deploy-checklist-feature-4.md

**文件归属**：`docs/agent-mode/deploy-checklist-feature-4.md`

**Spec 引用**：§15

**实施时机**：S5 验收完成后，与 #2 / #3 deploy-checklist-feature-{2,3}.md 同结构写。

---

## S5 验证策略

**沿用 #2/#3 模式**：Go 单元测试 + race + 集成测试，**不出 UI/HTTP**。

### S5 完成判定（spec §11 验证一致）

- [ ] `go test -race ./internal/numind/biz/sandbox/... ./internal/numind/biz/agent/... ./internal/numind/store/...` ALL PASS
- [ ] `go build ./...` 整包 build clean
- [ ] `go vet ./...` exit 0
- [ ] sandbox 包 line coverage ≥ 80%
- [ ] bashvalidator 包 line coverage 接近 V3（98.8%）
- [ ] agent 包 (新增 ctx/hooks) line coverage ≥ 80%
- [ ] store/agent_sandbox_session line coverage ≥ 80%
- [ ] `-tags dockerintegration` 本地 5 个 test PASS（手工跑 + 截图存到 S5 acceptance record）

### 测试覆盖关键路径

| 路径 | Task | 验证方式 |
|------|------|----------|
| migration → AutoMigrate → store CRUD | 1, 5 | SQLite in-memory test |
| SandboxConfig defaults pass ValidateSecurityChecklist | 2 | TestDefaultSandboxConfig_PassesValidateSecurityChecklist |
| DockerClient interface satisfied by mock + real | 3 | 编译期断言 + mock test |
| bashvalidator 8 P0 拦截 20 attack vectors | 4 | V3 attack vector matrix |
| Pool concurrent Borrow + Return | 6 | race detector 10-goroutine test |
| SandboxHookManager Pre/Post flow | 7 | mock pool + mock store integration |
| bash_exec.Execute parse → validate → exec | 8 | mock DockerClient happy path |
| image_gen.Execute friendly error | 8 | direct call test |
| adapter hooks not nil → calls Pre/Post | 9 | mock RunHooks |
| runner ctx WithRunID inject | 9 | mock tool reads ctx.Value |
| biz.go wires SandboxHookManager + SetDefault | 10 | TestNewBiz integration |
| REAL docker exec echo/ls/python -c | 11 | dockerintegration tag |

### 回归保护诚实声明

- M1-M10 单元测试 + race detector test 进 CI 主套件（`task test`），永久回归保护
- M11 真实 docker exec test 用 `//go:build dockerintegration` build tag 门控，**仅在本地 / dev / 未来集成机有 docker daemon 的 host 上跑**；CI 不跑
- dev 服务器手工验证 = S6 后一次性，不进 CI；deploy-checklist-feature-4.md 记录验证命令

### 部署后的手工 sanity check（S6 后做）

参考 spec §15 部署 checklist：
1. SSH dev → `docker pull python:3.11-slim`
2. dev MySQL 跑 migration（手工，per `project_dev_deploy_migration_gap.md`）
3. dev `/healthz` 含 "sandbox pool warmed 5 containers"
4. dev curl 触发 Agent run → bash_exec("echo hello") → 返回 stdout "hello"

---

## ndf-done 前置门槛

- [ ] manifest `progress.completed_tasks >= 11`（M1-M11；M12 可选）
- [ ] manifest `progress.reviewed_tasks >= 11`
- [ ] manifest `stage == S5` 转 S6 前 stage == S5
- [ ] 全部新建文件 (28) + 修改文件 (10) commit
- [ ] `go test -race ./...` PASS（业务包；CI 默认）
- [ ] `go vet ./...` 干净
- [ ] sandbox 包覆盖率 ≥ 80%
- [ ] 无 P0/P1 残留
- [ ] `task lint` PASS（go vet + golangci-lint）
- [ ] **未部署 qa/prod 任一环境**
- [ ] `ndf-done` 原子化 merge → develop（S6 步骤）

---

## 备注

- **架构蓝本同步**：本 feature 覆盖蓝本 §4.6 Sandbox（已 ADR-0002 锁定，#14 更新蓝本）
- **跨 feature 接口契约**：`sandbox.Pool` / `agent_sandbox_session` schema / `NewSandboxHookManager` / `adaptFullToEinoTool` 升级签名 = #5/#6/#11/#13 共享，**不可破坏**
- **autopilot 协议**：dev 部署后停 prod，等用户决定何时上 prod；继续启动 #5
- **风险监控**：reviewer 验证 spec §14 接口稳定承诺；如 Phase 4-5 中发现 sandbox.Pool 接口必须改 → 必须记 ADR 解释（避免 #5/#6 启动后 review 时迷失）

---

*最后更新：2026-05-22*
