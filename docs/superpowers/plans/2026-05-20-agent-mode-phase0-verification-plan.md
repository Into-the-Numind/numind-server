# Agent 模式 Phase 0 前置验证 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 验证 4 个关键技术假设（A1 Daytona KVM / A2 Eino+aiservice / A3 8 P0 Bash validator / A4 Coze-Eino 心智模型）；产出 5 个验证物 V1-V5；闭环决策 Phase 1 是否启动 / 沙箱方案最终选型。

**Architecture:** numind-server 单仓库。两类产物：
- 主 module 内：V2 demo（`cmd/agent-phase0-eino-demo/`，Eino 依赖进主 go.mod）
- 主 module 外：V3 demo（`cmd/agent-phase0-bash-validator/`，独立 go.mod，零依赖）
- 文档：V1/V5 ADR + V4 笔记（不产代码）

**Tech Stack:** Go 1.24 + (主 module) cloudwego/eino + (V1 dev 服务器) Daytona OSS Docker Compose

**Spec 引用**: [2026-05-20-agent-mode-phase0-verification-design.md](../specs/2026-05-20-agent-mode-phase0-verification-design.md)（S2 gate 通过，含 §3 V2 / §4 V3 / §10 不变量；3 P0 + 1 P1 + 5 P2 已吸收）

---

## 文件清单

### 新建

| 路径 | 职责 |
|---|---|
| `cmd/agent-phase0-eino-demo/main.go` | V2 入口 |
| `cmd/agent-phase0-eino-demo/adapter.go` | Eino ChatModel → aiservice.Chat adapter |
| `cmd/agent-phase0-eino-demo/tools.go` | 单一 demo 工具 `get_current_date` |
| `cmd/agent-phase0-eino-demo/observability.go` | Langfuse span 注入 helper |
| `cmd/agent-phase0-eino-demo/adapter_test.go` | adapter 单测 |
| `cmd/agent-phase0-eino-demo/tools_test.go` | 工具单测 |
| `cmd/agent-phase0-eino-demo/README.md` | 运行步骤 + 验收记录 |
| `cmd/agent-phase0-bash-validator/go.mod` | 独立 module（V3 零依赖纯字符串处理）|
| `cmd/agent-phase0-bash-validator/validator.go` | 公共 Validator interface + Result |
| `cmd/agent-phase0-bash-validator/control_char.go` | V3.1 ControlChar |
| `cmd/agent-phase0-bash-validator/unicode.go` | V3.2 Unicode |
| `cmd/agent-phase0-bash-validator/carriage_return.go` | V3.3 CR |
| `cmd/agent-phase0-bash-validator/command_substitution.go` | V3.4 CommandSub |
| `cmd/agent-phase0-bash-validator/ifs.go` | V3.5 IFS |
| `cmd/agent-phase0-bash-validator/proc_environ.go` | V3.6 ProcEnviron |
| `cmd/agent-phase0-bash-validator/backslash_operator.go` | V3.7 BackslashOp |
| `cmd/agent-phase0-bash-validator/brace_expansion.go` | V3.8 BraceExpansion |
| `cmd/agent-phase0-bash-validator/*_test.go` | 8 个 validator 单测 + 矩阵测试 |
| `cmd/agent-phase0-bash-validator/ATTACK_VECTORS.md` | 20 个攻击向量 + 拦截矩阵 |
| `docs/agent-mode/eino-coze-study-notes.md` | V4 深读笔记 ≥ 3000 字 |
| `.ndf/decisions/agent-mode-phase0-verification/0001-kvm-daytona.md` | V1 ADR |
| `.ndf/decisions/agent-mode-phase0-verification/0002-sandbox-final.md` | V5 沙箱 ADR |
| `.ndf/decisions/agent-mode-phase0-verification/screenshots/` | V1 截图归档目录 |

### 修改

| 路径 | 改动内容 |
|---|---|
| `go.mod` | 新增 `require github.com/cloudwego/eino vX.Y.Z`（V2 实施第 1 步 pin）|
| `go.sum` | `go mod tidy` 自动生成 |

