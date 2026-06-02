# Agent Mode 上线前测试设计（Prod-Readiness Test Plan）

> **目的**：在 agent mode 上线 prod 前，从三视角（内部工程师 / 非工程师配置者 / 非工程师使用者）系统性测试，找出硬 bug、软 bug、不符合用户习惯的设计与现状（对标业界最佳实践）。最终目标：保证上线后在 prod 环境最大程度稳定。
>
> **方法论铁律**：**测真实运行时行为，不信文档、规格与现有单测**（理由见 §0.2）。本计划由代码勘察 + 第一手验证产出，已发现 5 条上线红线（§0.1）。
>
> 评估基线：`numind-server` develop HEAD `f73bea85`（2026-06-01）；agent mode 仍在演进（近期 `open-tools-skill-as-guidance`、`agent-tool-schema-infra` 全开工具注册并移除"dead permission layer"；活跃 worktree `persistent-sandbox-session` S0）。
>
> 置信度标注：【确认】= 本次第一手验证；【高】= subagent 代码级 + file:line + 交叉佐证；【中】= 单点代码推断，待测试证伪。

---

## §0 上线就绪度结论（先读）

### 0.1 已发现的"红线"问题（go-live blocker）

| ID | 现象 | 证据 | 影响 | 置信度 |
|----|------|------|------|--------|
| **BLK-1** | **权限管线在非测试二进制中全局短路** | `biz/permission/gate.go:110-121`：`if flag.Lookup("test.v")!=nil { 真管线 } else { ForceAllow }`；commit `14754a39 "force release all permissions globally"`；`d381ee9c "full-open tool registration + remove dead permission layer"` | dev+prod 下所有工具权限、租户黑名单(L2)、IsDestructive 拦截、bashvalidator(经管线时) 全失效。**单测因 test.v 跑真管线而全绿 → 虚假信心** | 【确认】 |
| **BLK-2** | **agent run 不扣真实积分** | `aiservice/middleware/context_budget.go:398`：无 `ContextFragments` 即透传不计费；`biz/agent/adapter.go:193-253` `convertToAiserviceRequest` 只设 Messages/Tools，从不设 ContextFragments | credits 三池(trial/cycle/booster)对 agent mode 完全不生效；内存 BudgetTracker 的 credits/turns 两维又因接线缺口/死代码失效，仅 wall_time 900s 兜底 | 【确认】计费旁路 +【高】budget 维度 |
| **BLK-3** | **bashvalidator 不拦危险命令；run_python 零命令校验** | `bashvalidator/validator.go`（8 检查器仅防混淆字符）；subagent go test 探针：`rm -rf /`、`curl\|sh`、fork bomb、`base64\|sh`、变量拼接、路径遍历 全部 ALLOW；`tool_run_python.go:154` Python 源码直送沙箱 | prod 一旦开 sandbox（rollout Phase E2 明确要开），即任意命令/代码执行；dev 已挂 `docker.sock` → 可达宿主 | 【高】 |
| **BLK-4** | **`ask_user_question` 在默认流式路径断裂** | yield 协议仅在 `runner.go:1079-1130`（非流式 Run）；`runner_stream.go`/`runner_runstream.go` 零 yield 处理；前端 `AgentChatView.vue:37,69` 默认走 `startStream` | 澄清提问在主路径不渲染或显示"任务失败"；答题后无 resume → 永久卡死"等待 agent 继续" | 【确认】 |
| **BLK-5** | **流式路径 `currentRun` 全程为 null** | `stores/agentChat.ts`：currentRun 仅 terminal/reconcile 才赋值 | 顶栏状态徽标、取消按钮、budget 60%/100% 预警阻断弹窗、取消积分提示 在主用户路径全失效 | 【高】 |

