# agent-correctness-fixes — 需求卡片 + 实施计划 (S0→S3 合并)

> Standard track. 起因：agent-mode prod-readiness 测试探测出的一批正确性/稳定性/安全缺陷（内部发现，非 customer 线上 bug）。User 2026-06-05 已逐条确认修复 + 确认「就在本 session 打包批量串行修」。
> 根因均由 6 个 read-only investigator subagent + 1 个 prod 数据分析 subagent 落实到 file:line（见各 task）。

## 背景 / 问题来源

agent mode 上线前大测试发现 9 类问题。其中 BLK-2(计费)、B2B2C 子账户、BLK-1/BLK-3(权限门禁/危险命令)、run_python SSRF、配置器工具开关 已在前序 feature 修复并 merge。本 feature 收口剩余 7 项正确性缺陷。

## Triage

推荐档位：**Standard**（不可降级）。理由：
1. 涉及 biz 层业务逻辑（compliance / 提示词拼装 / 计费估算）—— 高风险
2. 影响文件数 >3（admin_service / tool_* / hooks / adapter / runner / runner_runstream / runner_stream / compliance/gate / compactv2）
3. 安全敏感（越权工具删除、合规检测接线）
4. 多个 task 共用核心流式文件 runner_runstream.go → 必须串行

无 DB schema 变更，无新 API 端点，无新外部服务集成。

## 验收标准（PRD）

| # | 缺陷 | 期望行为 |
|---|------|---------|
| 1 | 内容合规检测形同虚设 | 用户输入的越狱/注入话术、模型输出的违禁内容被真实检测（接通 CheckUserInput/CheckLLMOutput，去掉永真 mock）|
| 2 | learner_data_query 越权(IDOR) | 该工具被整体删除，不再注册给任何 agent |
| 3 | 行为指引(system_prompt)在流式聊天不生效 | 正常聊天(RunStream)与 Run 两条路径都把 system_prompt 发给 LLM |
| 4 | 纯文字回答重复显示 | 纯文字答案在 SSE 只发一次 |
| 5 | 多人并发旁白串台 | 旁白按 run 上下文路由，并发互不污染 |
| 7 | 管理员取消已结束任务污染记录 | 对 terminated run 取消返回 ErrAgentRunNotCancellable，不覆盖 terminal_metadata |
| 8 | 长对话压缩系数对长尾/思考模型低估 | 压缩触发线 0.85→0.70（或改用 contextbudget 分语言估算器），覆盖 p90 1.5× 长尾 |

> #6（断流闲置超时）暂不在本批：需新增 idle-timeout 机制（涉及 stream 读循环改造），User 建议先 90s，作为后续独立 task；#9（多实例）单机无需动。本 feature 不含 #6/#9。

## 任务计划（S3）— 串行执行

每个 task：RED(失败测试)→GREEN(实现)→REFACTOR；完成后并行双 Sonnet reviewer（spec-compliance + code-quality）。内部发现非 customer bug，故 §12 `test(qa):` 前缀非强制，但 TDD 失败测试仍必写并永久留库（Rule 10 持久回归）。

### T1 — #7 管理员取消守卫（隔离，最小）
- **根因**：`internal/numind/biz/agent/admin_service.go` `isTerminalStatus`(149-155) 只认 `"completed"/"failed"/"cancelled"/"error"`，但 agent_run.status 真实只写 `"running"`/`"terminated"`（DB CHECK 约束）。已结束 run(status=terminated) 不匹配守卫 → 漏过 → `SetCancellationRequested`(store/agent_run.go:139-153) 用全列 `Updates` 覆盖 `terminal_metadata` 并盖 `cancellation_requested_at`(晚于 ended_at)。
- **修**：`isTerminalStatus` 改为 `status != "running"` 即终态（或显式加 `"terminated"`）；metadata 写改用 `MergeTerminalMetadata` 不覆盖。修 `admin_service_test.go:138` 用 `"terminated"`（生产真实值，非 `"completed"`）。
- **验收**：取消 terminated run → ErrAgentRunNotCancellable；terminal_metadata 不被覆盖；新增/修正测试 GREEN。

### T2 — #2 删除 learner_data_query 越权工具（隔离）
- **根因**：`internal/numind/biz/agent/tool_learner_data_query.go` `Execute`(56) 直接信 LLM 传入的 `in.UserID` 查档案，不校验调用者身份（ctx 里有 `middleware.UserIDFromCtx` 却没用）→ IDOR。
- **修**：删 `tool_learner_data_query.go` + 其 `_test.go`；移除 `factory_platform.go:112` 的构造；移除 `student_run_lifecycle.go:736` `safeToolBaseline` 中的 `"learner_data_query"`。grep 全仓库 `learner_data_query` 清零残留（含 prompt/doc 引用如有）。
- **验收**：工具不再注册；`go build ./...` + `go test ./...` 绿；grep `learner_data_query` 仅余本计划/历史 manifest。

### T3 — #5 旁白串台（核心文件）
- **根因**：进程级唯一 `*RunHooks`(biz.go:350-354,592) 被所有 run 共享；每个 run 在 `runner_runstream.go:314-317` / `runner.go:831-833` 改写共享字段 `effectiveHooks.NarrationRunID = run.ID`；`adapter_full_to_eino.go:207-208` 用该共享字段路由 → 并发末写覆盖 → A 的旁白进 B 对话。其它 per-run id 都从 ctx 取（sandbox/permission），唯独旁白读共享字段。
- **修**：`adapter_full_to_eino.go` 用 `agent.RunIDFromContext(ctx)`(已经 `WithRunID` 注入) 替代 `a.hooks.NarrationRunID`；删 `hooks.go:65` 的 `NarrationRunID` 字段及两处 mutation。
- **验收**：旁白按 ctx runID 路由；新增并发测试（两 run 交错 emit，断言各自 buffer 不含对方 narration）。

