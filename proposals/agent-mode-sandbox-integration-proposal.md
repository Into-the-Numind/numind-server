# Agent 模式 Sandbox Integration — 提案

## §1 方案概述

agent-mode 14-feature #4/14。落地 Phase 0 V5 ADR 决策的 Docker pool 沙箱，替换 #3 bash_exec / image_gen stub 为真实实现。

阻塞：#5 skill-system / #6 permission-pipeline / #11 student-ux / #13 compliance-3layer。

核心交付：
- `internal/numind/biz/sandbox/` 子包（Pool 管理 + Docker CLI 封装 + 安全加固 + 网络策略）
- `agent_sandbox_session` 审计表 + Store
- `internal/numind/biz/agent/bashvalidator/` 子包（从 Phase 0 V3 cmd 提取 8 P0 函数）
- `tool_bash_exec.go` Execute 真实实现（validator → pool.Borrow → Exec → Return）
- `tool_image_gen.go` Execute 仍是 friendly error（**provider 未注册时**），保留接口；真实 wanx 调用延迟到 follow-up
- `internal/numind/biz/agent/factory_sandbox_hooks.go` — RunHooks 工厂
- `adapter.go` adaptFullToEinoTool 升级 — Eino tool 调用包装 PreToolCall / PostToolCall（**方案 A**，runner.go 签名不变）

## §2 周期

- 预估 8-10 工作日（W4-W5）
- 内部 R&D
- dev 部署后停 prod（autopilot 协议）

## §3 技术可行性

### 复用

| 模块 | 来源 | 用途 |
|------|------|------|
| #2 `RunHooks struct` | `internal/numind/biz/agent/hooks.go` | #4 实例化 + 注入；签名稳定，无破坏 |
| #2 `HookAction enum` | `internal/numind/biz/agent/hooks.go` | sandbox 失败时返回 HookActionStop（映射 hook_stopped terminal） |
| #2 `AgentRunner.Run` signature | `internal/numind/biz/agent/runner.go` | 签名不变；hooks 注入路径在 RunRequest.Hooks → adapter 闭包 |
| #3 `FullTool` + `BaseTool` | `internal/numind/biz/agent/tool_full.go` + `base_tool.go` | bash_exec/image_gen 嵌入 BaseTool；Execute 重写 |
| #3 `adaptFullToEinoTool` | `internal/numind/biz/agent/registry.go`（grep 找到实际位置） | **升级**：旧实现包装 FullTool → einotool.BaseTool，**新实现**在 InvokableRun 内部触发 hooks.PreToolCall / hooks.PostToolCall |
| #3 `ToolConfig.EnableSandbox` / `EnableImageGen` | `internal/numind/biz/agent/tool_full.go` | bash_exec.IsEnabled / image_gen.IsEnabled 已与 ToolConfig 对接（#3 已就位） |
| Phase 0 V3 8 P0 Bash validators | `numind-server/cmd/agent-phase0-bash-validator/`（main 包不可 import） | **代码提取**到 `internal/numind/biz/agent/bashvalidator/` 子包；引用方仅 #4（#6 升级到 23 validator 时再扩展） |
| V5 ADR Q2 安全清单 | `.ndf/decisions/agent-mode-phase0-verification/0002-sandbox-final.md` | seccomp + AppArmor + no-root + cap-drop + no-new-priv + read-only + tmpfs + resource limits 全部落地 |
| Docker daemon | dev 服务器 49.233.219.254 现有 Docker `0.0.0-20241223130549-3b49deb` | Phase 0 V1 已实测；通过 `os/exec` 调 docker CLI（DooD 模式） |
| GORM `datatypes.JSON` / `default:true` | 项目通用 | agent_sandbox_session 字段类型选择 |

### 不复用 / 新建

| 模块 | 理由 |
|------|------|
| Docker SDK Go client | 引入重依赖；用 `os/exec` 调 docker CLI 等价且无 import 膨胀 |
| Daytona Go SDK | V5 ADR 已否决 Daytona |
| aiservice.ImageGenerate 函数 | **scope 外**；wanx2.1/wan2.2 endpoint 注册到 aiservice DB Registry 是独立 follow-up（与 #12 billing 联动）；#4 仅在 image_gen.Execute 内 lookup provider 不存在 → 返回 friendly error |