> **附加红线候选（需 Wave 1 确认）**：**B2B2C 子账户无法使用 agent** — `student_run_lifecycle.go:671-686` `resolveDefinition` 要求 `ad.ParentUserID == userID`，真正的子账户（`parent_user_id!=null`）跑父账户 agent 会 `ErrSkillNotFound`。若属实，与平台 B2B2C 定位直接冲突。【高】

### 0.2 文档 vs 现实 的系统性偏差（元风险 — 最重要）

| 文档/规格声称 | 来源 | 现实 |
|---|---|---|
| "5 层 hook 全活" | e2e-rollout #14 §4 成功指标 | permission hook = no-op（BLK-1）；compliance 的 `CheckUserInput`(L0 注入)/`CheckLLMOutput`(输出合规) **全库零调用**；L0 LLM 分类器是永真 mock |
| "aiservice 唯一入口（含 billing reconcile）" | CLAUDE.md `ai-service.md §0`；I5 invariant | agent LLM 调用经过 billing middleware 但被短路、不 reserve/reconcile（BLK-2）；`image_gen` 还裸 HTTP 打 DMXAPI 绕过 aiservice |
| "bashvalidator 拦 rm -rf、curl\|bash" | CLAUDE.md §6b | 实测全放行（BLK-3） |
| "TerminalReason/LoopEvent 各 19 个 invariant" | CLAUDE.md §6b；I2/I7 | Go enum 实为 14 TerminalReason + 20 LoopEvent；其中 5 个 reason 是死代码（无产生点）；`state.go:51` 编译断言已漏一个值 |

**含义**：**任何基于文档、规格、现有单测做的"上线就绪"判断都不可信**。本系统每个子 feature 单测齐全（80-99% 覆盖、race 绿），但关键安全/计费路径在 prod-shape 二进制下行为与单测相反。测试必须以"真实运行时探针 + 真实环境黑盒"为准绳。

### 0.3 对测试策略的含义

1. **Wave 1 优先级 = 用真实探针确认/证伪 §0.1 的 5 条红线**（不是追覆盖率，是验真实行为）。
2. 凡"有单测且绿"的安全/计费路径，必须额外做 **prod-shape 验证**（编译非 test 二进制 / dev 部署后黑盒）。
3. 上线 gate 的核心不是"测试通过"，而是"红线已关闭或被显式接受风险"（§6）。

---

## §1 Agent Mode 功能全景（理解产出）

### 1.1 它是什么

基于字节 **Eino ReAct** 框架的**类 Claude-Code agent**：自主多轮调用工具完成任务。三类参与者：
- **配置者**（父账户，`parent_user_id=null`）：在 web-v3 `/config/agents` 搭建 agent（问卷/高级模式 + 装载 Skill）。
- **使用者**（代码称 "student"，= 子账户/终端 C 端用户）：在 web-v3 `/agent/chat` 对话使用。
- **管理员**（admin 端）：`/admin/agent-runs` 监控运行、强制取消、Langfuse trace 跳转。

按 credits 计费（设计上，实际见 BLK-2）。backend `biz/agent/` ≈ 36k 行 / 180 文件，14 个开发 feature（#1-#14）已全 merged。

### 1.2 架构分层与控制流

- **入口**：`runner.go::Run`（同步，yield/resume 用）/ `runner_runstream.go::RunStream`（SSE 流式，**生产默认**）。
- **ReAct loop**：Eino `react.Agent`（`MaxStep:120`）；adapter `aiserviceAdapter` 实现 `model.ToolCallingChatModel`，把 Eino ↔ `aiservice.Chat`。
- **状态机** `state.go`：14 个 Go `TerminalReason`（completed / model_error / aborted_streaming / prompt_too_long / permission_denied / error_max_budget / waiting_for_user_choice / hook_stopped … + 5 个**死代码** reason）+ 20 `LoopEvent` + 5 `HookAction`（Continue/Stop/BlockingStop/PermissionDeny/BudgetExceeded）。
- **Hook chain（外→内，固定）**：`compliance → permission → budget → sandbox → narration`（装配于 `biz.go:343-367`）。注意 bashvalidator **不在** chain 内，在 `tool_bash_exec.go::Execute` 内部跑。
- **上下文压缩** `adapter_compactv2.go`：仅实现 L3（LLM 摘要）；token 用 `len/4` 估算（中文严重低估）；contextWindow 拿不到时静默退 32K。
- **SSE 事件模型** `stream/events.go`：14 个 EventType（token_delta / reasoning_delta / assistant_message / tool_call_* / step_done / state_change / **question_prompt** / terminal / error / ping）。
- **流式锁 + cancel 注册表**：`stream/lock.go` + `runner.go` 的 `cancels map` 均**进程内**（无 Redis），多实例失效。

