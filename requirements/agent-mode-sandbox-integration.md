# Agent 模式 Sandbox Integration

## 来源
- 提出人：产品负责人 / 创始人
- 提出日期：2026-05-21
- 上下文：agent-mode 14-feature 分解 #4/14。**#1 Phase 0 V5 ADR 已决定选 Docker pool**（覆盖蓝本决策 #5 Daytona），原因 A1=NO（Tencent CVM 不支持嵌套 KVM）。**#2 runtime-skeleton** 完成（AgentRunner + 19-reason 状态机 + AbortController + Withhold + **RunHooks 接口预留**）。**#3 tool-registry** 完成（FullTool 36 方法 + ToolFactory + PlatformToolFactory + 6 工具，其中 `bash_exec` / `image_gen` 是 stub）。本 feature 把 Docker pool 沙箱真实落地，配合 RunHooks 注入路径，把 stub 升级为真实实现。

## 需求描述

### 问题

#3 完成后，Agent Runtime 工具链整体可用，但**两个核心工具是 stub**：
- `bash_exec.Execute()` 返回 `"bash_exec requires #4 sandbox-integration"` — IsDestructive=true / IsEnabled 受 `ToolConfig.EnableSandbox` 控制
- `image_gen.Execute()` 返回 `"image_gen requires aiservice.ImageGenerate entry"` — IsEnabled 受 `ToolConfig.EnableImageGen` 控制

这两个工具占据 v1 Agent 工具池里"代码执行 + 多媒体生成"两条核心能力。**不实现 = Agent v1 demo 残缺**：
- 学员无法跑数据分析 / 图表 / 文档转码任务（Agent 模式核心卖点之一）
- B2B 配置者无法在试聊里看到代码沙箱实际行为
- 后续 #6 permission-pipeline 无法验证 sandbox_id 注入字段；#11 student-ux 无法演示 Agent 真实产物；#13 compliance-3layer 无 sandbox 审计来源

同时 V5 ADR 列出 6 个 Open Question，**Q1-Q4 必须在 #4 落地**（Q5 / Q6 留给 #14 / v2）：
- Q1 Docker pool 并发上限 → #4 S0 估算 → 容器池大小设计
- Q2 容器逃逸应用层防御具体清单 → #4 S2 设计
- Q3 workspace 清理策略 → #4 S3 决策
- Q4 网络白名单实现方式 → #4 S2 设计

蓝本 §4.6.2 SandboxConfig + §4.6.3 预装环境 + §4.6.4 容器池管理 + §4.6.5 文件管理 全部需要落实到代码或显式延迟到 follow-up。

### 范围（Docker pool 沙箱 v1 + bash_exec / image_gen 真实实现）

