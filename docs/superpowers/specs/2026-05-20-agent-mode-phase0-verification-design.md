# Agent 模式 Phase 0 前置验证 — 技术设计

**Spec date**: 2026-05-20
**Feature ID**: agent-mode-phase0-verification（agent-mode 14-feature 分解 #1/14）
**Track**: Standard
**Status**: DRAFT（S2 阶段，待 reviewer pass）
**架构蓝本**: `docs/agent-mode/architecture-v1.md` §4.1 / §4.6 / §10 / §11

## §1 设计概览

### 1.1 目标

Phase 0 验证 4 个关键技术假设，输出 5 个验证物（V1-V5）。本 spec 给出 V2 / V3 demo 代码的**接口设计**、V1 / V4 / V5 文档的**结构骨架**，以及跨验证物的**公共契约**（如独立 go.mod 模块约定 / Langfuse 集成模式 / 与主 server 的安全边界）。

### 1.2 修复/验证的 4 个核心假设

| 假设 ID | 假设内容 | 验证物 | 验证后果 |
|---------|----------|--------|---------|
| **A1** | 腾讯云 dev 服务器（49.233.219.254）支持嵌套 KVM，可跑 Daytona OSS | V1 | A1 ✓ → 沙箱选 Daytona；A1 ✗ → 沙箱选 Docker pool 或 CubeSandbox 提前 |
| **A2** | `cloudwego/eino` 框架可以通过 adapter 模式包装 `aiservice.Chat()` 作为 ChatModel，不丢 Langfuse trace / billing / 路由 | V2 | A2 ✓ → #2 runtime-skeleton 用 Eino；A2 ✗ → 触发"Eino vs 自研最小 Runtime"评估 |
| **A3** | 8 个 P0 Bash validator（来自 Claude Code v2.1 拆解）可以用 Go 表达，且能拦截 20 个典型攻击向量 | V3 | A3 ✓ → #6 permission-pipeline 继承代码；A3 ✗ → 部分 validator 标 P1，加入 V5 ADR follow-up |
| **A4** | Eino Graph API 心智模型可被团队（即我）通过深读建立；Coze Studio 工具加载机制可被借鉴或明确否定 | V4 | A4 ✓ → #3 tool-registry S2 spec 有清晰参考；A4 ✗ → 进 #3 时需重读源码 |

### 1.3 关键不变量（设计必须保持）

1. **Phase 0 不动主 server 代码**：V2/V3 demo 在 `cmd/agent-phase0-*/` 独立 `go.mod`，主 `numind-server/go.mod` 零变更；V4/V5 仅写文档；V1 仅产 ADR + 截图，不动代码。
2. **不动 DB schema**：Phase 0 demo 不创建任何 agent_* / sandbox_* / credit_admin_test_grant 表。V2 demo 调 Reserve/Reconcile 是**复用现有** credits 链路，不改 schema。
3. **不动 prod 环境**：所有验证活动在 dev 服务器 + 本地 + 构建机进行；不打 prod tag；不修改 config_prod.yaml；不部署 demo 二进制到 dev container 服务。
4. **aiservice 唯一入口**：V2 demo 必须通过 `aiservice.Chat(ctx, taskID, req)`（3 参数签名）调 LLM，禁止裸 HTTP；这是 Langfuse trace + billing + 路由降级三件套的基础。
5. **凭据隔离**：Daytona / Coze API key 经环境变量（`.claude/settings.local.json`），禁止进 Git / config 文件 / docker-compose.yml 明文。

---

## §2 V1 设计：KVM 可用性 + Daytona OSS 部署

### 2.1 流程

```
SSH 到 49.233.219.254（DEV_SSH_HOST, root, DEV_SSH_PASS）
    ↓
sudo apt-get install -y cpu-checker  # 安装 kvm-ok
    ↓
kvm-ok > /tmp/kvm-ok.log              # KVM 可用性 → ADR 附录
    ↓
docker volume create daytona-data
    ↓
mkdir -p /opt/daytona && cd /opt/daytona
    ↓
curl 下载 Daytona OSS docker-compose.yml（官方 release）
    ↓
docker compose up -d                  # 启动 Daytona services
    ↓
docker compose ps > /tmp/daytona-ps.log
    ↓
等待健康检查 60s，再 docker compose ps 校验
    ↓
curl http://localhost:3986/api/workspaces -X POST ... # 创建 Python 3.11 workspace
    ↓
curl ... -X POST /api/workspaces/{id}/exec -d '{"cmd": "python -c print(\"hello\")"}'
    ↓
所有结果归档到 .ndf/decisions/agent-mode-phase0-verification/0001-kvm-daytona.md
    ↓
docker compose down -v                # 清理！不留 idle workspace
```