### 1.3 工具清单（21 个）

| 工具 | 用途 | sandbox | 网络 | 配额 | baseline 默认 |
|------|------|:---:|:---:|------|:---:|
| `bash_exec` | sandbox 内 shell | ✓ | ✗ | `EnableSandbox` | 开关(code_sandbox/dangerous) |
| `run_python` | sandbox 内 Python + 输出上传 COS | ✓ | ✓ | `EnableSandbox` | **baseline 常开** |
| `web_search` | Tavily 搜索（5min 缓存） | | ✓ | — | 常开 |
| `web_fetch` | 网页→Markdown（有 SSRF 防护） | | ✓ | — | 常开 |
| `kb_search` | SalesRAG 向量检索 | | 内网 | — | 常开 |
| `file_read` | 读用户上传文件(PDF/图/文本) | | ✓ | — | 常开 |
| `image_gen` | 文生图（**裸 HTTP 打 DMXAPI**） | | ✓ | `EnableImageGen` | 开关(media) |
| `analyze_image` | 视觉描述+OCR（缓存是 dead stub） | | ✓ | per-run 10 | 常开 |
| `annotate_image` | 区域标注 | | ✓ | per-run 5 | 常开 |
| `create_text/json/csv/html/png_chart` | 生成产物上传 COS | | ✓ | — | 常开 |
| `document_generate` | **STUB（IsEnabled=false，永不可用）** | | | | — |
| `memory_write/read` | 用户全局长期记忆(L2) | | | — | 常开 |
| `ask_user_question` | 暂停 run 抛选择题（yield） | | | — | 常开 |
| `load_skill` | 加载技能指引（DB + 磁盘） | | | `EnableSkills` | **baseline 常开** |
| `get_current_date` | 当前 UTC 日期 | | | — | 常开 |
| `learner_data_query` | 查学员脱敏档案（**无水平 ACL，IDOR**） | | | — | 常开 |

### 1.4 Skill 系统

- **v1**：内嵌 `agent_definition.generated_skill_body / custom_skill_body`（deprecated 未删）。
- **v2**：独立 `skill` 表 + `agent_skill_binding`（多对多、排序、软删、版本历史回滚）。
- **marketplace**：两阶段脱敏（正则 PII + LLM 机构/产品名）→ 发布；订阅=克隆副本到订阅方租户。仅父账户可发布/订阅。
- **平台磁盘 skill**：xlsx/docx/pptx/pdf author（`skills.Registry` 扫描磁盘，所有 agent 可用）。
- **runtime 双读**：v2 binding 存在走 v2，否则 fallback v1（**v2-only agent 且 DB 故障 → 空 body 静默运行**，见风险）。

### 1.5 记忆 / 沙箱 / 压缩 / 旁白 / 多模态

- **记忆**：L1 短期（`agent_session_memory`，per-agent，含 embedding）+ L2 全局（memory_write/read）+ AGENT.md cascade（部署级 + 用户级）。
- **沙箱**：真 Docker（`--cap-drop=ALL` / no-new-priv / seccomp / read-only / `--network=none` / 非 root）；**seccomp 是 default-ALLOW**（仅 29 条黑名单，弱于 Docker 默认）；dev 挂 `docker.sock`。
- **压缩**：见 1.2，长会话/中文是重点风险。
- **旁白(narration)**：把工具调用翻译成中文进度；`NarrationRunID` 写在**池级共享 struct** → 并发 run 串流（P0）。
- **多模态**：图片/PDF 走 inline 或 text-fallback（`agent_attachment` 异步 OCR/vision）。