> **零变更主 server 业务代码 / 配置 / migration**：本 feature 不动 `internal/numind/biz/` / `internal/numind/store/` / `internal/numind/controller/` / `internal/pkg/model/` / `config_*.yaml` / `migrations/`。仅在主 module 加 Eino 依赖 + cmd/ 下两个 demo 目录。

---

## TOC（5 个原子 task — V1-V5）

### Task 1：V3 Bash Validator + 测试（独立 go.mod，零依赖，可最先做）
### Task 2：V2 Eino + aiservice demo（主 module，依赖 V3 模式作为"测试驱动"参考）
### Task 3：V4 Coze Studio / Eino 源码深读笔记（独立 Agent dispatch，与 Task 1+2 并行）
### Task 4：V1 KVM + Daytona OSS dev 服务器实测（SSH 操作，独立 Agent dispatch）
### Task 5：V5 沙箱方案 ADR（**串行依赖 V1 结果**，最后做）

**依赖图**：

```
Task 1 (V3) ─────────┐
Task 2 (V2) ─────────┤
Task 3 (V4) ─────────┤ → S5 验收 → S6 ndf-done
Task 4 (V1) ─→ Task 5 (V5) ─┘
```

并行性（Tier 评估）：
- Task 1 + Task 2 同仓库但**完全不同目录**（`cmd/agent-phase0-bash-validator/` vs `cmd/agent-phase0-eino-demo/`），属 **Tier 3**：disjoint write，主 session 必须输出文件归属表 + 跑 `ndf-check-disjoint.sh` 验证
- Task 3 改 `docs/agent-mode/`，与 Task 1/2 完全 disjoint → 可加入并行集合
- Task 4 是 SSH 远端操作，本地仅产 ADR md 文件，与 Task 1/2/3 disjoint → 可加入并行集合
- Task 5 依赖 Task 4 输出 → 串行

主 session dispatch 策略：
- **Round 1（并行）**：Task 1 + Task 2 + Task 3 + Task 4（4 路 Agent，Tier 3 disjoint）
- **Round 2（串行）**：Task 5（依赖 Round 1 的 Task 4 输出）

**Tier 3 文件归属表（Round 1 dispatch 前必须运行 `ndf-check-disjoint.sh` 验证）**：

```bash
# 在 worktree 根目录运行
bash /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "cmd/agent-phase0-bash-validator/go.mod cmd/agent-phase0-bash-validator/go.sum cmd/agent-phase0-bash-validator/*.go cmd/agent-phase0-bash-validator/*_test.go cmd/agent-phase0-bash-validator/ATTACK_VECTORS.md" \
  "go.mod go.sum cmd/agent-phase0-eino-demo/*.go cmd/agent-phase0-eino-demo/*_test.go cmd/agent-phase0-eino-demo/README.md" \
  "docs/agent-mode/eino-coze-study-notes.md" \
  ".ndf/decisions/agent-mode-phase0-verification/0001-kvm-daytona.md .ndf/decisions/agent-mode-phase0-verification/screenshots/*"
```

预期：exit 0（完全 disjoint）。`go.sum` 路径独立——Task 1 写 `cmd/agent-phase0-bash-validator/go.sum`（独立 module），Task 2 写主 `go.sum`，**两者物理上是不同文件**，不冲突。

每个 Task 完成后**主 session 必须**：
1. 验证 git commit 存在（NDF Rule 8：subagent commit 验证）
2. Dispatch **并行**两个 Sonnet reviewer（spec-compliance + code-quality / 文档专项 reviewer），单 turn 两个 Agent
3. P0/P1 修完才能算 `reviewed_tasks +1`

---

## Task 1：V3 Bash Validator + 测试

### 目录初始化

```bash
mkdir -p cmd/agent-phase0-bash-validator
cd cmd/agent-phase0-bash-validator
go mod init phase0-bash-validator   # 独立 module；短名称有意为之（V3 是 one-shot prototype，不对外 import，名称仅用于 go.mod 自身解析）
```

