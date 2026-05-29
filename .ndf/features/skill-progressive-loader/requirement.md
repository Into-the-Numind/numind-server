# skill-progressive-loader — invoke_skill 架构重构（Codex/Claude Code 风格）

## 来源
- 提出人：zchen27@tulane.edu（dev 测试触发）+ 双参考项目调研
- 提出日期：2026-05-29
- 触发事件：dev agent_run_id=83 用户跑「找最近一周最热门 AI 创业项目并出 PPT」任务时 `invoke_skill pptx-author` 失败两次：

  ```
  2026-05-29 18:45:51 invoke_skill: skill code exited non-zero
    stderr: "ModuleNotFoundError: No module named 'invoke_skill'"
    stdout: "[ERROR] 无法导入 invoke_skill，请确认 skill 环境已部署。"
  2026-05-29 18:47:17 (重试) 同样的 ModuleNotFoundError
  ```

  内层 LLM 生成的 Python 用了 `import invoke_skill` —— 沙箱里没有这个 module。

## 需求描述

**当前架构（broken）：**

```
  ┌─ 外层 agent LLM ─────────────────────────────┐
  │  tool: invoke_skill(skill_name, instructions, input_files) │
  └──────────────────┬────────────────────────────┘
                     │
                     ▼
  ┌─ invokeSkillTool.Execute（Go 编排）─────────────────────┐
  │  Step 7: aiservice.Chat(profile.AgentRun) ─┐           │
  │    prompt = "你是代码生成专家... 以下是   │           │
  │              SKILL.md... 请生成 Python..." │           │
  │                                            ▼           │
  │  ┌─ 内层 LLM ─────────────────────────────┐           │
  │  │ 读 SKILL.md 的伪代码示例：              │           │
  │  │   import invoke_skill                  │           │
  │  │   invoke_skill("pptx-author", {...})  │           │
  │  │                                        │           │
  │  │ 「照搬即可」→ 生成 Python：             │           │
  │  │   import invoke_skill  ← 沙箱无此 module│          │
  │  └────────────────────────────────────────┘           │
  │  Step 8: docker exec python3 /workdir/skill_run.py    │
  │    ModuleNotFoundError → exit 1                       │
  └────────────────────────────────────────────────────────┘
```

**两个抽象层级混淆：**

- `pptx-author/SKILL.md` 用 **declarative 伪代码**示例（`invoke_skill("pptx-author", {...})`）—— 这是给**外层 agent LLM** 看的「如何调用本 skill」说明书
- 但 `invokeSkillTool.Execute` 把整篇 SKILL.md 当 prompt 喂给**内层 LLM**，要求它生成在沙箱里跑的真实 Python
- 内层 LLM 看「说明书」抄作业，把 declarative 伪代码当真，写出 `import invoke_skill`

**两个参考项目的设计验证（subagent 调研）：**

1. **Codex** (`codex-rs/core-skills/src/loader.rs` + developers.openai.com/codex/skills)：
   - SKILL.md = **prompt-as-skill**（progressive disclosure）
   - 初始 context 只塞 name+description+path（~8KB budget）
   - 用户/模型决定触发后，**完整 SKILL.md body 加载进同一个 agent loop**
   - **没有内层 LLM**：SKILL.md 教外层 agent 怎么写真实 python-pptx、然后用 `shell` tool 执行
   - 单 agent loop + progressive 加载

2. **Claude Code** (`SkillTool.ts::runAgent` + `loadSkillsDir.ts`)：
   - SKILL.md = **forked agent prompt**
   - SkillTool 触发 → 创建 **forked sub-agent**（独立 token budget）
   - SKILL.md 内容作为该 sub-agent 的 user message
   - sub-agent 使用 **真实工具名**（`${AGENT_TOOL_NAME}` 占位符），不是伪代码
   - 单层级 LLM（外层调 sub-agent，但 sub-agent 也是同一类 agent，用同一套 tool registry）

**两个项目都不存在「内层 LLM 看 SKILL.md 写 Python」的二级抽象。**

## 业务目标

1. **解锁 PPT/Excel/Word/PDF 生成功能在 dev/prod 稳定可用** —— 这是 SOP 工作台「最后 N 公里」交付能力，B2B/B2C 客户都在等
2. **消除架构错配** —— 当前的"declarative SKILL.md + generative inner LLM"组合在 LLM 不犯错时偶尔走通（dev log 显示 agent_run_id=78 第三次重试简化英文指令后成功一次），但 deterministic 失败率高，是不可上 prod 的状态
3. **后续 skill（xlsx-author / docx-author / pdf-from-html）开发成本可控** —— 当前模式下每个 skill 都要靠 LLM 抄 SKILL.md 伪代码"猜对一次"，新增 skill 都要靠运气；按 Codex/Claude Code 范式重构后，SKILL.md 写法明确，新增 skill 只是新写一份指令文档
4. **可测性提升** —— 当前架构内层 LLM 输出 nondeterministic，端到端测试只能靠"跑十次看有没有 9 次过"；改成单层 agent + run_python 后，可以测试 agent 选用了正确的 skill + 调用了 run_python with sensible code，行为更可观测