### 1.6 数据模型与 API 面

- **9 张表**：`agent_run`（status 仅 running/terminated）、`agent_definition`(+history)、`agent_permission_config`(+decision_log)、`agent_sandbox_session`、`agent_session_memory`、`agent_message_search`(ngram FULLTEXT，AutoMigrate 不建)、`agent_tool_artifact`、`agent_attachment`。
- **API**：用户端 ~29 端点（agent-runs CRUD/stream/answer/cancel/extend-budget、sessions、skills v1/v2、marketplace、search）+ 管理端 2 端点（list / force-cancel）。
- 详见 §1 末尾"完整端点表"（略，见 subagent 调研，可按需展开）。

### 1.7 环境矩阵（什么在 prod 开/关）

| 维度 | local | dev | qa | **prod** |
|------|:---:|:---:|:---:|:---:|
| sandbox backend | docker | docker | （无→disabled） | **（无→disabled）** |
| permission 决策 | ForceAllow | ForceAllow | ForceAllow | **ForceAllow** |
| 真实积分扣减(agent) | ✗ | ✗ | ✗ | **✗** |
| compliance L0/输出 | 未接线 | 未接线 | 未接线 | **未接线** |

> 关键：prod **当前** bash/python 因无 sandbox 配置而降级报错（BLK-3 是 latent）；但 rollout Phase E2 明确要在 prod 加 sandbox 配置 → 上线动作本身会"武装" BLK-3。**测试必须覆盖"prod 开 sandbox 后"的形态。**

---

## §2 测试策略总则

1. **测真实行为优先**：目标环境 = dev 真实后端（9091 用户 / 9099 admin）+ web-v3 dev（9200）+ admin dev（9100）+ dev MySQL + dev Langfuse；**禁止碰 prod**（CLAUDE.md 硬规则）。安全红线必须用 **prod-shape 二进制**（非 test.v）验证。
2. **分层覆盖**：工程师层（Go unit/integration、`-race`、API 探针、并发脚本、Langfuse trace 检查、dev DB SQL 断言）+ 旅程层（Playwright E2E 持久回归 + gstack `/qa` 一次性截图 QA）。
3. **风险优先**：Wave 1 先打红线，再铺全量。
4. **回归保护诚实声明**：Playwright/Go test = 永久回归；gstack `/qa`、手动 smoke = 一次性（无持久保护）。高风险路径（计费/权限/会员）按 NDF Rule 10 必须落 Playwright/Go。
5. **每条 finding 标置信度 + 期望 vs 疑似实际 + 严重度（P0/P1/P2）**；客户/线上类 bug 走 NDF Rule 11（先复现测试再修）。
6. **工具链**：`go test ./... -race`、`task test`、Playwright（`e2e/`，凭据走 `$E2E_USERNAME/$E2E_PASSWORD` + 子账户 `$E2E_STUDENT_*`）、gstack `/qa`、curl/httpie、Langfuse UI、`sshpass` 连 dev MySQL。

---

## §3 视角一：内部工程师

> 每条 charter：**目标 / 方法 / 关键用例 / 期望 / 待证伪疑似缺陷 / 严重度**。

### 3.1 安全 — 权限/沙箱/合规（最高优先）