### 风险

| ID | 风险 | 缓解 |
|----|------|------|
| R1 | numind-server 在 dev 容器内调宿主机 docker → 需 DooD（Docker-outside-of-Docker）+ `/var/run/docker.sock` bind mount + 容器内装 docker CLI | (a) S4 编码同时改 `Dockerfile`（dev 阶段 ADD docker CLI）+ deploy 脚本（dev mount socket）；(b) prod Dockerfile / 部署脚本不动；(c) **如 dev 部署后发现 docker CLI 不在容器内**，临时方案是 `SANDBOX_BACKEND=disabled` 降级，单独修 Dockerfile follow-up |
| R2 | seccomp profile JSON 太严格误伤合法 syscall（如 Python 启动失败） | 用 Docker 默认 profile + 加 deny 黑名单（保守起点）；S5 测试覆盖 `python -c "print(2+2)"`；dev 手工验证发现误伤 → 调 profile（独立小 commit） |
| R3 | 容器逃逸（应用层安全加固不足以挡死） | V5 ADR 已声明 v1 风险可接受；**生产化前必须做渗透测试 follow-up**；本 feature 不上 prod，dev 上有限制 |
| R4 | Pool 池耗尽 / 等待超时 / 并发激增 | `SANDBOX_POOL_MIN=5` + `SANDBOX_POOL_MAX_WAIT_MS=30000`；超时 → bash_exec.Execute 返回 friendly error；监控指标在 #14 接入 Prometheus |
| R5 | bashvalidator 提取 V3 代码 → main 包 → 子包 重命名导致测试漏迁移 | S3 plan 把"提取 + 子包测试同步"作为独立 task；S4 编码 reviewer 验证测试覆盖率不下降 |
| R6 | adapter 升级（adaptFullToEinoTool）影响 #3 现有 PlatformToolFactory tests | adapter 接受 `hooks *RunHooks` 可选参数；nil → 退化为旧行为；#3 现有 test 传 nil 不破坏 |
| R7 | ctx 跨 PreToolCall / PostToolCall 传递 sandbox session ID | 用 RunHooks 内部 `sync.Map[runID]sessionID`（**S2 spec 定稿**）；按 tool 调用粒度 + runID 隔离 |
| R8 | image_gen 仍是 stub 状态 | 范围控制 — 不在 #4 落地 wanx2.1/wan2.2 真实调用；保留接口 + 友好错误；follow-up 单独写 feature |

### AI 可观测性

- bash_exec / image_gen 调用进 Langfuse trace（继承 #3 设置）
- sandbox Pool.Borrow / Return 在 Langfuse 写 Span（pool_borrow / sandbox_exec / pool_return 三段）
- agent_sandbox_session DB 行 = 审计真相源（重启 Langfuse 也能查）
- 监控指标（**#14 接入** Prometheus，#4 仅准备好 Langfuse Span）：`sandbox.pool_size` / `sandbox.wait_time_ms` / `sandbox.exec_duration_ms`

## §4 PRD

### 用户故事

- 作为 **#5 skill-system 实施者**，我需要 ToolConfig.EnableSandbox=true 时 bash_exec 真实可用（不再 stub error）
- 作为 **#6 permission-pipeline 实施者**，我需要 sandbox_id（=agent_sandbox_session.id）能在 PreToolCall 之后从 ctx / DB 取
- 作为 **#11 student-ux 实施者**，我需要 bash_exec 真实跑 `python -c "..."` 返回 stdout 给 LLM
- 作为 **#13 compliance-3layer 实施者**，我需要 agent_sandbox_session 表所有沙箱调用审计

### 验收标准