### T4 — #4 纯文字重复（核心文件）
- **根因**：两个 emitter 都对纯文字发 token_delta——`streamScanToolCallChecker`(runner_runstream.go:711-718，eino StreamToolCallChecker，实时 pump) + `consumeEinoStream`(runner_stream.go:253-259)。eino 把 model 输出 copy 两份(react.go:369-378)，纯文字(无 tool)时两份都带完整答案 → 发两遍；controller(student_run_stream.go:190-214) 不按 message_id/seq 去重。consumeEinoStream 对 tool_calls 去重(269-276) 但文字无去重。
- **修**：设计去重——确定 checker 为 canonical 实时 emitter 后，纯文字路径下 consumeEinoStream 不再重发已由 checker 发出的文字（或反之）。需读懂 eino copy 流转，谨慎设计；reviewer 重点把关不漏发/不少发。
- **验收**：纯文字答案只发一次；新增测试断言无重复 token_delta 内容；tool-call 路径回归不变。

### T5 — #3 + #1a 统一提示词拼装（核心文件）
- **根因**：两条路径镜像各缺一半。RunStream(流式，聊天主路径，runner_runstream.go:293-304) 无 `ShouldUseV2Prompt` 分支 → 注入了 L0/L1 硬规则但**丢 system_prompt(行为指引)**。Run 的 V2 分支(runner.go:760-808 + runner_prompt.go:57-64 BuildSystemPromptV2) 注入 system_prompt 但**丢 tenantHardRulesPlaceholder(L0/L1 硬规则)**。
- **修**：统一为两条路径都同时含【平台/机构硬规则】+【system_prompt】。RunStream 加 V2 分支（复用 BuildSystemPromptV2/BuildInstitutionSection）；BuildSystemPromptV2 纳入 tenantHardRules 段。
- **验收**：RunStream 且 system_prompt 非空 → system message 含行为指引文本；Run V2 路径 → system message 含 L0/L1 硬规则；两路径各加测试。

### T6 — #1b 合规检测接线（biz 接线）
- **根因**：`CheckUserInput`(输入注入检测：关键词表+真实 qwen LLM 分类器) 与 `CheckLLMOutput`(输出 fence/品牌检测) **零生产 caller=死代码**；`compliance/gate.go:121-122` 输出 LLM 分类器是永真 mock。`SystemPromptBlock`/`CheckToolCall` 已 live。
- **修**：在 run 入口对用户输入调 `CheckUserInput`、对最终答案调 `CheckLLMOutput`；命中处理=优雅终止 run + 友好提示（区别于工具软拦截）；评估 `gate.go:121` 永真 mock 是否换真分类器或保留（S4 内决，记 ADR）。注意 Langfuse trace 不破坏（rule ai-service）。
- **验收**：注入关键词输入被拦/标记；含违禁 fence/品牌输出被处理；新增测试；trace 完整。
- **风险**：本 task 最开放、最安全敏感——若 S4 发现需产品决策（命中后是硬终止还是仅标记）→ Pause and Ask。

### T7 — #8 压缩系数校准（隔离）
- **数据结论（prod 2453 真实调用分析）**：现 `estimateTokensEino`(adapter_compactv2.go:183-199) 用 `len()` 字节÷4，对中文≈0.75 token/字（轻微高估，非低估，threshold.go:48 注释「低估」是误植）；真凶=长尾(36% 调用实际>估算，p90 1.5×)与思考模型(claude-thinking 1.9×) 系统低估 → 该压没压撑爆。
- **修（首选最小高杠杆）**：`compactv2/autocompact_constants.go:22` `AutocompactThreshold` 0.85→0.70（对应 `HardLimitRatio` 0.95→0.85）。可选更彻底：`estimateTokensEino` 改用 `contextbudget` 分语言估算器(zh=0.60,SafetyMultiplier=1.30，已被 2453 条 prod 验证)。
- **验收**：阈值下调生效；新增测试覆盖触发点；（若换估算器）中文样本估算贴近真实。
- **caveat**：长期最优=按(provider,model)填 token_estimation_profile(结构+calibration_ratio 反馈已现成、0 行未启用)——记为 follow-up，不在本 task。

## Tier 协议
- T1 / T2 / T7 互相隔离（admin_service / tool_*+factory+lifecycle / compactv2）；T3/T4/T5/T6 共用 runner_runstream.go 等核心文件 = Tier 4 串行。
- 本 feature 单 session 驱动，统一**串行**执行（最稳），不做 Tier 3 并行。

## S5 验证策略（Rule 10）
- **方式**：后端 Go 持久回归测试（每 task TDD 失败测试永久留库）为主——本批多为 biz/runtime 逻辑，需自动回归保护（含 T6 安全敏感，按 Rule 10 必须持久测试，非一次性）。
- **补充 dev 冒烟**（gstack /qa，一次性）关键用户路径：
  1. 建带显著「行为指引」的 agent → 聊天 → 行为体现该指引（T5）
  2. 纯文字回答不重复（T4）
  3. 管理端对已结束 run 取消被拒（T1）
  4. learner_data_query 已不在工具列表（T2）
- 涉及 LLM 调用(T6)：确认 Langfuse trace/generation 未被破坏。

## 进度
total_tasks=7。S4 起逐 task 更新 manifest progress。