| ID | 目标 | 方法 / 关键用例 | 期望 vs 疑似 | 严重度 |
|----|------|----------------|--------------|:---:|
| ENG-SEC-1 | 权限是否真生效 | prod-shape 二进制下，配租户 L2 黑名单(`agent_permission_config` regex_deny/tool_blacklist)，让 agent 调被禁工具/命令 | 期望被拦；疑似全放行(**BLK-1**) | P0 |
| ENG-SEC-2 | bashvalidator 语义绕过矩阵 | dev 开 sandbox，构造 `rm -rf /`、`curl x\|sh`、`:(){:\|:&};:`、`X=rm;$X -rf`、`base64 -d\|sh`、`cat /workdir/../etc/shadow`、`/proc/self/mem` | 期望危险被拦；疑似全放行(**BLK-3**) | P0 |
| ENG-SEC-3 | 沙箱逃逸面 | 容器内试访问宿主、`docker.sock`、`/proc/self/mem`、凭据文件；验证 seccomp default-ALLOW 影响 | 期望隔离牢固；疑似可读敏感/可达宿主 | P0 |
| ENG-SEC-4 | compliance 是否接线 | 发 prompt injection（"忽略以上指令…"）、诱导泄漏 system prompt、违禁话题 | 期望被 L0/输出闸拦；疑似零拦截（CheckUserInput/Output 不调用，分类器永真 mock） | P0 |
| ENG-SEC-5 | IDOR / SSRF | `learner_data_query` 传他人 user_id；`run_python` input_file = `http://169.254.169.254/...`；`web_fetch` SSRF 回归（非 test 行为） | 期望越权/SSRF 被拒；疑似 learner IDOR + run_python SSRF 可达 | P0/P1 |
| ENG-SEC-6 | 跨用户数据隔离 | 直接打 `ListBySession`/`UpdateSession*`/`agent-runs/search`/`snapshot` 用他人 session_id/run_id | 期望严格按 user_id；疑似 store 层缺 user_id 过滤（防御纵深缺口） | P1 |

### 3.2 计费 / 积分

| ID | 目标 | 方法 | 期望 vs 疑似 | 严重度 |
|----|------|------|--------------|:---:|
| ENG-BILL-1 | agent run 是否扣费 | 跑重任务（多轮+web_search+run_python+压缩），前后查 `credit_cycle/booster/trial` 余额 + `usage_record` | 期望余额下降；疑似纹丝不动(**BLK-2**) | P0 |
| ENG-BILL-2 | budget 4 维熔断 | 构造超 MaxCredits(800)/MaxTurns(100) 的 run；观察是否中断 + wall_time 900s | 期望熔断；疑似仅 wall_time 生效（credits/turns 死代码） | P0 |
| ENG-BILL-3 | 异常下积分泄漏 | run crash/cancel/timeout → 查 orphan reservation | 当前因不扣费无泄漏；**修 BLK-2 后必回归**（detached ctx 不传播 cancel 退款） | P1 |
| ENG-BILL-4 | admin 试聊记账 | 父账户在 Builder 试聊 → 查 admin_test 池 used_amount | 期望增长；疑似 `ReserveAgentTest` 无 caller，月末对公少计 | P1 |
| ENG-BILL-5 | 多实例 daily cap | 模拟多实例并发 → daily 是否被绕过 N 倍 | 期望全局一致；疑似 in-memory 不共享 | P2 |

### 3.3 运行时 / 状态机

| ID | 目标 | 方法 | 严重度 |
|----|------|------|:---:|
| ENG-RT-1 | 纯文本回答 SSE 双发射 | 让 agent 不调工具直接答 → 抓 SSE：token/assistant/step_done 是否重复、seq 是否单调 | P0 |
| ENG-RT-2 | 并发 narration 串流 | 同时起 2+ run → 验证旁白是否串到错误 runID 流（NarrationRunID 池级共享） | P0 |
| ENG-RT-3 | TerminalReason 真实可达性 | 触发各终态；验证 max_turns/aborted_tools 等死代码（MaxStep 超限映射成 model_error） | P1 |
| ENG-RT-4 | cancel 边界 + 多 pod | run 进行中 vs 终态 cancel；多实例下 cancel 是否静默失效（进程内注册表） | P1 |
| ENG-RT-5 | compact 中文长上下文 | 堆中文长对话 → 验证 `len/4` 低估导致不触发→直接 PTL；压缩后恢复正确性 | P1 |
| ENG-RT-6 | yield→answer→resume | 全链路；附件是否丢失；extractor 是否重复 enqueue | P1 |