### 2.2 失败处理决策树

| 失败点 | 行为 |
|--------|------|
| SSH 凭据失败 | **必停**，让用户检查 `.claude/settings.local.json` 中的 `DEV_SSH_*` |
| `kvm-ok` 输出非"KVM acceleration can be used" | A1 失败；记录到 V1 ADR；触发 V5 选 Docker pool |
| Daytona docker compose 启动失败 | 排查 3 次：网络 / 镜像 pull / 端口冲突 / volume 权限；3 次失败 → A1 失败，V5 备选 |
| Workspace 创建失败但 Daytona 跑通 | 视为 A1 部分失败：Daytona infra OK 但 API 不可用 → V5 ADR 注明，可能仍选 Daytona 但加风险条款 |
| Python 执行失败 | 视为 A1 部分失败：基础设施 OK 但运行时不可用 → 同上 |

### 2.3 V1 ADR 结构（`.ndf/decisions/agent-mode-phase0-verification/0001-kvm-daytona.md`）

```markdown
# ADR-0001: KVM + Daytona OSS dev 服务器验证

## Status
[Accepted | Rejected | Partial]

## Date
2026-05-21

## Context
... (背景)

## Findings
### KVM Availability
- 命令：`kvm-ok`
- 输出：`<贴粘 stdout>`
- 判定：[YES / NO / DEGRADED]

### Daytona OSS Deployment
- 镜像 tag：daytonaio/daytona:vX.Y.Z
- docker compose ps：`<贴 docker compose ps 输出>`
- 启动耗时：N 秒
- 健康检查：[全部 healthy / 部分 healthy / 失败]

### Workspace 创建
- API 调用：POST /api/workspaces
- 响应时间：M ms
- workspace id：`ws-xxx`
- Python 3.11：`print("hello")` 输出 = "hello\n"

## Decision
[继续 Daytona / 触发备选（Docker pool / CubeSandbox 提前）]

## Consequences
... (对 V5 ADR 和 #4 sandbox-integration 的影响)

## 截图附件
- 路径：`.ndf/decisions/agent-mode-phase0-verification/screenshots/`
```

---

## §3 V2 设计：Eino + aiservice 整合 demo

### 3.1 模块归属决策（S2 reviewer P0 后修正）

**核心约束**：`aiservice` / `langfuse` 是 `internal/pkg/*` 包，Go 工具链强制规则——**只有与 `internal` 父目录同模块的代码才能 import**。即使用 `replace` directive 把主 server 包装成本地依赖，也**不绕过 internal 规则**（Go 工具链按"包的物理位置 + 调用者的模块根"判定，replace 不改变这一点）。

因此 **V2 demo 必须放在主 `numind-server` 模块内**，不能用独立 `go.mod`。

**结果**：Eino 依赖进入主 `numind-server/go.mod`。Trade-off 接受：
- **若 A2（Eino+aiservice 整合）成功** → feature #2 `agent-mode-runtime-skeleton` 会持续使用 Eino，go.mod 依赖天然合理
- **若 A2 失败** → 立即在 follow-up feature `agent-mode-eino-vs-custom-runtime-eval` 评估替代方案；评估期间 Eino 依赖临时保留在 go.mod，评估结论确定后 `go mod tidy` 移除

### 3.2 目录结构

```
numind-server/                       # 主 module（go.mod 中 module name = "numind-server"）
├── go.mod                           # **新增 Eino 依赖到此处**
├── cmd/
│   └── agent-phase0-eino-demo/      # **属于主 module，无独立 go.mod**
│       ├── main.go                  # 入口（package main）
│       ├── adapter.go               # Eino ChatModel → aiservice adapter
│       ├── tools.go                 # 1 个 demo 工具（get_current_date）
│       ├── observability.go         # Langfuse trace 注入
│       ├── adapter_test.go
│       ├── tools_test.go
│       └── README.md                # 怎么跑 + 验收记录
```

跑 demo：`cd numind-server && go run ./cmd/agent-phase0-eino-demo/`

### 3.3 主 go.mod 变更

在 `numind-server/go.mod` 增加：