**M1 DB**：
- [ ] agent_sandbox_session 表 migration 跑通（forward + rollback）
- [ ] AutoMigrate 注册到 `internal/numind/helper.go`
- [ ] FK type 匹配（agent_run_id BIGINT UNSIGNED NULL）
- [ ] 索引：`idx_ass_user_started` (user_id, started_at) + `idx_ass_status` (status) + `idx_ass_run` (agent_run_id)

**M2 Store**：
- [ ] `IAgentSandboxSessionStore`：Create / UpdateState / GetByContainerID / List
- [ ] in-memory SQLite test 覆盖 race + concurrent UpdateState

**M3 Sandbox biz pool / config / runner / docker_client**：
- [ ] `Pool` interface：Borrow(ctx) (*Session, error) / Return(sess *Session, exitCode int, errMsg string) / Close()
- [ ] `Session` struct：ContainerID / ImageTag / WorkdirHostPath / CreatedAt / mu sync.Mutex
- [ ] Config 默认值与蓝本 §4.6.2 + V5 ADR Q2 对齐（MemoryLimitMB=512 / CPUQuota=1.0 / Timeout=30s / SessionTimeout=300s / NetworkPolicy=None / WorkdirSizeMB=512 / ReadOnlyRootfs=true / Capabilities=[NET_BIND_SERVICE]）
- [ ] DockerClient interface（Spawn / Exec / Destroy / Inspect）+ os/exec 实现 + mock 实现（仅 test 包）
- [ ] Pool 预热 5 个 / 销毁不回收 / 异步补一个
- [ ] `SANDBOX_BACKEND=disabled` 时 Borrow 返回 ErrSandboxDisabled

**M4 安全加固**：
- [ ] seccomp.json 文件存在（Docker default + deny syscalls：ptrace / mount / unshare / keyctl / bpf / pivot_root / clone3 / personality / userfaultfd）
- [ ] Docker run-time 参数清单（编译期断言）：seccomp / apparmor / user 1000:1000 / cap-drop ALL / cap-add NET_BIND_SERVICE / no-new-privileges / read-only / tmpfs /workdir:size=512m,uid=1000,gid=1000 / memory=512m / cpus=1.0 / pids-limit=64

**M5 网络白名单**：
- [ ] NetworkPolicyNone → `--network=none`
- [ ] NetworkPolicyAllowlist → 返回 ErrNotImplemented（v1 stub，#14 真实落地）

**M6 bash_exec 真实实现**：
- [ ] 解析 ToolInput JSON `{"command":"..."}`
- [ ] 跑 bashvalidator.Validate(command)；失败返回 deny error（含 reason）
- [ ] Pool.Borrow → sandbox.ExecCommand(ctx, sess, []string{"/bin/sh","-c",command}) → 返回 ToolResult JSON `{"stdout":"...","stderr":"...","exit_code":N}`
- [ ] PostToolCall hook 自动 Pool.Return
- [ ] **build tag `dockerintegration`** 集成 test 跑真实 docker（echo / ls / python -c）

**M7 image_gen friendly error**：
- [ ] Execute 检查"是否已注册 image provider"（v1：硬编码 false） → 返回 ErrImageGenProviderNotConfigured（friendly + 不阻塞 Run）
- [ ] 单测：不论 ToolConfig.EnableImageGen 值如何，Execute 都返回 friendly error（除非 provider registered = 永远 false in #4）
- [ ] **不**新增 aiservice.ImageGenerate 函数 / ImageProvider interface（确保 #4 scope 不膨胀）— 留 follow-up feature