## 优先级

**高**。这是 dev 用户实测撞到的功能性 P0 bug，pptx/xlsx/docx/pdf-from-html 四类 invoke_skill 全部受影响。Hotfix A 让 agent 不再被 `file_read` 杀掉，但用户仍然拿不到 PPT 输出。Hotfix A 已稳定 dev，下一步交付的核心阻塞就是 B。

## Triage

- **推荐轨道：Standard**
- **分类理由（5 条标准）**：
  1. 数据库 schema 变更：**否**（skill 注册/manifest 已经在 `ai_service` 表，但本次重构是 invoke_skill 调用语义，不动表）
  2. 新增 API 端点：**可能否**（核心改动在 agent 内部 tool 实现；如果"skill registry list" 想暴露给前端可能需要新端点，待 S2 决定）
  3. 新外部服务集成：**否**（继续用现有 sandbox + run_python；不引入外部 service）
  4. 影响文件数：**>3**（核心改动至少 4 文件：`tool_invoke_skill.go` 重构 / `pptx-author/SKILL.md` 重写 / `tool_run_python.go` 可能需新 capability / `runner.go` 可能需 sub-agent dispatch；下游 xlsx/docx/pdf-from-html 的 SKILL.md 也要重写。预计 ≥6 文件）
  5. 高风险业务逻辑（支付/权限）：**否**（不涉及支付/会员/积分。但是 sandbox 安全模型本身有一定风险——LLM 写的代码在 docker 里跑——已有的 sandbox 隔离继续维持即可，不引入新风险）

- **人类决定：升级为 Standard**（user 在 2026-05-29 session 中明确选了 "深度（Standard）"，理由：「架构错配的根因不解决，下一个 skill 仍会踩，治标不治本」）

## 边界（in / out of scope）

**In scope（本次 Standard 解决）：**
- `invoke_skill` tool 的设计变更：是否保留？如保留，是否还要内层 LLM？
- `pptx-author/SKILL.md` 改成 Codex/Claude Code 风格
- skill registry 加载方式
- `run_python` tool 与 skill 工作流的耦合（如果选 Codex 风格：agent 写 python → 用 run_python 执行；如果选 Claude Code 风格：sub-agent fork + sub-agent 自己用 run_python）

**Out of scope：**
- 添加新 skill（xlsx/docx/pdf-from-html 的 SKILL.md 重写在 S4 内可顺手做，但新增 skill 类别如「视频生成」「图表交互式编辑」不在本次）
- sandbox 镜像本身的更新（如果需要新增 python lib，那是 sandbox 层的事，不在本 feature；如果当前 sandbox image 没装 `python-pptx` 那已是 Bug C 不是本次）
- 前端 UI 调整（除非 skill 列表暴露方式变了；S2 决定）
- B2B billing 影响（无）

## 关联历史

- **同次 dev 测试的伴随 Hotfix**：`file-read-accept-outputs-soft-errors`（已 merged develop 3f897660，部署 dev 健康）。两个 bug 同源（用户同一次 agent 任务暴露），不同根因，独立修复。
- **Hotfix `stream-emit-toolcall-events`**（merged 2026-05-29）：让 tool_call_start/result/error 事件流到前端。本次重构如果 invoke_skill 取消、改用 sub-agent dispatch + run_python，需要复审 SSE 事件是否还在正确语义上 emit。
- **manifest 中 stream-emit-toolcall-events 的 known_issues 字段**（2026-05-29 18:00）已经提到："生成 PPT 时第一次 invoke_skill 失败 AI 说'环境初始化遇到问题'然后重试成功（agent_run 78：17:24:58 首试→17:27:11 重试→17:28:34 成功 82s）"，并下结论"判断为 LLM 生成的 pptx 代码首次有误、AI 换简化英文指令重试自愈"。**本次 root cause 调查推翻了这个判断**：失败不是"LLM 偶尔出错"，是 deterministic 的「SKILL.md 教错对象 → 内层 LLM 抄成 import invoke_skill」。把 known_issues 字段标记为 superseded。
- **agent-mode 14 features 路线图**（docs/agent-mode/architecture-v1.md）：本 feature 不在原 14 个之列，是 14 完成后用户体验暴露的架构债。

## 备注

- 两种参考范式（Codex progressive-disclosure vs Claude Code forked sub-agent）的取舍是 S2 技术设计阶段的核心任务，本 requirement card 不预先决定。
- 重构方向必须维持 sandbox 隔离（LLM 写的 Python 仍只在 docker 里跑）。
- 重构后必须保留 langfuse trace/generation 记录（aiservice 入口唯一性 invariant I5）。