| # | 模块 | 产出物 |
|---|------|--------|
| M1 | **DB 表**：`agent_sandbox_session`（沙箱会话生命周期审计） + GORM model | migration 双文件（forward + rollback）。字段含 `user_id` UNSIGNED INT NOT NULL / `agent_run_id` BIGINT UNSIGNED NULL（**FK 类型匹配 #2 agent_run.id BIGINT UNSIGNED**；#4 阶段 NULL 允许，#11/#12 填充） / `container_id` VARCHAR(128) / `image_tag` VARCHAR(128) / `status` VARCHAR(20) (running/terminated/failed) / `started_at` / `ended_at` / `mem_limit_mb` INT / `cpu_quota` DECIMAL(3,1) / `exit_code` INT NULL / `error_msg` TEXT NULL。**审计表**，PreToolCall 同步写一次，PostToolCall 同步更新一次（非异步以保证审计完整性） |
| M2 | **Store**：`IAgentSandboxSessionStore`（Create / UpdateState / GetByContainerID / List） | `internal/numind/store/agent_sandbox_session.go` + `_test.go`（in-memory SQLite） |
| M3 | **Docker pool biz**：`internal/numind/biz/sandbox/`（pool manager + lifecycle 子包） | `pool.go`（容器池：min=5 预热 / 销毁不回收 / 新建替补；环境变量 `SANDBOX_POOL_MIN=5` / `SANDBOX_POOL_MAX_WAIT_MS=30000` / `SANDBOX_BACKEND=docker\|disabled`）；`config.go`（SandboxConfig 默认值 = 蓝本 §4.6.2）；`runner.go`（ExecCommand / WriteFile / ReadFile 三个原语 op）；`docker_client.go`（封装 `docker run` / `docker exec` / `docker rm`，**用 `os/exec` 调系统 `docker` CLI**，避免引入 Docker SDK 重依赖；可换成 Docker Engine HTTP API 在 #14 升级前）。**优雅降级**：`SANDBOX_BACKEND=disabled` 时 Pool.Borrow() 返回 `ErrSandboxDisabled`，bash_exec/image_gen Execute 返回友好错误（与 #3 stub 同语义）。**部署模式（Docker-outside-of-Docker, DooD）**：numind-server 在 dev 服务器上以 docker 容器运行；要在容器内调用宿主机 docker 来管理"沙箱兄弟容器"，必须 (a) 容器内安装 `docker` CLI binary（更新 Dockerfile），(b) 启动时 bind mount `/var/run/docker.sock:/var/run/docker.sock`（更新 dev 部署脚本）。**两项变更只发生在 dev 部署链路**；prod Dockerfile / 部署脚本不动（SANDBOX_BACKEND=disabled 不需 docker CLI）。S2 spec 详细列出 Dockerfile diff + 部署脚本 diff |
| M4 | **应用层安全加固**（V5 ADR Q2 必做清单） | `internal/numind/biz/sandbox/security.go`：Docker run-time 参数组装（`--security-opt seccomp=<profile.json>` / `--security-opt apparmor=docker-default` / `--user 1000:1000` / `--cap-drop=ALL --cap-add=NET_BIND_SERVICE` / `--security-opt no-new-privileges` / `--read-only` / `--tmpfs /workdir:size=512m,uid=1000,gid=1000` / `--memory=512m --cpus=1.0 --pids-limit=64`）。`internal/numind/biz/sandbox/seccomp.json`：Docker default profile 嵌入 + 追加 deny syscall 清单（`ptrace` / `mount` / `unshare` / `keyctl` / `bpf` / `pivot_root` / `clone3` / `personality` / `userfaultfd` 等） |
| M5 | **网络白名单**（V5 ADR Q4） | `internal/numind/biz/sandbox/network.go`：v1 默认 `NetworkPolicyNone`（`--network=none` 完全断网）；Allowlist 模式声明 `AllowlistStub`（return ErrNotImplemented），实装延迟到 #14 e2e（需 iptables 配合，超出 v1 范围）|
| M6 | **`bash_exec` 真实实现** | 替换 `tool_bash_exec.go` 的 Execute 体：解析 `ToolInput`（JSON `{"command":"..."}`）→ **跑 Phase 0 V3 8 个 P0 Bash validator**（从 `cmd/agent-phase0-bash-validator` 把 validator 函数代码拷贝到 `internal/numind/biz/agent/bashvalidator/` 包并 import；#6 permission-pipeline 时升级到 23 validator） → 通过则 `pool.Borrow()` → `sandbox.ExecCommand(ctx, sess, "/bin/sh -c <cmd>")` → 返回 stdout/stderr/exit_code JSON 给 LLM。validator 失败 → 返回 deny 错误，不进沙箱 |
| M7 | **`image_gen` 真实实现** | 替换 `tool_image_gen.go` 的 Execute 体：调 `aiservice.ImageGenerate`（如缺则在 `aiservice` 包加 `ImageGenerate(ctx, taskID, req) (*ImageGenResponse, error)` 函数声明 — 最小可用版本：路由到 wanx2.1-t2i-turbo / wan2.2-t2i-flash 通过 aiservice DB Registry）。**不走沙箱**（image_gen 是纯 LLM API 调用，无代码执行）；返回 image URL JSON 给 LLM；ToolConfig.EnableImageGen=true 时 IsEnabled=true。**如 wanx2.1 实际 endpoint 未注册到 DB Registry → 暂返回 `ErrImageGenProviderNotConfigured` 友好错误**，真实调用延迟到 follow-up（与 document_generate stub 处理一致）|
| M8 | **RunHooks 工厂**：`factory_sandbox_hooks.go` | `NewSandboxHooks(pool sandbox.Pool, sessStore store.IAgentSandboxSessionStore) *RunHooks` 工厂函数。PreToolCall：调 `tool.Info(ctx)` 取 `*schema.ToolInfo`（**注意：Info 返回 (*ToolInfo, error)，需处理 error → log + 返回 HookActionContinue 不阻断**），判断 `info.Name == "bash_exec"` → `pool.Borrow()` → 写 `agent_sandbox_session` row（status=running）→ 注入 sandbox session ID 到 `ctx`（用 context.WithValue + unexported key type 防止跨包污染）。PostToolCall：从 ctx 取出 session ID → `pool.Return()`（destroy + spawn replacement）→ 更新 `agent_sandbox_session` row（status=terminated / exit_code / error_msg）。**ctx 跨 Pre→Post 传播**：用全局共享的 sync.Map 或 RunHooks 内部 map[runID]sessionID（PreToolCall 写入，PostToolCall 读取并删除）—— **S2 spec 定稿**这个跨调用 state 传递机制 |
| M9 | **adapter 层 hook 调用点注入**（方案 A） | 升级 `adaptFullToEinoTool`：包装 Eino 的 `tool.InvokableRun` 实现，每次 Eino 调底层 tool 前/后触发 `hooks.PreToolCall` / `hooks.PostToolCall`。**runner.go 签名不变**（#2 已稳定）。注入路径：`RunRequest.Hooks` 由 biz.go wire SandboxHooks 默认；适配器从 `hooks` 闭包读取 |
| M10 | **Unit + Integration tests** | sandbox 子包 unit test（pool / config / security / network / docker_client mock）；bash_exec / image_gen 升级版 test；hook 注入 integration test（mock pool + mock store）；race detector 干净；**真实 docker exec 集成 test** 用 build tag `dockerintegration` 门控（CI 不跑） |