**M8 RunHooks 工厂**：
- [ ] `NewSandboxHooks(pool sandbox.Pool, sessStore store.IAgentSandboxSessionStore) *agent.RunHooks` 工厂函数（**无 runID 参数**：hooks 是进程级单例，runID 通过 ctx 传递 — `agent.WithRunID(ctx, runID)` / `agent.RunIDFromContext(ctx)`，新增 agent 包内 helper）
- [ ] runner.go 在 Run() 内 `ctx = WithRunID(ctx, run.ID)` 完成 runID ctx 注入（**新增 #4 范围内 1 行修改 runner.go，不算签名变更**）
- [ ] hooks 内部维护 `sync.Map[string]*sandboxBorrow`（key = `runID + "/" + toolName`，PreToolCall 写入、PostToolCall 读取并删除；多次 bash_exec 调用同一 runID 时 key 加 invocation seq？**S2 spec 定稿**）
- [ ] PreToolCall：
  - 从 ctx 取 runID（未找到 → 视为非 bash_exec，返回 Continue）
  - 调 `tool.Info(ctx)`（处理 error → log + 返回 Continue 不阻断）
  - 若 info.Name == "bash_exec" → pool.Borrow → 写 agent_sandbox_session row(running, agent_run_id=runID) → 存 borrow 到 hooks 内部 sync.Map
  - 返回 (HookActionContinue, nil)
- [ ] PostToolCall：
  - 从 ctx 取 runID + tool.Info 取 name → 查 sync.Map（未找到 = 不是 bash_exec call，直接 Continue）
  - pool.Return(sess, exitCode, errMsg)
  - 更新 agent_sandbox_session row(terminated/failed + exit_code + error_msg)
  - 删除 sync.Map 入口
  - 返回 (HookActionContinue, nil)

**M9 adapter 层 hook 调用点**（方案 A）：
- [ ] adaptFullToEinoTool 升级签名：从 `adaptFullToEinoTool(ft FullTool) einotool.InvokableTool` → `adaptFullToEinoTool(ft FullTool, hooks *RunHooks) einotool.InvokableTool`
  - **向后兼容路径**：hooks 传 nil → 等价旧行为；#3 现有 tests 通过 nil
  - Eino tool 接口要求 InvokableRun 返回 `(string, error)`；hook 错误 → 转换为 Eino error
- [ ] InvokableRun 改造：
  ```go
  func (a *fullToolEinoAdapter) InvokableRun(ctx context.Context, args string, _ ...einotool.Option) (string, error) {
      input := ToolInput(args)
      // 触发 PreToolCall（hooks 内部决定是否真正处理该工具）
      if a.hooks != nil && a.hooks.PreToolCall != nil {
          action, err := a.hooks.PreToolCall(ctx, a, args)
          if err != nil {
              return "", fmt.Errorf("PreToolCall: %w", err)
          }
          if action == HookActionStop || action == HookActionBlockingStop {
              return "", fmt.Errorf("tool execution stopped by hook: %v", action)
          }
      }
      // 执行真实 FullTool
      result, execErr := a.ft.Execute(ctx, input)
      var output string
      if result != nil {
          output = string(result)
      }
      // 触发 PostToolCall（即使 Execute 失败也调，让 hook 做 cleanup 如 pool.Return）
      if a.hooks != nil && a.hooks.PostToolCall != nil {
          _, postErr := a.hooks.PostToolCall(ctx, a, output, execErr)
          if postErr != nil {
              // PostToolCall 错误优先级低于 execErr；仅在没原 execErr 时返回
              if execErr == nil {
                  return output, fmt.Errorf("PostToolCall: %w", postErr)
              }
              // log + 继续返回 execErr
          }
      }
      return output, execErr
  }
  ```
- [ ] runner.go 装配阶段升级：`adaptFullToEinoTool(ft, req.Hooks)`（runner.go **改动 1 行**：装配工具循环加 hooks 参数 — 这不算签名变更）
- [ ] **`AgentRunner.Run` 函数签名 + `RunRequest` struct 完全不变**（仅 runner.go Run() 内部代码做 1 行 ctx 注入 + 1 处工具装配参数补充）

**M10 测试**：
- [ ] sandbox 子包 unit test（pool / config / security / network / docker_client mock）≥ 80% line coverage
- [ ] bash_exec / image_gen 升级测试（vs #3 stub 测试，替换 + 新增 ToolResult 解析）
- [ ] hook 注入 integration test（mock pool + mock store，验证 PreToolCall 写 session row + 跨 Pre/Post 跟踪 + PostToolCall 更新 row）
- [ ] **build tag `dockerintegration`** 真实 docker 集成 test（仅本地 / dev，CI skip）
- [ ] race detector：sandbox 包 + agent 包整体 `go test -race` 干净
- [ ] dev 手工验证 sanity check（S6 后独立 follow-up）