```go
require (
    github.com/cloudwego/eino v0.x.y       // 待 V2 实施时基于真实 latest 确定版本
    github.com/cloudwego/eino-ext v0.x.y   // 若 V2 需要 eino-ext 中的 component
)
```

S4 实施 V2 第 1 步：跑 `go get github.com/cloudwego/eino@latest`，pin 实际版本号。

### 3.4 Eino ChatModel Adapter 设计（S2 reviewer P0 后修正：API 签名校准）

Eino 框架定义了 `ChatModel` interface（来源：[github.com/cloudwego/eino/components/model](https://github.com/cloudwego/eino)）。**V2 实施第 1 步：读 `eino/components/model/interface.go` 真实签名，pin Eino 版本**。基于公开文档预期签名：

```go
// Eino 接口（V2 实施时核对源码）
package model

type ChatModel interface {
    Generate(ctx context.Context, in []*schema.Message, opts ...Option) (*schema.Message, error)
    Stream(ctx context.Context, in []*schema.Message, opts ...Option) (*schema.StreamReader[*schema.Message], error)
}
```

我们的 adapter（`adapter.go`）—— **module path 校准为 `numind-server`，aiservice.Chat 3-arg 签名校准**：

```go
package main

import (
    "context"
    "github.com/cloudwego/eino/components/model"
    "github.com/cloudwego/eino/schema"
    "numind-server/internal/pkg/aiservice"   // module = "numind-server"，主 module 内直接 import
    "numind-server/internal/pkg/langfuse"
)

const demoTaskID = "phase0-eino-demo"   // aiservice.Chat 必需的 taskID（billing 标识）

type AiserviceAdapter struct {
    modelName string // 通过 aiservice 路由获取实际 provider
}

var _ model.ChatModel = (*AiserviceAdapter)(nil)

// Generate: Eino schema.Message → aiservice.ChatRequest → aiservice.Chat(ctx, taskID, req) → schema.Message
func (a *AiserviceAdapter) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
    req := convertToAiserviceRequest(in, a.modelName, opts...)
    resp, err := aiservice.Chat(ctx, demoTaskID, req)   // 3 args: ctx, taskID, req
    if err != nil {
        return nil, err
    }
    return convertToEinoMessage(resp), nil
}

// Stream: 同样 3-arg 签名
func (a *AiserviceAdapter) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
    req := convertToAiserviceRequest(in, a.modelName, opts...)
    ch, err := aiservice.ChatStream(ctx, demoTaskID, req)   // <-chan ChatChunk
    if err != nil {
        return nil, err
    }
    return wrapChannelAsStreamReader(ch), nil               // channel → Eino StreamReader 适配
}
```

**关键设计点**：
1. **不修改 aiservice**，只在 demo 内适配
2. **trace context 通过 ctx 传播**（不是 adapter 字段），保证 Langfuse `tc := langfuse.FromContext(ctx)` 工作
3. **billing 自动获得**：aiservice.Chat 内部已有 Reserve/Reconcile 链路，adapter 透传即可；`taskID="phase0-eino-demo"` 标识便于验收 SQL 抽样
4. **路由降级自动获得**：aiservice 内部已有 route fallback，adapter 不感知
5. **import 路径校准**：module name 是 `numind-server`（不是 `git.youshu.io/numind-server`，已读 go.mod 第一行确认）

### 3.5 ReAct Loop demo

```go
// main.go
func main() {
    ctx := context.Background()

    // 1. 创建 Langfuse trace
    traceID := langfuse.TraceID()
    langfuse.CreateTrace(traceID, "phase0-eino-demo-trace",
        langfuse.WithUserID(0),
        langfuse.WithTraceInput(map[string]any{
            "user_question": "今天周几",
            "max_react_steps": 5,
            "demo_run": true,
        }),
        langfuse.WithTraceTags("phase0", "phase0-verification"),
    )
    ctx = langfuse.WithTrace(ctx, traceID)

    // 2. 构造 Eino ReAct Agent
    // **V2 实施第 1 步：核对 eino/flow/agent/react/react.go 中的 Config struct 字段名**
    adapter := &AiserviceAdapter{modelName: "qwen-turbo"}
    tools := []tool.Tool{newGetCurrentDateTool()}

    agent, err := react.NewAgent(ctx, react.Config{
        Model: adapter,
        Tools: tools,
        MaxSteps: 5,
    })
    if err != nil { log.Fatal(err) }

    // 3. 跑一次
    resp, err := agent.Run(ctx, "今天周几？")
    if err != nil { log.Fatal(err) }

    fmt.Println("Final answer:", resp.Content)
}
```

### 3.6 Observability 设计（S2 reviewer P0 后修正：Langfuse API 签名校准）

```go
// observability.go — 工具调用 span 注入
// 校准要点（已读 internal/pkg/langfuse/helpers.go）：
//   - CreateSpan(traceID, spanID, name, opts...)  // name 是第 3 个位置参数，不是 Option
//   - EndSpan(traceID, spanID, opts...)            // 需要 traceID 作为第一参数
func instrumentedToolCall(ctx context.Context, toolName string, fn func() (string, error)) (string, error) {
    tc := langfuse.FromContext(ctx)
    if tc == nil {
        return fn()
    }
    spanID := langfuse.SpanID()
    spanName := "span-tool-exec-" + toolName
    langfuse.CreateSpan(tc.TraceID, spanID, spanName,                  // ← name 是位置参数
        langfuse.WithSpanParent(tc.ParentObservationID),
    )
    defer langfuse.EndSpan(tc.TraceID, spanID)                          // ← traceID 是第一参数
    return fn()
}
```

按 `ai-service.md §3`：
- LLM 调用 → Generation（aiservice 内部已发；签名 `CreateGeneration(traceID, genID, opts...)` / `EndGeneration(traceID, genID, opts...)`）
- 工具执行 → Span（adapter 显式发，按上面校准的签名）
- LLM 失败 → Generation 含 `{"error": err.Error()}` 输出（aiservice 内部应已处理；若没有则 adapter 补救）

### 3.7 错误路径测试

```go
// 错误路径专用测试模式（main.go 通过 --error-path 命令行参数触发）
func runErrorPathDemo(ctx context.Context) {
    // 同 main.go 流程，但 adapter.modelName = "non-existent-model-xyz"
    // 期望：aiservice.Chat 返回 error；Langfuse 后台可见 error generation；demo 输出 stderr；exit code 非 0；但不 panic
    adapter := &AiserviceAdapter{modelName: "non-existent-model-xyz"}
    // ... 构造 ReAct Agent + Run
    // demo main() 用 defer recover 兜底任何 panic，转 log.Fatalf
}
```

由 `main.go` 通过命令行参数控制（`./agent-phase0-eino-demo --error-path`）。

---

## §4 V3 设计：8 个 P0 Bash Validator

### 4.1 目录结构

```
cmd/
└── agent-phase0-bash-validator/
    ├── go.mod                     # 独立 module（无主 server 依赖，纯字符串处理）
    ├── validator.go               # 公共 Validator interface
    ├── control_char.go            # V3.1: ASCII 控制字符（除 \t \n \r）
    ├── unicode.go                 # V3.2: Unicode RTL / NBSP / zero-width
    ├── carriage_return.go         # V3.3: \r 注入
    ├── command_substitution.go    # V3.4: $(...) / `...`
    ├── ifs.go                     # V3.5: IFS / $IFS 利用
    ├── proc_environ.go            # V3.6: /proc/*/environ 等敏感路径
    ├── backslash_operator.go      # V3.7: \x \u 编码绕过
    ├── brace_expansion.go         # V3.8: {a,b,c} 扩展爆炸
    ├── *_test.go                  # 每个 validator 独立 _test.go
    └── ATTACK_VECTORS.md          # 20 个攻击向量 + 拦截矩阵
```

### 4.2 公共 Validator interface

```go
package main

type Decision int
const (
    Allow Decision = iota
    Deny
)

type Result struct {
    Decision Decision
    ValidatorID string  // 哪个 validator 触发
    Reason string       // 人类可读的拦截原因
    Pattern string      // 匹配到的危险 pattern（如 "$(id)"）
}

type Validator interface {
    ID() string
    Validate(cmd string) Result   // Allow = Result{Decision: Allow}
}
```

### 4.3 8 个 validator 详细规则

| ID | 检测目标 | 触发条件（伪代码） | 典型攻击向量 |
|----|---------|-----------------|-------------|
| V3.1 ControlChar | ASCII 控制字符（0x00-0x1F 除 \t \n \r）+ 0x7F | `for _, r := range cmd { if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0x7F { Deny } }` | `cmd\x00rm` / `\x07exec` |
| V3.2 Unicode | RTL override (U+202E) / NBSP (U+00A0) / zero-width (U+200B U+200C U+200D U+FEFF) | unicode 字符表检测 | `ls‮.exe` |
| V3.3 CR | `\r` 单独存在（不在 `\r\n` 序列中）—— shell 把 \r 当行结束符 | regex `\r(?!\n)` | `echo hi\rrm /` |
| V3.4 CommandSubstitution | `$(...)` / `` `...` `` / `<(...)`/`>(...)` 命令替换 | regex `\$\(|\`\|\<\(|\>\(` | `echo $(id)` / `` echo `id` `` |
| V3.5 IFS | `$IFS` / `${IFS}` / `$'\t'` 等利用 IFS 拆分 | regex `\$\{?IFS\}?|\$'.*?'` | `cat${IFS}/etc/passwd` |
| V3.6 ProcEnviron | `/proc/*/environ` / `/proc/*/cmdline` / `/proc/*/maps` 敏感路径 | regex `/proc/[^/]+/(environ|cmdline|maps|status|fd)` | `cat /proc/self/environ` |
| V3.7 BackslashOperator | `\x` / `\u` / `\0` 十六进制/八进制编码（在 `echo -e` / `printf` 等场景被解释） | regex `\\\\[xu0-9]` 在 echo/printf 参数位置 | `echo -e '\x72\x6d /'` |
| V3.8 BraceExpansion | `{a,b,c}` brace expansion + `{a..z}` / `{1..N}` range expansion；嵌套 `{a,{b,c}}` 可爆炸大量参数 | 双 regex 任一命中即 Deny：`\{[^{}]*,[^{}]*\}`（逗号形式） OR `\{[^{}]*\.\.[^{}]*\}`（范围形式） + 嵌套深度检测 | `rm /{a..z}/{1..1000}` / `echo {a,{b,c}}` |

### 4.4 攻击向量库（20 个）

```markdown
# ATTACK_VECTORS.md — V3 测试矩阵

| # | 攻击向量 | 预期 validator | 预期 Decision |
|---|---------|---------------|--------------|
| 1 | `rm -rf /` | （非 v3 范围，靠白名单）| Allow（V3 不管语义） |
| 2 | `cmd\x00rm -rf /` | ControlChar | Deny |
| 3 | `ls‮.exe` | Unicode | Deny |
| 4 | `echo hi\rrm /` | CR | Deny |
| 5 | `echo $(id)` | CommandSubstitution | Deny |
| 6 | `` echo `id` `` | CommandSubstitution | Deny |
| 7 | `cat <(id)` | CommandSubstitution | Deny |
| 8 | `cat${IFS}/etc/passwd` | IFS | Deny |
| 9 | `$'\trm'` | IFS（ANSI-C quoting）| Deny |
| 10 | `cat /proc/self/environ` | ProcEnviron | Deny |
| 11 | `cat /proc/1/cmdline` | ProcEnviron | Deny |
| 12 | `echo -e '\x72\x6d'` | BackslashOperator | Deny |
| 13 | `printf '\x72\x6d /'` | BackslashOperator | Deny |
| 14 | `rm /{a..z}/{1..1000}` | BraceExpansion | Deny |
| 15 | `echo {a,{b,{c,d}}}` | BraceExpansion（嵌套）| Deny |
| 16 | `python -c print("hello")` | 全 PASS | Allow（正常命令）|
| 17 | `ls -la /home` | 全 PASS | Allow |
| 18 | `echo $HOME` | 全 PASS | Allow（普通变量替换非 V3 范围）|
| 19 | `cat file.txt \| grep foo` | 全 PASS | Allow（管道非 V3 范围）|
| 20 | `head -n 5 /etc/hostname` | 全 PASS | Allow |
```

**期望**：6 个 Allow + 14 个 Deny（case #1 `rm -rf /` 不在 V3 范围内 = Allow，加上 case #16-20 的 5 个正常命令；case #2-15 是 14 个 Deny）。每个 Deny 必须命中正确的 validator。

### 4.5 测试模式

每个 validator 自带 `_test.go`，含 happy path（应 Allow 的命令）+ 攻击 case（应 Deny）。`go test ./cmd/agent-phase0-bash-validator/` 单测覆盖率 ≥ 90%。

---

## §5 V4 设计：Coze Studio / Eino 源码深读笔记

### 5.1 目录与文件

```
docs/agent-mode/
└── eino-coze-study-notes.md   # ≥ 3000 字
```

### 5.2 内容结构（强制 outline）

```markdown
# Eino / Coze Studio 源码深读笔记

## §1 Eino Graph API 心智模型
### 1.1 State 传递机制
- 关键代码：cloudwego/eino/internal/state.go（vX.Y.Z）
- 引用代码 1（10-30 行）
- 解读
### 1.2 节点编排（Chain vs Graph）
- 关键代码：cloudwego/eino/flow/chain.go
- 引用代码 2
- 解读
### 1.3 Stream vs Generate 分流
- 关键代码：cloudwego/eino/components/model/interface.go
- 引用代码 3
- 解读

## §2 Eino 工具注册接口
### 2.1 Tool interface
- 关键代码：cloudwego/eino/components/tool/interface.go
- 引用代码 4
- 字段说明
### 2.2 工具调用 lifecycle
- 关键代码：cloudwego/eino/flow/agent/react/react.go
- 引用代码 5
- 解读

## §3 Coze Studio 工具加载机制评估
### 3.1 Coze 的工具源
- coze-dev/coze-studio 仓库结构摘要
- 加载代码引用 6
### 3.2 是否值得借鉴
- 决策：[借鉴 / 部分借鉴 / 不借鉴]
- 理由（结合架构蓝本 §4.2 ToolFactory 设计）

## §4 对 #3 tool-registry feature 的影响
- 直接复用：...
- 需要改造：...
- 风险点：...
```

### 5.3 deliverable 验证规则

- 字数 ≥ 3000（用 `wc -m` 中文字符数 ≥ 3000；reviewer 校验）
- 至少 6 段代码引用（每段 10-30 行，注明文件路径 + 行号 + commit hash）
- §1.1 §2.1 §3.2 三节必答（即 3 个核心问题）

---

## §6 V5 设计：沙箱方案 ADR

### 6.1 ADR 文件

`.ndf/decisions/agent-mode-phase0-verification/0002-sandbox-final.md`

### 6.2 模板结构

```markdown
# ADR-0002: 沙箱方案最终决策

## Status
Accepted

## Date
2026-05-26

## Context
... (背景：V1 KVM/Daytona 验证结果)

## Options Considered

### Option A: Daytona OSS (v1) + CubeSandbox (v2 升级)
- Pros / Cons / 实测数据（来自 V1）

### Option B: 自建 Docker pool
- Pros / Cons / 工作量估计

### Option C: CubeSandbox 提前到 v1
- Pros / Cons / 集成成本

## Decision
[A / B / C]

## Consequences
- 对 #4 sandbox-integration 的影响
- 对架构蓝本决策 #5 是否冲突（若是，注明"覆盖蓝本决策 #5"）

## Open Questions
- 网络白名单实测（v1 vs v2）
- 资源 quota（CPU / RAM / 磁盘）
- 撤销/清理周期

## Trigger Conditions for Revisit
- 当 ... 时，本决策需要重新评估
```

---

## §7 关键不变量（汇总，S2 reviewer 校准后）

1. **V2 在主 module / V3 独立 go.mod**（S2 reviewer P0 后修正）：
   - V2 demo 在 `numind-server/cmd/agent-phase0-eino-demo/` 属于主 `numind-server` 模块（直接 import internal/aiservice 必须如此）。Eino 依赖进入主 go.mod
   - V3 在 `cmd/agent-phase0-bash-validator/` 用独立 go.mod（零依赖纯字符串处理，不需要 internal access）
2. **aiservice 唯一入口**：V2 demo 必须 `aiservice.Chat(ctx, taskID, req)`（3 参数），禁止裸 HTTP（违反 = reviewer FAIL）
3. **Langfuse trace 完整性**：V2 demo 必须有 trace + ≥1 generation + ≥1 span（按 ai-service.md §3）；API 签名（`CreateSpan(traceID, spanID, name, opts...)` / `EndSpan(traceID, spanID, opts...)`）严格遵守
4. **凭据从 env**：Daytona / Coze API key 走 `.claude/settings.local.json`，禁止 commit
5. **prod 零影响**：V1-V5 全程不动 config_prod.yaml / 不 SSH prod 服务器 / 不打 v* tag
6. **可观测错误**：V2 错误路径必须 Langfuse 记录 error generation，不能只 panic
7. **V3 0 false negative**：20 个攻击向量必须 100% 被拦截（每个命中正确 validator）；false positive 可接受少量（V3 是 v1 原型，正式版在 #6）
8. **V4 字数硬门槛**：≥ 3000 字，否则 reviewer FAIL
9. **CI 隔离边界**（S2 reviewer P2 新增）：V3 独立 go.mod 不进主 server CI 扫描；V3 的 `go test` 必须显式在其目录跑（`cd cmd/agent-phase0-bash-validator && go test ./...`）。主 server `task lint` / `go test ./...` 不会自动覆盖 V3。S3 plan 的 S5 验证 task 必须显式调用 V3 测试
10. **V2 Eino 版本 pin**（S2 reviewer P2 新增）：V2 实施第 1 步是 `go get github.com/cloudwego/eino@latest` + `go mod tidy`，然后读 `eino/components/model/interface.go` 和 `eino/flow/agent/react/react.go` 真实 API 签名核对 spec 中的预期签名（Config 字段名 / Generate/Stream 参数）；不一致则按真实 API 调整 adapter

---

## §8 风险与缓解（补充 S1 proposal 的 R1-R7）

| 风险 | S1 标识 | S2 进一步缓解 |
|------|---------|---------------|
| R1 KVM 不可用 | S1 写明 | V5 ADR 模板已预留 Option B/C 备选 |
| R2 Eino adapter 写不出 | S1 写明 | §3.3 给出 adapter 接口骨架；V2 实施时基于此进一步实现 |
| R3 Daytona 部署坑多 | S1 写明 | §2.1 给出失败处理决策树（3 次重试硬门槛）|
| R4 Validator 边界 case 难表达 | S1 写明 | §4.3 给出 8 个 validator 的具体 regex/逻辑；V3 实施时调优 |
| R5 V4 跑题 | S1 写明 | §5.2 给出强制 outline，3 个必答问题 |
| R6 V2 token 不计入 | S1 写明 | §3.5 trace context 显式通过 ctx 传播，aiservice 内部已有 Reserve/Reconcile，确保不丢 |
| R7 Phase 0 闭环后发现补做 | S1 写明 | V5 ADR 模板有 "Open Questions" 段，#4 启动前必读 |

### 新增风险（S2 识别）

| ID | 风险 | 缓解 |
|----|------|------|
| R8 | Eino 版本与 aiservice 内部 schema 不兼容（如 streaming API 签名差异）| V2 实施时先用 Eino 最低可用版本（go.mod 锁定）；遇到不兼容立即降级 + 记录 V5 follow-up |
| R9 | V1 在 dev 服务器装 Daytona 后占资源，影响其他 dev 服务 | V1 完成立即 `docker compose down -v`；不持久化运行 |
| R10 | V3 validator 可能 false positive 误拦正常命令 | §4.4 攻击向量库 20 个中 5 个是 happy path，明确测试 false positive 边界；正式版 #6 会有更多 happy path 覆盖 |

---

## §9 实施依赖图（送 S3 plan）

```
V1 (KVM/Daytona) ─────────────┐
                              ├──→ V5 (沙箱 ADR)
                              │
V2 (Eino+aiservice demo) ─────┤
                              ├──→ S5 验收
V3 (Bash validator) ──────────┤
                              │
V4 (深读笔记) ────────────────┘
```

并行项：V1 / V2 / V3 / V4（4 路）
串行项：V1 → V5
集合点：S5（所有验证物完成后集中验收）

---

## §10 与架构蓝本的对照

| Spec 章节 | 蓝本对应章节 | 是否冲突 |
|-----------|-------------|---------|
| §2 V1 | §4.6 Sandbox / §10 决策 #5 / §11 W1 | 无（V1 验证蓝本决策 #5）|
| §3 V2 | §4.1 Runtime / §10 决策 #4 | 无（V2 验证蓝本决策 #4）|
| §4 V3 | §4.7 Permission / Appendix C / §11 W1 | 无（V3 实现 8 个 P0，蓝本要求 23 个完整版在 #6）|
| §5 V4 | §10 决策 #4 #5 | 无（V4 沉淀蓝本决策的依据） |
| §6 V5 | §10 决策 #5 / §11 W2 | **可能覆盖**：若 V1 失败选 Docker pool，V5 覆盖蓝本决策 #5（蓝本选 Daytona）。覆盖时 V5 ADR 顶部明确注明 |

---

**Spec 完成。等待独立 reviewer 审。**