### 不在范围（Out of Scope）

- **prod 部署**：止步 develop merge + dev container 部署
- **权限 pipeline 集成**：#6 处理（V3 Bash validator 是 #6 子任务的"23 validator 完整版"输入，#4 仅 import 8 P0 函数）
- **Tenant 隔离**：#13 处理（多租户 sandbox image 路径隔离 / quota 配额）
- **Tool 输入二次校验** & `sandbox_id` 写入 `agent_tool_call_log`：#6 处理
- **沙箱镜像精装包**（pandas/numpy/matplotlib/ffmpeg/whisper 等蓝本 §4.6.3 清单）：留 follow-up。**#4 验证基础镜像 `python:3.11-slim` 启动 + bash_exec 跑通 echo / ls / python -c 即可**；真实数据分析任务效果验证延迟到精装包 follow-up（与 #11 student-ux 联动）
- **网络 Allowlist 模式真实落地**：v1 None 即可，Allowlist 声明接口但 return NotImplemented，留 #14 与 iptables 配合
- **CubeSandbox 升级路径**：v2 工作
- **容器逃逸渗透测试**：生产化前的 follow-up
- **API 端点 / Controller**：保持 biz 层 only
- **真实 wanx2.1 / wan2.2 图像 endpoint 注册到 aiservice DB Registry**：超出 #4 范围。`image_gen` Execute **如检测到 provider 未注册** → 返回 `ErrImageGenProviderNotConfigured` 友好错误（保持 #3 stub 同语义）。真实 endpoint 注册由专门的 follow-up feature 处理（可在 #12 billing-integration 后做）

### 技术约束