### 3.4 流式 SSE（前后端契约 — 集中爆雷区）

| ID | 目标 | 严重度 |
|----|------|:---:|
| ENG-STREAM-1 | `ask_user_question` 流式断裂 + 答题后无 resume（**BLK-4**） | P0 |
| ENG-STREAM-2 | 断流无 terminal 收尾 → 气泡永久"生成中"（无超时兜底） | P1 |
| ENG-STREAM-3 | `currentRun=null` 连锁失效（状态徽标/取消/budget 弹窗，**BLK-5**） | P0 |
| ENG-STREAM-4 | 409 双订阅 / 刷新恢复（流式不重连，丢已生成文本） | P1 |

### 3.5 Skill 系统

- v1/v2 skew（v2-only agent + DB 故障 → 空 body 静默运行）【P0】；同名 skill 阻断 Run（暴露内部规则号）【P1】；marketplace 订阅两阶段孤儿补偿非原子【P0】；sanitize LLM 不可用阻断发布（无降级）【P1】；load_skill turn cap 与磁盘 skill 不一致【P2】。

### 3.6 数据 / 迁移 / 运维

- `agent_message_search` FULLTEXT 仅手动建（AutoMigrate 不建）→ 新环境部署 checklist【P2】；`isTerminalStatus` 与实际 status 值不匹配 → admin cancel 已结束 run 污染审计【P0】；缺 `UNIQUE(parent_user_id,name)`【P1】；`sandbox_session.agent_run_id` 允许 NULL（审计断链）【P2】；GORM `default:true` bool Create 坑（多张表）回归。

### 3.7 并发 / 压测

- 并发 N run：narration 串流（ENG-RT-2）、`vision_quota` TOCTOU 超配、budget daily 绕过；SSE 长连接稳定性；`visionQuotaStore` sync.Map 无 GC 内存增长。

---

## §4 视角二：非工程师配置者

> 旅程脚本 + 可用性 + 黑话审计 + 合规硬规则 + 业界对标。

### 4.1 旅程测试场景（CFG-*）

| ID | 场景 | 重点验证 |
|----|------|----------|
| CFG-1 | 从模板创建 | 走通；模板预填的 6 道隐藏题（联网/风格/兜底语）配置者看不见也改不了 |
| CFG-2 | 从零创建 | "12 题问卷"实际只渲染 8 题；**3 个工具开关默认全开（含"高危"）且绕过确认弹窗**（默认 true 不触发 false→true 确认） |
| CFG-3 | 派生(复制) | 术语"派生" vs 业界"复制副本"；副本字段一致性 |
| CFG-4 | 高级模式 | "切换以获得更精细控制"是**空承诺**（prompt textarea `disabled` + "即将上线"）**且不可逆**（无切回入口，丢问卷结构） |
| CFG-5 | Skill 编辑 | 裸 YAML frontmatter + Markdown（非工程师劝退级） |
| CFG-6 | 装载 Skill | Builder 全程不提 Skill；要去另一个顶级菜单建——信息架构断裂 |
| CFG-7 | 版本历史回滚 | 回滚创建新版本不覆盖；diff 清晰度 |
| CFG-8 | marketplace 发布 | 脱敏 diff、二次脱敏一致性、char delta>5% 拦截 |
| CFG-9 | 保存后 | "试聊" toast"即将上线"扑空；"使用数据" tab 永久占位扑空（首次体验连撞两墙） |
| **CFG-CRIT** | **system_prompt 是否真生效** | Builder 写 `system_prompt`，但 v2 高级编辑器/runtime 读 `custom_skill_body/generated_skill_body` → 强烈怀疑 `system_prompt` 是**孤儿字段**（写了存了不进 prompt）。**跨前后端验证：建 agent 写独特行为指令 → 跑对话看是否生效** | P0 |