### 实现清单

- [ ] T1.1：`validator.go` — 定义 Validator interface + Result struct（spec §4.2）
- [ ] T1.2：实现 8 个 validator 文件，每个含 `New<Name>Validator() Validator`
  - [ ] ControlChar (0x00-0x1F 除 \t \n \r + 0x7F)
  - [ ] Unicode (RTL U+202E / NBSP U+00A0 / zero-width U+200B,200C,200D,FEFF)
  - [ ] CR (regex `\r(?!\n)`)
  - [ ] CommandSubstitution (regex `\$\(|\`|<\(|>\(`)
  - [ ] IFS (regex `\$\{?IFS\}?|\$'.*?'`)
  - [ ] ProcEnviron (regex `/proc/[^/]+/(environ|cmdline|maps|status|fd)`)
  - [ ] BackslashOperator (regex `\\\\[xu0-9]` 在 echo/printf 上下文)
  - [ ] BraceExpansion (双 regex：`\{[^{}]*,[^{}]*\}` OR `\{[^{}]*\.\.[^{}]*\}`，含嵌套深度检测)
- [ ] T1.3：`ATTACK_VECTORS.md` — 20 个攻击向量矩阵（5 Allow + 15 Deny；spec §4.4）
- [ ] T1.4：每个 validator 单测 + 一个全矩阵测试 `validator_test.go`
- [ ] T1.5：`go test ./...` 100% pass；测试覆盖率 ≥ 90%
- [ ] T1.6：`go vet ./...` + `gofmt -l .` 干净
- [ ] T1.7：commit `feat(phase0-v3): 8 P0 Bash validators + 20 attack vectors`

### 验收

```bash
cd cmd/agent-phase0-bash-validator
go test -cover ./...            # 所有 PASS + cover ≥ 90%
go test -run TestAttackMatrix    # 20 个攻击向量矩阵
```

### Reviewer dispatch

- spec-compliance reviewer：核对 spec §4.3 8 validator 的 regex 与实现一致 + ATTACK_VECTORS.md 20 个 case 完整
- code-quality reviewer：核对 Go 代码风格 + 测试设计 + 无 dead code

---

## Task 2：V2 Eino + aiservice demo

### 实现清单

- [ ] T2.1：**实施前置**：`go get github.com/cloudwego/eino@latest` + `go mod tidy`（pin Eino 版本到主 go.mod）
- [ ] T2.2：**核对 API**：读 `vendor/github.com/cloudwego/eino/components/model/interface.go` 和 `flow/agent/react/react.go`，确认：
  - ChatModel interface 真实签名（Generate / Stream 参数）
  - react.Config struct 真实字段名（spec §3.5 预期 Model/Tools/MaxSteps）
  - 不一致则改 adapter 实现，spec 不更新（spec 是 ground truth 预期，actual API 是运行时事实）
- [ ] T2.3：建目录 `cmd/agent-phase0-eino-demo/`，初始化 `main.go` 框架
- [ ] T2.4：`adapter.go` 实现 AiserviceAdapter（spec §3.4）
  - `Generate` 调 `aiservice.Chat(ctx, "phase0-eino-demo", req)` 3 参数
  - `Stream` 调 `aiservice.ChatStream(ctx, "phase0-eino-demo", req)` + channel→StreamReader 适配
  - convertToAiserviceRequest / convertToEinoMessage / wrapChannelAsStreamReader helper
- [ ] T2.5：`tools.go` 实现单一 demo 工具 `get_current_date`（返回 ISO 8601 字符串）
- [ ] T2.6：`observability.go` 实现 `instrumentedToolCall` 包装函数（spec §3.6）
  - `langfuse.CreateSpan(traceID, spanID, name, opts...)` — name 是第 3 位置参数
  - `langfuse.EndSpan(traceID, spanID)` — 第一参数是 traceID