- **Docker daemon 必须在 dev 服务器（49.233.219.254）已就绪**：V1 ADR 已验证 `Docker version 0.0.0-20241223130549-3b49deb`
- **prod 永不部署沙箱真实代码**：通过 `SANDBOX_BACKEND=disabled` 环境变量在 prod 配置；代码本身可以 develop merged，仅 dev 跑 backend=docker。**`config_prod.yaml` 必须明确设 `sandbox.backend=disabled`**（rules §3 禁止修改 config_prod.yaml — 但**新增配置项的默认值固化为 disabled** 等同于"无显式配置 = prod 安全"，不需改 config_prod.yaml。S2 spec 验证：Pool 工厂在读 config 时 fallback 到 disabled，**只有显式 sandbox.backend=docker 才启用真实容器**）
- **runner.go 签名不变**（#2 已稳定）：hook 注入在 `adaptFullToEinoTool` adapter 层（方案 A）
- **bash_exec / image_gen Execute 内通过 `ctx` 取 sandbox session ID**：ctx 注入由 PreToolCall hook 完成；context key 用 unexported type 防止跨包污染
- **aiservice 唯一入口**：image_gen 必须经过 aiservice.ImageGenerate（如缺则补声明，#4 范围内）— 禁止裸 HTTP 调外部图像 API
- **biz 层规则**（CLAUDE.md §3）：业务逻辑全部在 `internal/numind/biz/sandbox/` 和 `internal/numind/biz/agent/`，无 controller（本 feature 无端点）
- **GORM `default:true` bool**：参考 `database.md §6` 避坑。agent_sandbox_session 设计为 status VARCHAR + exit_code INT NULL，避免 default:true bool 问题
- **凭据**：Docker socket（dev 服务器 `/var/run/docker.sock`）通过 `os/exec` 调 `docker` CLI（无需 SDK 凭据管理）；prod 不上 sandbox 代码路径，凭据无关
- **Phase 0 V3 Bash validator 复用**：从 `cmd/agent-phase0-bash-validator` 把 8 P0 函数代码（不含 main）拷贝/提取到 `internal/numind/biz/agent/bashvalidator/` 包，避免 main 包不可 import 的问题。**#6 permission-pipeline 时升级到 23 validator 完整版**

## 业务目标

1. **Agent v1 demo 闭环**：bash_exec / image_gen 不再返回 stub error，可演示真实代码执行 + 图片生成（image_gen 真实调用仍延迟，stub 替换为友好错误而非 hard error）
2. **#5 skill-system 启动条件**：配置者勾选"启用沙箱代码"开关时，运行时有真实 sandbox pool 承接，skill 配置链路闭环
3. **#6 permission-pipeline 启动条件**：sandbox_id 字段有真实来源（#4 PreToolCall 注入），#6 可在 permission 决策时读取
4. **#11 student-ux 启动条件**：学员能跑代码沙箱任务（数据分析 / 图表 / 文档转码），无 bash_exec stub 错误打断
5. **#13 compliance-3layer 启动条件**：L1 平台合规查 sandbox 调用审计 → 依赖 agent_sandbox_session 表
6. **V5 ADR Q1-Q4 落地**：Q1 池大小（5）+ Q2 安全清单（seccomp+AppArmor+no-root+cap-drop+no-new-priv+read-only+tmpfs+memory+cpu+pids）+ Q3 销毁策略（销毁不回收 + 异步新建替补）+ Q4 网络（v1 None，Allowlist stub）

## 优先级

**高** — 阻塞 #5 / #6 / #11 / #13 全部启动；agent-mode demo 价值的核心闭环。

## Triage

- 推荐轨道：**Standard**
- 分类理由：
  1. DB schema 变更：**是**（agent_sandbox_session 新表）
  2. 新增 API 端点：**否**（biz 层）
  3. 新外部服务集成：**是**（Docker daemon / Docker CLI；首次集成）
  4. 影响文件数：**>3**（migration + 1 model + 1 store + sandbox 子包 6 文件 + bashvalidator 子包 + tool_bash_exec 升级 + tool_image_gen 升级 + adapter 升级 + factory_sandbox_hooks + biz.go wire + 测试 = 20+ 文件）
  5. 高风险业务逻辑：**是**（沙箱安全加固 = 中风险；但不动支付/会员）

   **1+3+4+5 多条触发 Standard**。同时 #4 需要严肃方案设计（Docker pool lifecycle / seccomp profile / hook 注入点 / V5 ADR Q1-Q4 落地）+ 跨 feature 接口契约稳定性高（#5/#6/#11/#13 共享 sandbox 抽象），Hotfix 三阶段无法承载。

- 人类决定：**确认 Standard**（autopilot 协议 [[feedback_agent_mode_autopilot]]）

## S5 验证策略（NDF Rule 10）