### 4.2 端到端可用性测试

找一个不懂 prompt engineering 的真实/模拟用户，从"我要做一个 X 助手"走到"建出可用 agent"，记录所有卡点、求助点、放弃点（对标 Coze/Dify 的"3-5 分钟建成"）。

### 4.3 黑话 / 认知负担审计清单

system prompt / "Prompt 代码" / YAML frontmatter / Markdown / token·bytes / `bash_exec`·`code_sandbox` / **"Nano Banana 2"（模型代号怼脸）** / `when_to_use` / **"v2 Runtime"** / "派生" / "软删除"。逐条评估有无中文缓冲与示例。

### 4.4 合规硬规则检查（`ui-ux.md`）

- 表单 **blur 校验缺失**（仅保存时一次性校验，违反规则 3）【P1】；
- **AgentList 空状态无 CTA**（违反规则 2，新用户首屏无引导）【P1】；
- 销毁性操作确认弹窗（合规，已做好）；4 态覆盖（多数合规）。

### 4.5 业界对标

| 维度 | 莫小派现状 | Coze/Dify/扣子/GPTs |
|------|-----------|---------------------|
| system prompt 引导 | 仅 placeholder | 示例库 + AI 一键优化 |
| 模型选择 | 0（隐藏） | 至少档位选择 |
| 联网/风格开关 | 隐藏在不渲染的题里 | 一线可见 |
| Skill 编写 | 裸 YAML+MD | 结构化表单/可视化 |
| 高级模式 | 空壳 | 真·JSON 超集 |

---

## §5 视角三：非工程师使用者

### 5.1 旅程脚本（USR-*）

| ID | 场景 | 重点 |
|----|------|------|
| USR-1 | 进入选 agent | firstRun 欢迎语 + starters；`availableAgents` 拉取失败时页面空白无提示无 retry（AgentCardGrid 模范 4 态组件未接入主流程） |
| USR-2 | 发消息看流式 | 光标/思考块/工具调用/计划卡片 happy path（打磨较好） |
| USR-3 | **ask_user_question** | **BLK-4**：默认路径不渲染提问或显示"任务失败"，答题后卡死 | 
| USR-4 | 最终答案 + 产物 | markdown / HTML iframe 沙箱 / 文件下载；过期处理 |
| USR-5 | 反馈 | 点赞踩；**历史只读态仍可重复提交反馈**（readOnly prop 定义未使用） |
| USR-6 | 历史 / 搜索 | 搜索页 4 态模范；历史列表 error 态裸露原始报错无 retry |
| USR-7 | 附件上传 | **无上传进度/spinner**（20MB 大文件无反馈，对标 ChatGPT 有进度条） |
| **USR-CRIT** | **B2B2C 子账户可用性** | 用真实子账户登录跑父账户 agent → 确认是否 `ErrSkillNotFound`（子账户不可用） | 

### 5.2 4 态覆盖审计（按组件）

逐组件核对 loading/empty/error/success：已知缺口——侧边栏 sessions（fetch 失败静默吞成 `[]`、无 loading）；附件上传无 uploading 态；历史列表 error 无 retry。

### 5.3 异常体验

| 场景 | 疑似现状 |
|------|----------|
| 断流（无 terminal） | 气泡永久"生成中"闪烁，无超时兜底 |
| 断网 mid-stream | 进 catch，保留部分回答，提示文案合理 |
| 刷新页面 | 流式不重连，已生成文本全丢；进行中 run 仅 isNewSession 分支恢复轮询 |
| 积分不足 | 硬阈值 `<50` 拦截，与 `estimate`（可能 200-600）脱节 → 余额 60 发起注定中途耗尽的任务 |
| budget 预警/阻断 | 流式路径 currentRun=null → 60%/100% 弹窗**根本不触发**（BLK-5） |
| 中止 vs 取消 | "中止"仅断前端 SSE（后端继续跑），"取消"才真停；语义混淆 |

