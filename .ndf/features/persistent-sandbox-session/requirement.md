# persistent-sandbox-session — 沙箱在单次对话内持久化（设计评估）

> S0 工件 · 2026-06-01 · **本 feature 当前仅做设计评估（S0→S2），不进入 S3/S4 编码**，等用户看完方案/风险/工量后再决定是否实施。

## 来源
- 提出人：zchen27@tulane.edu
- 提出日期：2026-06-01
- 触发：`run-python-stateless-guidance` hotfix（2026-05-31，已部署 dev `develop-98c5566f`）交付后，用户追问三个架构问题：
  1. 任务太大、AI 只能完成一小部分，可能吗？
  2. 大任务能否拆 subagent / 多沙箱并行？
  3. Claude Code / Codex 怎么处理？我们的方式有何局限、要不要升级？

## 背景：当前架构 vs 业界

### 当前（无状态沙箱）
- `run_python` / `bash_exec` 每次调用：`SandboxHookManager.preToolCall` 从池 `Borrow` 一个 warm 容器 → 用 → `postToolCall` `Return`，而 `pool.go:Return` **同步 `Destroy` 容器**。
- `/workdir` 是**每容器独享的 tmpfs**（`security.go` `--tmpfs`，无宿主机挂载），容器销毁即蒸发。
- 结论：**每次工具调用 = 全新一次性沙箱**，文件不跨调用保留。
- 这正是 `run-python-stateless-guidance` 修的那个坑的根：LLM 误以为有状态，"call#1 存盘 → call#2 重开续写"，第二次的新容器里文件不存在。那个 hotfix 是**创可贴**——用提示告诉 LLM"一次做完、别重开"。

### 业界参考（两个 subagent 精读源码确认，2026-06-01）
| 维度 | 我们 | Claude Code | Codex |
|---|---|---|---|
| 工作目录 | 一次性 tmpfs，每调用后销毁 | **持久**（用户真实 cwd，全 session 不变；`Shell.ts` 全局 cwd + 磁盘文件） | **持久**（用户真实 repo workspace；`exec.rs` 复用同一 cwd） |
| 沙箱作用 | 隔离 **+ 销毁状态** | 仅**安全隔离**（per-command wrapper），不销毁 FS | 仅**安全隔离**（Seatbelt/Landlock per-command），不销毁 FS |
| 跨调用续写文件 | ❌ 第二次没了 | ✅ 文件一直在 | ✅ 文件在，`apply_patch` 可就地改 |
| Subagent / 并行 | ❌ 无 | ✅ AgentTool / spawnTeammate 可并发 | ✅ multi_agent_v1 spawn_agent 可并行 |
| "必须一次做完"提醒 | **有（刚加）** | **从无，不需要** | **从无，不需要** |

**核心洞察**：Claude Code 和 Codex 的工作目录全程持久，沙箱只给单条命令套安全壳、底下文件不动，所以 LLM "写一点→看一眼→再写一点"天然成立。我们的"一次性沙箱"是**刻意但与业界分歧**的选择；"一次做完"提醒是为绕开这个分歧的补丁。

## 问题陈述（要解决什么）

### 当前无状态设计的天花板
- **执行时间**（30s 默认 / 120s 上限）：几乎不会卡——50 页 PPT 的 python 跑完 <1s。
- **文件大小**：基本不会卡。
- **真天花板 = AI 单次回复能写多少 Python 代码**：生成整份文档的 Python 必须塞进**一次** LLM 回复（一次工具调用）。绝大多数文档塞得下；但**极端巨大**的生成（几百页 / 程序化堆砌海量内容）代码量可能超过单次输出 token 预算。此时 LLM：
  - 没法分两次（沙箱无状态，第二次没了）
  - 又塞不进一次
  - → **卡死，无解**

### Subagent / 并行（用户 Q2）
- 当前：**无 subagent 能力**（grep 全仓 0 命中），ReAct 循环**严格串行**（`react.NewAgent` MaxStep=120，一次一个工具）。
- 池里 5 个 warm 容器是给**不同对话/用户并发**用的，**不是**单任务并行。
- → "大任务拆开并行"目前走不通。

## 业务目标