- [ ] T2.7：`main.go` 主流程（spec §3.5）
  - 命令行参数：`--error-path`（错误路径测试模式）
  - 创建 Langfuse trace（含 WithUserID(0) + WithTraceInput + WithTraceTags）
  - 构造 ReAct agent + 跑一次
  - happy path 输出 final answer；error path 模式输出 stderr + exit 1（不 panic）
- [ ] T2.8：单元测试
  - `adapter_test.go`：mock aiservice，测试 message 转换正确
  - `tools_test.go`：测试 get_current_date 返回格式
  - 不写 ReAct loop 整体集成测试（依赖外部 LLM API）—— 整体走 S5 手工验收
- [ ] T2.9：`README.md` 记录（直接给完整 SQL，不让 implementer 再去找 spec）：
  - 如何跑：`go run ./cmd/agent-phase0-eino-demo/`
  - 验收 SQL ① Reserve：`SELECT * FROM credit_reservation WHERE created_at > '<demo 启动时间>' ORDER BY id DESC LIMIT 1;`（至少 1 行）
  - 验收 SQL ② Reconcile：`SELECT * FROM credit_transaction WHERE source_type IS NOT NULL AND created_at > '<demo 启动时间>' ORDER BY id DESC LIMIT 1;`（至少 1 行，source_type ∈ {trial, subscription, cycle, booster}）
  - 错误路径：`go run ./cmd/agent-phase0-eino-demo/ --error-path` 期望 exit code != 0 + Langfuse 后台 error generation 可见
- [ ] T2.10：commit `feat(phase0-v2): Eino ChatModel adapter + ReAct demo via aiservice`

### 验收（在本地或构建机执行，**不进 dev container 部署**）

```bash
go run ./cmd/agent-phase0-eino-demo/                  # happy path
go run ./cmd/agent-phase0-eino-demo/ --error-path     # error path

# Langfuse 后台检查：有 trace + ≥1 generation + ≥1 span
# SQL ① Reserve：SELECT * FROM credit_reservation WHERE created_at > <demo start> ORDER BY id DESC LIMIT 1;
# SQL ② Reconcile：SELECT * FROM credit_transaction WHERE source_type IS NOT NULL AND created_at > <demo start> ORDER BY id DESC LIMIT 1;
```

### Reviewer dispatch

- spec-compliance reviewer：核对 spec §3.4 adapter 的 aiservice.Chat 3 参数 / Langfuse Span/Generation 区分 / 错误路径
- code-quality reviewer：核对 ctx 传播 + error handling + ReAct loop 终止条件

---

## Task 3：V4 Coze Studio / Eino 源码深读笔记

### 实现清单

- [ ] T3.0：`mkdir -p docs/agent-mode`（仓库根目录的 `docs/` 下没有 `agent-mode/` 子目录，先建；本 feature 的产物加入此目录后即可与未来 14-feature 蓝本同源）
- [ ] T3.1：克隆 cloudwego/eino @ latest stable 到 `/tmp/eino-reading/`
- [ ] T3.2：克隆 coze-dev/coze-studio @ latest 到 `/tmp/coze-studio-reading/`
- [ ] T3.3：写 `docs/agent-mode/eino-coze-study-notes.md`（spec §5.2 outline）
  - §1.1 State 传递机制（≥ 1 段代码引用，10-30 行）
  - §1.2 节点编排（Chain vs Graph，≥ 1 段代码引用）
  - §1.3 Stream vs Generate 分流（≥ 1 段代码引用）
  - §2.1 Tool interface（≥ 1 段代码引用）
  - §2.2 工具调用 lifecycle（≥ 1 段代码引用）
  - §3.1 Coze 工具源（仓库结构摘要）
  - §3.2 是否值得借鉴（明确决策 + 理由）
  - §4 对 #3 tool-registry feature 的影响（直接复用 / 需要改造 / 风险点）
- [ ] T3.4：每段代码引用注明文件路径 + 行号 + commit hash
- [ ] T3.5：用 `wc -m` 校验中文字符数 ≥ 3000（注意 wc -m 数 unicode 字符）
- [ ] T3.6：commit `docs(phase0-v4): Eino + Coze Studio 源码深读笔记`