### 5.4 黑话审计

"等待 **agent** 继续…" / "已运行 N **步**" / fallback "正在调用工具 **web_fetch**"（原始工具名漏给学员）/ "操作失败"。

### 5.5 业界对标

ChatGPT/Claude/Monica：stream timeout 兜底、澄清提问为一等交互、上传进度、断线重连、消耗透明。

---

## §6 执行计划（Wave 化）+ 上线 Gate

### Wave 0 — 测试环境与夹具（前置）
- dev 部署当前 develop（server + admin + web-v3 + admin-web）；跑齐 agent migrations。
- 建测试 agent（含一个**必触发 `ask_user_question`** 的 fixture，复用 `seed_e2e_test_agent`）。
- 确认子账户凭据 `$E2E_STUDENT_USERNAME/$E2E_STUDENT_PASSWORD`（未配 → 向用户索取）。
- 打通 dev MySQL 只读查询通道 + dev Langfuse 可见性。

### Wave 1 — 红线确认（最高优先，~1-2 天）
跑 ENG-SEC-1/2/3/4、ENG-BILL-1/2、ENG-STREAM-1/3、CFG-CRIT、USR-CRIT。
**产出**：§0.1 的 5 条 BLK + 子账户/system_prompt 候选的确认/证伪 + 证据。**这一步直接决定能否上线。**

### Wave 2 — 工程师深测
状态机 / 并发 / compact / skill v1-v2 / 数据隔离 / 迁移 / SSRF·IDOR 全量。

### Wave 3 — 配置者旅程 QA
Playwright（CFG-1/2/4/7/8）+ gstack `/qa`（视觉/认知负担）+ 黑话审计。

### Wave 4 — 使用者旅程 QA
Playwright（USR-2/3/4/6）+ gstack `/qa`（异常体验）+ 黑话审计。

### Wave 5 — 回归套件固化 + 压测
把红线场景固化为永久 Playwright/Go 回归；并发/SSE 长连接压测。

### 上线 Gate（exit criteria）
- [ ] 所有 P0 **关闭** 或 **显式接受风险并有补偿**。
- [ ] **安全**：permission 决策真实生效 **或** 明确"纯沙箱隔离"路线并加固（default-deny seccomp + 去 `docker.sock` + run_python 命令校验 + bashvalidator 语义拦截）。
- [ ] **计费**：agent run 真实扣费 **或** "v1 不计费"为明确商业决策。
- [ ] **流式**：ask_user_question / budget 预警 / 取消 在主路径可用。
- [ ] **B2B2C**：子账户可正常使用 agent。
- [ ] compliance L0/输出闸接线（或显式接受"靠 prompt 自觉"风险）。

---

## §7 待用户决策（开放项）

1. **测试执行目标环境**：dev（推荐 — 真实后端 + dev DB，禁 prod）。
2. **是否将已确认 P0 同步 spin off 成 NDF hotfix/standard 轨**，还是先只交付测试设计 + 执行测试。
3. **安全模型方向**：恢复 permission 管线 vs 走"纯沙箱隔离"加固（决定 §3.1 测试重点）。
4. **计费**：v1 是否必须扣费上线（决定 BLK-2 是否 blocker）。
5. **子账户可用性**：是否 v1 必需。
6. **自动化深度**：Playwright E2E 回归套件投入 vs gstack `/qa` 一次性 QA。

---

*创建于 2026-06-01，基于 develop HEAD `f73bea85` 的代码勘察 + 第一手验证。本文档是测试设计，非测试执行结果；红线置信度见各条标注，最终以真实环境执行为准。*