1. **消除"超大文档无解"天花板**：让 AI 能像 Claude Code/Codex 那样在一次对话内**分步搭建**大文档（call#1 建框架 → call#2 续写 → call#3 加图表），文件跨调用保留。
2. **架构对齐业界主流**：把"一次性沙箱"升级为"对话内持久沙箱"，去掉别扭的"必须一次做完"提示，让 LLM 的自然多步工作模式直接成立（减少 LLM 困惑 / 误判 / 重试浪费）。
3. **为未来铺路**：持久工作目录是 subagent / 迭代式数据分析 / "读自己上一步产物再加工"等高级 agent 能力的前置基础设施。

## 优先级

**中**。当前创可贴已覆盖 99% 文档场景（dev 实测 5 页 PPT 端到端成功）。这是**消除天花板 + 架构对齐**的升级，不是救火。**prod 当前 `skills_enabled=false`（沙箱 dev-only）**，所以升级窗口宽松，可从容设计。

## Triage

- **推荐轨道：Standard**
- **分类理由（5 条）**：
  1. 数据库 schema 变更：**可能是**（容器↔agent_run 绑定的生命周期可能需要持久化追踪；`agent_sandbox_session` 表已存在，可能需加字段。S2 决定）
  2. 新增 API 端点：**否**（agent 内部基础设施改动）
  3. 新外部服务集成：**否**（继续用现有 docker sandbox）
  4. 影响文件数：**>3**（pool.go 池子重设计 + factory_sandbox_hooks.go 生命周期改造 + runner.go 终态清理钩子 + tool_run_python.go/tool_bash_exec.go 会话复用 + guidance 反转。预计 ≥6 文件）
  5. 高风险业务逻辑：**是**（沙箱安全面——容器从"秒级存活"变"对话级长活"，攻击面、资源泄漏、跨 run 隔离都需重新论证。这是本 feature 最需谨慎的维度）
- **人类决定**：用户 2026-06-01 选"先出个 Standard 设计方案供评估"——即走 S0→S2 出方案/风险/工量，**不进 S3/S4**，看完再定。

## 边界

**In scope（本设计评估覆盖）：**
- 容器生命周期从"per-tool-call"改为"per-agent-run"（一次对话一个容器，`/workdir` 跨调用保留）
- 池子容量重设计（5 个秒级复用 → 对话级长活，容量模型完全不同）
- 终态清理（run completed/error/aborted/timeout + 异常未达终态的 reaping）
- guidance 反转（"一次做完" → "可分步搭建"）
- feature flag 灰度（stateless 现状 ↔ stateful 新行为可切换）
- 安全面分析（长活容器的攻击面、资源上限、跨 run 隔离论证）

**明确 Out of scope（单独的未来 initiative，不在本 feature）：**
- **Subagent / 任务分解**（用户 Q2 的另一半）——这是比持久沙箱大得多的能力（agent 编排、上下文 fork、并发协调），独立立项
- **单任务多沙箱并行**——同上，依赖 subagent 框架
- run_python/bash_exec 的功能扩展（新库、新格式）
- prod 启用 sandbox（本 feature 只让架构就绪，prod 开关另议）

## 关联

- `run-python-stateless-guidance`（merged 2026-05-31）：本 feature 实施后，那个 hotfix 的"一次做完/别重开"guidance 需**反转**为"可分步搭建"。两者是同一问题的"创可贴 vs 根治"。
- `skill-progressive-loader`（merged 2026-05-29）+ 4 个 run_python wiring hotfix：本 feature 不动 read_skill/run_python 的**功能**，只改它们底下的**沙箱状态模型**。
- `sandbox-integration`（#4，原始沙箱框架）：本 feature 是对其池子/生命周期模型的重大演进。

## 备注

- 本 feature 的产出是**评估材料**：S1 给 2-3 个设计方案 + 推荐 + 风险表 + 工量；S2 给推荐方案的精确技术设计。S2 后**停下**，等用户拍板是否进 S3/S4。
- 安全性是头号约束：任何方案都必须保证**不同 agent_run / 不同用户之间的容器隔离 100% 不变**（一容器只服务一个 run，绝不共享）。