**验证方式：Go 单元测试 + race detection + mock Docker client 集成 + 真实 docker exec build-tag 集成（仅 dev/local）+ dev 服务器 SSH 手工验证（S6 后）**

- M1-M2 DB schema + store：unit test 调用 store 方法，校验 DB 行（in-memory SQLite）
- M3 Docker pool：自定义 `DockerClient` interface（Spawn / Exec / Destroy / Inspect），unit test 用 mock 实现验证 Borrow / Return / pool size 行为；race detector 覆盖并发 Borrow（10 goroutine）
- M4 安全加固：编译期断言 Docker run-time 参数清单完整；seccomp profile JSON schema 校验（用 Go 标准 json.Valid + Docker seccomp schema 比对）
- M5 网络白名单：None 模式 unit test（Pool.Borrow 返回的 session 配置含 `--network=none`）；Allowlist stub 返回 ErrNotImplemented
- M6 bash_exec：unit test（mock pool + mock validator）；**build tag `dockerintegration` 集成 test** 跑真实 `docker run python:3.11-slim` 跑 `echo hello` / `ls /workdir` / `python -c "print(2+2)"` 三类验证（仅本地 / dev，CI 跳过）
- M7 image_gen：unit test（mock aiservice.ImageGenerate）；如 aiservice 缺入口，补声明 + 函数签名 + provider 未注册时返回友好错误的测试
- M8 RunHooks 工厂：integration test（mock pool + mock store），验证 PreToolCall 写 session row + ctx 注入 + PostToolCall 更新 row
- M9 adapter hook 调用点：integration test（adaptFullToEinoTool 升级版），mock einotool.InvokableTool 验证 hooks 调用时机
- **dev 服务器手工验证**（S6 后做）：SSH dev → docker pull python:3.11-slim → 跑一个最小 demo 二进制（cmd/agent-mode-sandbox-smoke，**单独 follow-up，不在 #4 范围**）

**理由**：#4 涉及真实 Docker daemon 调用，纯 unit test 无法验证 host integration；但 CI 跑在 GitHub Actions runner（无 docker daemon 权限），故采用三层策略：(1) unit test 跑覆盖率 + (2) build-tag 集成 test 跑本地 + (3) dev 手工验证 host（S6 后 + 单独 follow-up）。

**回归保护诚实声明**：
- M1-M5 + M6 unit 部分 + M7-M9 单元测试**进 CI 主套件**（`task test`），永久回归保护
- M6 真实 docker exec test 用 `// +build dockerintegration` build tag 门控，**仅在有 docker daemon 的 host 上跑**，CI 不跑
- dev 手工验证 = 一次性 + 单独 follow-up；将来需重跑（如 Docker daemon 升级 / seccomp profile 变更）由 #14 e2e-rollout 接管

## 备注

- **架构蓝本同步**：本 feature 覆盖蓝本 §4.6（原选 Daytona），蓝本本身的更新推迟到 #14 e2e-rollout 阶段统一同步，当前以 ADR-0002（V5 Docker pool）+ 本 feature 的 spec 为准
- **跨 feature 接口稳定性**：本 feature 定义的 `sandbox.Pool` interface（Borrow / Return / ExecCommand / WriteFile / ReadFile）+ `agent_sandbox_session` 表 schema 是 #5/#6/#11/#13 共享契约，**不可轻易破坏**
- **autopilot**：dev 部署后停，等用户决定 prod；#4 完成后启动 #5
- **V5 ADR Q2 落地清单**（spec 阶段最终确认）：`seccomp` profile / `AppArmor docker-default` / `USER 1000:1000` / `--cap-drop=ALL --cap-add=NET_BIND_SERVICE` / `--security-opt no-new-privileges` / `--read-only --tmpfs /workdir:size=512m,uid=1000,gid=1000` / `--memory=512m --cpus=1.0 --pids-limit=64`
- **runner.go Run() ReAct loop 真实执行**：#2 留的 `_ = einoAgent` 在 #4 是否解决？**不解决**：#4 只接入 hooks 给 stub-level Run()，hooks 通过 adapter 层即可被触发（方案 A）；真实 ReAct loop 执行留给后续 feature（可能 #11 student-ux 实现 SSE 流式时一并接入）