### 验收

```bash
wc -m docs/agent-mode/eino-coze-study-notes.md       # ≥ 3000
grep -c "^### " docs/agent-mode/eino-coze-study-notes.md   # ≥ 8 个三级标题
grep -E "\.go:\d+" docs/agent-mode/eino-coze-study-notes.md | wc -l  # ≥ 6 个代码引用
```

### Reviewer dispatch（文档专项）

- 文档质量 reviewer：3 个核心问题（§1.1 §2.1 §3.2）必答 + 代码引用准确 + 字数 ≥ 3000

---

## Task 4：V1 KVM + Daytona OSS dev 服务器实测

### 实现清单

- [ ] T4.1：用 `DEV_SSH_*` 凭据 SSH 到 49.233.219.254（注意：用 sshpass + 环境变量，不在 shell history 留密码）
- [ ] T4.2：`sudo apt-get install -y cpu-checker && kvm-ok > /tmp/kvm-ok.log`，把输出写进 ADR
- [ ] T4.3：若 KVM 不可用 → 跳到 T4.8 写"A1 失败"ADR，触发 V5 备选；继续 T4.4-T4.7
- [ ] T4.4：`mkdir -p /opt/daytona && cd /opt/daytona && curl 下载 Daytona OSS docker-compose.yml`
- [ ] T4.5：`docker compose up -d`，等待 60s，`docker compose ps` 校验全 healthy（重试 3 次失败 → 触发 V5 备选）
- [ ] T4.6：API 创建 Python 3.11 workspace + 执行 `print("hello")`，截图 / 命令输出归档
- [ ] T4.7：`docker compose down -v`（**清理！不留 idle workspace 占资源**）
- [ ] T4.8：本地写 `.ndf/decisions/agent-mode-phase0-verification/0001-kvm-daytona.md`（spec §2.3 模板）
- [ ] T4.9：上传截图到 `.ndf/decisions/agent-mode-phase0-verification/screenshots/`
- [ ] T4.10：commit `docs(phase0-v1): KVM + Daytona OSS dev server verification ADR`

### 验收

```bash
# ADR 文件存在 + 包含必填字段
test -f .ndf/decisions/agent-mode-phase0-verification/0001-kvm-daytona.md
grep -q "^## Status$" .ndf/decisions/.../0001-kvm-daytona.md
grep -q "kvm-ok 输出" .ndf/decisions/.../0001-kvm-daytona.md
grep -q "docker compose ps 输出" .ndf/decisions/.../0001-kvm-daytona.md
grep -q "Decision" .ndf/decisions/.../0001-kvm-daytona.md
```

### Reviewer dispatch（文档专项）

- ADR 质量 reviewer：spec §2.3 模板字段完整 + Decision 明确 + Consequences 描述对 V5 / #4 影响

### 风险

- **如果 SSH 凭据失败**：**必停**，让用户检查 `.claude/settings.local.json` 中的 `DEV_SSH_*`
- **如果 Daytona OSS 镜像 pull 失败**：3 次重试，超过 → 触发 V5 备选

---

## Task 5：V5 沙箱方案 ADR（依赖 Task 4 完成）

### 实现清单

- [ ] T5.1：读 Task 4 输出的 V1 ADR（特别是 Findings 段）
- [ ] T5.2：根据 V1 实测结果，从 Option A/B/C 三选一（spec §6.2）：
  - **A1=YES**：选 Daytona OSS v1 + CubeSandbox v2 升级（与蓝本决策 #5 一致）
  - **A1=DEGRADED**：选 Daytona OSS v1 + 加风险条款（v2 提前到 6-9 月）
  - **A1=NO**：选 Docker pool（v1）或 CubeSandbox 提前（v1，技术风险高）—— **覆盖蓝本决策 #5**，ADR 顶部明确注明