### 边界

- **out of scope**：
  - prod 部署
  - tenant 隔离（多租户镜像 / quota）
  - 权限 pipeline 集成（#6）
  - 网络 Allowlist 真实落地（#14）
  - CubeSandbox v2 升级
  - 容器逃逸渗透测试
  - 沙箱镜像精装包（pandas/numpy/matplotlib/ffmpeg/whisper）— follow-up
  - wanx2.1 / wan2.2 endpoint 真实注册到 aiservice DB Registry — follow-up
  - cmd/agent-mode-sandbox-smoke demo 二进制 — follow-up（dev 手工验证可用 ad-hoc shell 命令）

### 关键决策（提案级）

1. **DooD 而非 DinD**：bind mount 宿主 socket 比 Docker-in-Docker 简单且 dev-only 风险可控。生产化时改方案。
2. **`os/exec` docker CLI 而非 Docker Go SDK**：无新依赖；CLI 字符串易审计。可在 #14 升级前迁移 SDK。
3. **`SANDBOX_BACKEND=disabled` 默认值**：Pool 工厂读 config 未显式 docker → fallback disabled。**等同于 prod 安全**，不需改 config_prod.yaml（rules §3 不许动 config_prod.yaml）。
4. **方案 A（adapter 层注入 hook）**：runner.go 签名不变，hook 注入点最深（每个 tool call 都触发），符合 #2 RunHooks 设计意图。方案 B（runner.go 内插入）需等 ReAct loop 真实实装，超出 #4 范围。
5. **image_gen 仍是 friendly error**：不在 #4 落地 aiservice.ImageGenerate；范围控制 + 等 wanx provider 注册 follow-up。bash_exec 是 #4 的真实主体。
6. **V3 8 P0 validator 提取到子包**：cmd/ main 包不可 import → 必须代码提取到 internal/numind/biz/agent/bashvalidator/。#6 升级到 23 validator 时再扩展。
7. **bashvalidator 在 bash_exec.Execute 路径调用**：validator 失败 → bash_exec 返回 deny error，不进沙箱（节省 Pool 资源 + 拦截显式恶意命令）。**这是 #4 自己的最小拦截**，#6 后续在 hook 层加更强权限校验（IsDestructive 二次确认等）。

## §5 备注

- **架构蓝本同步**：覆盖蓝本 §4.6 Daytona → Docker pool（已 ADR-0002 锁定）；蓝本本身 #14 阶段统一更新
- **跨 feature 接口契约**：`sandbox.Pool` interface + `agent_sandbox_session` schema 是 #5/#6/#11/#13 共享；不可破坏
- **autopilot 协议**：S6 ndf-done 后停 prod，等用户决定何时上 prod；继续启动 #5

## §6 部署影响

- **Dockerfile 变更**（dev 部署链路）：base image 装 `docker-ce-cli`（仅 dev，prod Dockerfile 不变；用 build arg / 多 stage 区分）
- **dev 部署脚本变更**：docker run 时加 `-v /var/run/docker.sock:/var/run/docker.sock`
- **config_dev.yaml 新增**：
  ```yaml
  sandbox:
    backend: docker
    pool_min: 5
    pool_max_wait_ms: 30000
    memory_limit_mb: 512
    cpu_quota: 1.0
    timeout_seconds: 30
    session_timeout_seconds: 300
    network_policy: none
    workdir_size_mb: 512
  ```
- **config_prod.yaml 不动**（autopilot 协议；不写 sandbox 段 = 默认 backend=disabled = prod 安全）
- **新增 docker 镜像**：`python:3.11-slim`（dev 服务器手工 `docker pull` 一次；S6 后部署 checklist 列）
- **migration**：新增 agent_sandbox_session 表 → S6 后手工 SSH dev MySQL 跑（memory `project_dev_deploy_migration_gap.md`）