- [ ] T5.3：写 `.ndf/decisions/agent-mode-phase0-verification/0002-sandbox-final.md`（spec §6.2 模板）
  - Options Considered（A/B/C 详细对比 + V1 实测引用）
  - Decision（明确选定）
  - Consequences（对 #4 sandbox-integration 的影响）
  - Open Questions（网络白名单 / 资源 quota / 撤销周期）
  - Trigger Conditions for Revisit
- [ ] T5.4：若覆盖蓝本决策 #5 → ADR 顶部加 "Overrides architecture-v1.md decision #5" 块
- [ ] T5.5：commit `docs(phase0-v5): final sandbox ADR — <选定方案>`

### 验收

```bash
test -f .ndf/decisions/agent-mode-phase0-verification/0002-sandbox-final.md
grep -q "^## Decision$" .ndf/decisions/.../0002-sandbox-final.md
grep -qE "^## Open Questions" .ndf/decisions/.../0002-sandbox-final.md
```

### Reviewer dispatch（文档专项）

- 决策质量 reviewer：三选一明确 + 理由有 V1 实测证据支撑 + 覆盖蓝本（若适用）明确注明 + Open Questions 实际可追踪

---

## S5 验证策略（NDF 规则 10 — 在 S3 plan 阶段必须显式给出）

### 验证方式

**仅后端 TDD + 手工演示 + reviewer 文档质量审**。不写 Playwright E2E，不跑 gstack `/qa`。

### 理由

| 因素 | 选择"仅后端 TDD + 手工 + reviewer 文档审" 的理由 |
|------|---------------------------------------------|
| 不出 UI | Phase 0 全部产物是 docs + cmd/ 下隔离 demo 二进制；无前端可点 |
| 不出业务功能 | 不在 SOP/Chatbot/Agent 用户流程内；用户视角看不到任何变化 |
| 一次性产物 | V1/V4/V5 是 ADR/笔记，未来不会"再用"；做 Playwright 没意义 |
| V2/V3 demo 单测保留为参考 | 但仅是参考样板，不构成回归基线（#2 / #6 会有自己的正式实现） |

### 关键用户路径

> 由于 Phase 0 不出用户功能，"路径" 仅为开发者自检路径：

1. **V3 路径**：`cd cmd/agent-phase0-bash-validator && go test -cover ./...` → 单测 PASS + 覆盖率 ≥ 90% + 20 个攻击向量矩阵 100% 命中
2. **V2 happy path**：`go run ./cmd/agent-phase0-eino-demo/` → final answer 输出含日期字符串
3. **V2 Langfuse path**：登录 Langfuse 后台找 trace（tag = `phase0-verification`），看见 ≥1 generation + ≥1 span + `generation.usage.total_tokens > 0`
4. **V2 billing path**：SQL ① + ② 校验（spec §3 + S1 proposal §4 V2 acceptance）
5. **V2 error path**：`go run ./cmd/agent-phase0-eino-demo/ --error-path` → stderr 输出错误 + Langfuse 后台可见 error generation + exit code != 0 + 不 panic
6. **V1 ADR path**：reviewer 校验 ADR 模板字段 + Findings 实测数据
7. **V4 笔记 path**：reviewer 校验 ≥ 3000 字 + 6 段代码引用 + 3 个核心问题必答
8. **V5 ADR path**：reviewer 校验三选一明确 + V1 实测引用 + 覆盖蓝本（若适用）注明

### 回归保护诚实声明

| 产物 | 回归保护？ | 说明 |
|------|----------|------|
| V1 ADR | ❌ 无 | 一次性环境验证；下次重新装 Daytona 需手工重做 |
| V2 demo 单测 | ⚠️ 局部 | `adapter_test.go` / `tools_test.go` 保留作样板。**不进 CI 主回归套件**；feature #2 启动时如继续用 demo 测试需手工跑 |
| V3 demo 单测 | ⚠️ 局部 | 同 V2。**不进主 server CI**（独立 go.mod 不被 `task lint` 扫描）。feature #6 启动时会重写正式版 + 进 CI |
| V4 笔记 | ❌ 无 | 静态文档，无回归概念 |
| V5 ADR | ❌ 无 | 决策记录，无回归概念 |

**关键诚实声明**：Phase 0 闭环后，**所有验证物均不进主 server CI 回归测试套件**。验证物的价值在于"沉淀团队心智模型 + 给后 13 个 feature 提供工程化模板"，不在于"持续守护代码不退化"。

### 必停场景（**真阻塞**，AI 必须暂停问用户）

1. **dev 服务器 SSH 凭据失败** — `.claude/settings.local.json` 中的 `DEV_SSH_*` 未配置或失效 → 让用户检查
2. **V2 Eino 集成失败超过 2 个工作日**（adapter 怎么写都不通） → 通报"触发 follow-up feature `agent-mode-eino-vs-custom-runtime-eval`" + 提议 AI 自主评估
3. **Langfuse 后台不可访问**（V2 验证 trace 写入受阻） → 让用户检查 Langfuse 配置
4. **V1 Daytona 部署占用过多 dev 资源影响其他服务** → **必停清理**

### NOT 必停场景（AI 自主推进）

- V1 KVM 不可用 → 自动进 V5 备选 ADR 路径
- V3 某个 validator 难写 → 标 P1 加 V5 follow-up，本 feature 仍可闭环
- V4 笔记字数不够 → reviewer FAIL，AI 补字数 + 加代码引用 + 重审
- 任何 reviewer P0/P1 → AI 修复后重审

---

## 主 session dispatch 与 commit 验证流程（NDF Rule 8 + 12）

每个 Task 完成顺序：

1. 主 session dispatch implementer Agent（或 4 路并行，Tier 3 disjoint，先跑 `ndf-check-disjoint.sh`）
2. Implementer Agent 返回后，主 session（**注意在 worktree 根跑 git 命令，不是子目录**）：
   - `git -C /private/tmp/wt-agent-mode-phase0-verification-numind-server log --oneline -5`（看最近 5 个 commit，找当前 task 的 commit）
   - `git -C /private/tmp/wt-agent-mode-phase0-verification-numind-server status`（确认 working tree 干净）
   - 如果 working tree 不干净或 commit message 不匹配 task 内容：
     - `git -C ... diff` 判断是否合理 → 合理则主 session 直接 commit / 不合理则 dispatch fix Agent
3. 主 session **并行 dispatch 两个 Sonnet reviewer**（同 turn 内 2 个 Agent 调用，model: "sonnet"）
   - spec-compliance reviewer：核对 spec 一致性
   - code-quality reviewer / 文档专项 reviewer（视 task 性质）
4. Reviewer 输出 `<severity>: <file>:<line> — <rule-id> — <problem> — fix: <suggestion>` 格式
5. P0/P1 修复 → 必要时重新 dispatch reviewer
6. 两份 review PASS → 主 session 更新 manifest `progress.reviewed_tasks += 1`

> **机械化阻断（NDF v2）**：S6 跑 `ndf-done` 前预期 `reviewed_tasks == completed_tasks == 5`，不等就报错。

---

## ndf-done 前置门槛（S6 进入标准）

- [ ] manifest `progress.completed_tasks == 5`
- [ ] manifest `progress.reviewed_tasks == 5`
- [ ] **manifest `stage == S6`**（S3→S4→S5→S6 已依序更新；NDF Rule 4.4 要求阶段转换时立即更新 manifest）
- [ ] 5 个产物文件 / 目录全部存在并 commit
- [ ] V2/V3 单测 PASS
- [ ] V1/V5 ADR 含必填字段
- [ ] V4 笔记 ≥ 3000 字
- [ ] 无 P0/P1 残留
- [ ] **未部署到 dev/qa/prod 任一环境**（Phase 0 止步 develop merge）
- [ ] 跑 `ndf-done`，原子化 merge feature/agent-mode-phase0-verification → develop + 删 worktree + 清 state

---

**Plan 完成。等待独立 reviewer 审 plan 原子性 + S5 验证策略合理性。**
