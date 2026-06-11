# Agent Mode 上线冲刺 — 统一 Backlog

> 流程 SOT：`docs/superpowers/specs/2026-06-10-agent-mode-launch-process-design.md`（Codes 根目录）
> 创建：2026-06-10（Phase 0）。所有 agent mode 上线相关问题的单一队列。
> 判准：使用流程类条目的严重度以 `design-baseline.md` 为准（R0 后逐步成形），在此之前一律标"待走查裁决"。
>
> 状态值：`open` 未处理 / `verify` 待复核 / `adjudicate` 待走查裁决 / `closed` 已关闭 / `accepted` 用户签字接受风险 / `post-launch` 上线后迭代

---

## §0 R0 形态对齐裁决（2026-06-10）——本节覆盖下文各条初判

> R0 已完成，产出 `design-baseline.md`（唯一产品判准）。关键裁决对 backlog 的影响：

| 裁决 | 对 backlog 的影响 |
|------|------|
| **CAP-1（上线阻断候选）**：现有 web_search/web_fetch 对锚定场景（小红书爆款选题挖掘）可能不够（登录墙+反爬）。**预探针结论（2026-06-10 run #114）：B+ 级**——agent 15+ 轮检索后诚实声明"拿不到站内单篇笔记/精确互动数据/近 30 天实时榜"，但用二手可信源（36氪具名博主案例、千瓜年度报告、官方话题数据）产出了 5 个方向级选题+移植建议，质量真实可用、零编造。**站内实时数据缺口确认存在**。候选路径：① Hybrid（人喂爆款素材→agent 拆解改编，零新能力）② 第三方数据源 API（新红/千瓜，v1.5）③ 浏览器自动化+cookie（合规重，缓）。最终判定留 R1（真方法论灌入后看莫小派要不要实时数据） | 风险降级：不再是"跑不通"，是"数据新鲜度取舍"，R1 实战定形态 |
| R1 重定义 = "实战建出选题 Agent"（能力探针+配置端走查+首个交付物三合一） | 原 R1 UI 走查脚本作废 |
| 试聊按钮空 toast（UF-5 前半）→ **P1 必修**：跳转聊天页跑草稿 agent | UF-5 升级，出"待裁决"区 |
| 方法论正文 custom_skill_body 编辑（UF-9）→ **v1 必修**（创始人核心操作） | UF-9 升级 P1 |
| 高级模式（UF-3）→ **v1 藏入口**（micro 任务），不做真 | UF-3 出"待裁决"区 |
| Marketplace v1 裁（终局=官方精品店，非租户互发） | UF-11、HW-12 → `wontfix-v1`；marketplace E2E/验收项全部不做 |
| 视频创作/剪辑 → v1.5 | 不进本冲刺任何排查 |
| 问卷降级为"建壳"，题目在 R1 实战中边用边裁 | UF-1/UF-2 转 R1 实战现场裁决 |
| D-1（新增设计出入）：agent 入口改"工作区"卡片区（与 SOP/chatbot 并列） | 新增前端改造项，R1 后出方案 |
| WK-1 from-scratch 修复 → 立即合并（用户授权 AI 决定） | 见 §2 状态更新 |
| 使用资格 gate = 会员在期+积分允许（HW-30 新增 `verify`）：复核代码是否按此 gate agent 使用 | 新增硬基建复核项 |

## §1 红线现状表（go-live blockers）

> 来源：prod-readiness-test-plan §0.1（2026-06-01 基线 f73bea85）。复核基线：develop 9b81d381 / web 697ba54（2026-06-10，代码取证 + manifest 交叉）。

| ID | 红线 | 2026-06-10 现状 | 证据 | 状态 |
|----|------|----------------|------|------|
| BLK-1 | 权限管线非测试二进制全局短路 | **已关闭**。`flag.Lookup("test.v")` 后门已删（hotfix remove-permission-backdoor 2026-06-02），config 驱动 `WithEnforce` 默认 true，7 validator 含 L2 TenantAdminRule 真实执行；enforce=false 是显式逃生舱（loud-warn + audit 落库） | `biz/permission/gate.go:69-79,96,125-140`；`biz/biz.go:321-341` | `closed` |
| BLK-2 | agent run 不扣真实积分 | **已关闭（develop/dev）**。agent-mode-billing S6：bill-only 网关模式 + `injectAgentBillingCtx` 接入流式/非流式两条 run 路径，dev 黑盒验证 Reserve→Reconcile 三池真扣、image_gen 计费。残留：内存 BudgetTracker MaxCredits 漏计 streaming final-answer turn（→ §3 HW-3）；prod 未部署（agent 整体未上 prod） | `billing_ctx.go:22-35`；`runner.go:503`；`runner_runstream.go:123`；`context_budget.go:404-409` | `closed` |
| BLK-3 | bashvalidator 不拦危险命令 | **已关闭（bash）**。validator 8→14 个（+6 语义检查：DestructiveRemove/ForkBomb/DownloadExec 等），`rm -rf /` 等有 DENY 测试断言。run_python 源码无静态校验=有意设计（fresh 沙箱隔离），沙箱边界本身待探针（→ §3 HW-5） | `bashvalidator/validator.go:52-72`；`semantic_validators_test.go:28-52` | `closed` |
| BLK-4 | ask_user_question 流式断裂 | **已关闭**。后端 `consumeEinoStream` yield→question_prompt+waiting terminal；前端渲染结构化 options + answer→resume 链路通 | `runner_stream.go:116-160`；`agentChat.ts:973-993`；commit 2286dbc | `closed` |
| BLK-5 | 流式 currentRun 全程 null | **核心已关闭**。stream_start 即 bootstrap currentRun，取消按钮/状态徽标可工作。**残留疑点**：流式中段预算 60% 预警大概率不触发（threshold 字段无中流刷新）→ 需 dev 运行时探针（→ §3 HW-4） | `agentChat.ts:754-777,997-1008`；commit 56c054c | `closed`（残留→HW-4） |
| BLK-6 | B2B2C 子账户无法用 agent | **已关闭**。`agentTenantAccess` helper：父本人 OR 同租户子账户+IsActive，三个校验点统一，dev live smoke PASS（child 跑父 agent 成功、跨租户 404） | `tenant_access.go:49-76`；b2b2c-student-agent-access S6 | `closed` |

**结论**：旧 5+1 条红线全部在代码层关闭。新红线 = §3 中标 P0 的开放项 + 走查可能新增。

---

## §2 走查挡路候选（R1 前处置）

| ID | 条目 | 说明 | 处置 |
|----|------|------|------|
| WK-1 | **agent-from-scratch-q6q7** | from-scratch 创建表单不渲染 q6/q7 → 422 死路。R0 确认路径保留，用户授权 AI 决定 → **2026-06-10 已 ndf-done 合并**（web-v3 develop 5419d0f），随 dev 重部署生效 | `closed` |
| WK-2 | dev 容器落后 develop HEAD | dev = develop-6706e3be（06-09 15:44），HEAD = 9b81d381（含 ReAct transcript 持久化等 06-09 后 commit） | R1 前 `/deploy-dev` 重新部署两仓库 |
| WK-3 | 特定 PNG 的 VLM 描述失败 | DashScope `image format is illegal` 400（attachment #3/#15，retry 4 次后带错 resolve）。不卡 run，但"传图让 agent 看"场景可能拿不到描述 | `verify`：R2 走查若复现则升级 |

> dev 环境探测（2026-06-10）：服务健康、task profiles 14 行齐全（06-04 已补）、最近 6 天 run 全 completed、附件 fallback 无积压（"新上传不触发"未复现）、sandbox warm pool 正常。**无阻断走查的环境问题。**

---

## §3 硬基建开放项（设计无关，Phase 2 排查/修复）

> 这些与产品使用模式无关，不等走查。初步分级，复核后修正。

| ID | 条目 | 来源 | 初判 | 状态 |
|----|------|------|------|------|
| HW-1 | compliance `CheckLLMOutput` 输出检查未接线（输入检查已接 2026-06-05） | correctness-fixes follow-up | P1 | `open` |
| HW-2 | sandbox 网络无出口限制（Docker bridge 全通）+ seccomp default-ALLOW（弱于 Docker 默认）+ dev 挂 docker.sock | runbook §5 / test-plan C6 | P1（试点小流量+可信租户；开放注册前 P0） | `open` |
| HW-3 | BudgetTracker MaxCredits 漏计 streaming final-answer turn（真实三池扣减不受影响，内存预算维度低估） | agent-mode-billing follow-up | P2 | `open` |
| HW-4 | 流式中段预算 60% 预警不触发（threshold 无中流刷新）【BLK-5 残留】 | 红线复核 2026-06-10 | P1 | `verify`（dev 探针：跑超 60% 预算的流式 run） |
| HW-5 | run_python/bash 沙箱边界实测（网络出口、资源限制、逃逸面）——"沙箱兜底"是 BLK-3 关闭的前提假设 | test-plan / 红线复核 | P1（探针验证） | `verify` |
| HW-6 | 父账户 Builder 试聊不记账：`ReserveAgentTest` 疑无 caller → 月末对公少计 | test-plan D4 | P1 | `verify` |
| HW-7 | orphan reservation 积分泄漏：run crash/cancel/timeout 下 detached ctx 不传播退款（BLK-2 修复后此项被武装，必须回归） | test-plan D3 | P1 | `verify` |
| HW-8 | yield（waiting_for_user_choice）期间 budget 仍累计（Pause/Resume wire 未完成） | runbook §10 / I4 | P1 | `verify` |
| HW-9 | skill v2-only agent 且 DB 故障 → 空 body 静默运行 | test-plan C5 | P1 | `verify` |
| HW-10 | store 层跨用户隔离纵深：ListBySession/UpdateSession*/search/snapshot 疑缺 user_id 过滤 | test-plan D2 | P1 | `verify` |
| HW-11 | run_python input_file SSRF（169.254.169.254 可达?） | test-plan D1 | P1 | `verify` |
| HW-12 | marketplace 订阅两阶段（脱敏→克隆）孤儿补偿非原子 | test-plan D12 | 待 R0（marketplace 可能砍出 v1） | `adjudicate` |
| HW-13 | agent_definition 缺 UNIQUE(parent_user_id, name)；sandbox_session.agent_run_id 允许 NULL 审计断链 | test-plan D16/D17 | P2 | `open` |
| HW-14 | vision_quota TOCTOU 并发超配 + sync.Map 无 GC | test-plan D19 | P2 | `open` |
| HW-15 | 多实例假设：流式锁/cancel 注册表/daily cap 全进程内（无 Redis）。当前单机部署=非问题；扩容前必修 | test-plan C2/D5 / correctness #9 | P2（单机 accepted） | `accepted`（用户 2026-06-05 拍板暂不动） |
| HW-16 | image_gen 收编 aiservice：计费已接（billing S6），tracing/路由降级仍绕过 | dev-fixes follow-up | P2 | `open` |
| HW-17 | presigned URL 24h 过期 → 持久化 markdown 里的图隔日裂图（应存 object key 读时再签） | image-durable-render follow-up | P1（试点用户隔日必现） | `open` |
| HW-18 | 断流闲置超时缺失（建议 90s）→ 断流时气泡可能永久"生成中" | correctness #6 / test-plan D9 | P1 | `open` |
| HW-19 | 刷新后流式不重连、已生成文本丢失；409 双订阅 | test-plan D10 | P1 | `verify` |
| HW-20 | 压缩 token 估算分语言估算器（zh=0.60）+ per-model profile（系数 0.85→0.70 已做，结构现成 0 行启用） | correctness follow-up | P2 | `open` |
| HW-21 | `agent_message_search` ngram FULLTEXT AutoMigrate 不建——prod migration checklist 已含 #(见 go-live-checklist)，确认覆盖 | test-plan C8 | P2（checklist 项） | `verify` |
| HW-22 | TerminalReason 5 个死代码值 + MaxStep 超限映射成 model_error（用户看到错误归因不对） | test-plan B3/D7 | P2 | `open` |
| HW-23 | AuditLogger 队满丢审计（buffer=1000，drop 计数 WARN 监控） | runbook I6 | P2（v1 设计现状，监控告警即可） | `accepted` 候选 |
| HW-24 | L1 记忆 expires_at 无自动清理 cron | runbook I3 | P2 | `post-launch` 候选 |
| HW-25 | 安全分类器（AgentInjectionCheck）计费给 parentUserID，应加免计费 ctx 标志 | correctness follow-up | P2 | `open` |
| HW-26 | 附件 fallback worker "新上传不触发"——**2026-06-10 探测未复现**（06-08 7 个新上传全部正常）。降级观察 | dev-fixes follow-up | P2 | `verify`（R2 复测） |
| HW-27 | multimodal Layer4 regex 错误检测脆弱 / capability matrix 错漏需人工 / 1500ms fallback 轮询窗口 | multimodal-fallback.md | P2 | `open` |
| HW-28 | GORM default:true bool Create 坑波及 agent 表回归确认 | test-plan D18 | P2 | `verify` |
| HW-29 | prod 部署前：dev/prod ai_service model_key 漂移（dev 实为 qwen3-vl-flash 无日期后缀等），multimodal capability migration UPDATE 几乎全未命中靠手工兜底。**prod 上线时必须先核对该环境 model_key 再跑等价 SQL** | v15-multimodal S6-Caveat-D1 | P0（Phase 4 gate 项） | `open` |

---

## §4 待走查裁决（使用流程类 — R0-R4 期间逐条定生死）

> 旧 test-plan 基于"代码里的使用模式"产出，用户已警告该模式可能整体不对。以下条目**不直接修**，走查时对照 design-baseline 裁决：是 bug / 是设计偏差要改 / 设计本来如此 / 功能砍出 v1 不修。

**配置者端**（R1 议程素材）：
- UF-1 模板预填 6 道隐藏题不可见不可改（E1）
- UF-2 "12 题问卷"实际渲染 8 题（E2，工具开关已删）
- UF-3 高级模式空承诺：prompt 编辑禁用+"即将上线"，切换不可逆丢问卷结构（E3 / CFG-4）
- UF-4 Builder 与 Skill 信息架构断裂：建 agent 全程不提 skill，skill 在另一顶级菜单（E4）
- UF-5 保存后"试聊"toast 扑空 +"使用数据"tab 永久占位（E5）
- UF-6 表单 blur 校验缺失（E7，违反 ui-ux 规则 3）
- UF-7 AgentList 空状态无 CTA（E8，违反规则 2）
- UF-8 配置端黑话怼脸：YAML/token/bash_exec/模型代号/软删除（E9）
- UF-9 custom_skill_body 高级编辑 v1 不支持（skill-system follow-up）
- UF-10 同名 skill 阻断 Run 且暴露内部规则号（D11）
- UF-11 sanitize LLM 不可用阻断 marketplace 发布无降级（D13）——连带 R0 marketplace in/out 裁决

**使用者端**（R2 议程素材）：
- UF-20 availableAgents 拉取失败页面空白无提示无 retry（F1）
- UF-21 历史只读态仍可重复提交反馈（F2）
- UF-22 历史列表 error 态裸报错无 retry（F3）
- UF-23 附件上传无进度反馈（F4）
- UF-24 侧边栏 sessions fetch 失败静默吞（F5）
- UF-25 积分不足拦截硬阈值 <50 与 estimate 脱节 → 余额 60 可发起注定耗尽的任务（F6，偏计费边界）
- UF-26 "中止"仅断前端 SSE 后端继续烧积分，与"取消"语义混淆（F7）
- UF-27 学员端黑话泄漏：web_fetch/"agent"/"步"（F8）
- UF-28 yield→answer→resume 附件可能丢失/重复 enqueue（D8）
- UF-29 ask_user_question 30 分钟无应答 stuck run 只能人工 cancel（I5）
- UF-30 file_read 不支持 xlsx/zip、PDF>60 页报错、上传 docx 已修（I8 部分过时，待复核现状）
- UF-31 "generated" modality 附件无 fallback 文本——agent 引用上轮生成文件时上下文缺失（J 类）

**Admin 端**（R3 议程素材）：
- UF-40 Layer-0 全局合规规则无 admin UI，需直改 DB（I7）
- UF-41 DB 直写强制取消不触发 credit reconcile（I1，运营注意项）

---

## §5 Follow-up 散件（当前不阻塞，多数 post-launch）

百炼 fileID 大文档二级路径；run_python mime fallback 优化；文件生成阶段心跳 narration；doc-gen 防错三件（SKILL.md 伪代码清理/render-back/xlsx recalc）；平台 SKILL.md prose read_skill→load_skill rename（micro，已 spawn 未落地）；ReserveAgentTest TOCTOU 事务化；image_gen pricing 待运营定价；DMXAPI cache 折扣 dev 实测（需先跑 3 个 migration）；Subscribe(dead-runID) 显式检查；stream-emit-toolcall known_issues #2 回写更新；沙箱镜像精装（pandas/ffmpeg）；容器逃逸渗透测试（开放注册前）；e2e mock spec 与组件漂移修复（wave2 H4）；test fixture schema drift 根治；prod ml-base Dockerfile 回植。

---

## §6 平台级发现（agent 之外，单独提请用户注意）

| ID | 条目 | 证据 | 初判 |
|----|------|------|------|
| **PLT-1** | **WebLogin 密码明文比对**：`user.Password != req.Password`（同文件另一处 Login 用 bcrypt auth.Compare）→ 用户密码大概率明文存储于 DB，影响现网 prod，与 agent 无关 | `biz/user/user.go:339-342`（2026-06-10 亲核）；wave2 §4.1 首报 | **P0（平台），建议独立 hotfix；修复需带存量数据迁移方案** |
| PLT-2 | dev admin 测试账号登录失败（凭据被改/锁）→ 阻断连 dev 库的 E2E | credit-log-task-names S5/S6 | P2（R3 走查 admin 端前需解决） |
| PLT-3 | 阿里 DashScope free tier 耗尽 403 → 部分子任务已切 DMXAPI | manifest 多处 | 观察 |

---

## §7 Manifest 卫生债（不影响上线，找空档清）

- agent-mode-p0-tools 条目重复 `stage:` key（completed 与 S3 并存）
- 20+ 条 H1/H3/S6 实际已 merge+部署但 stage 未关闭；completed>7 天未归档
- 两大僵尸条目：agent-react-streaming（S3, 0/16，实际早已 land）、agent-mode-e2e-rollout（S4, 3/38，被后续 feature 实质超越，剩余 scope 需盘点重立项或关闭）
- agent-mode-configurator-relocate（S0）与现实脱节（搬迁实际已发生）
- persistent-sandbox-session：S2 设计完成，**等用户 go/no-go**（→ R0 议题 8）

---

## §8 冲刺期间新发现

> R1 续测（2026-06-11，multi-question 已部署 dev）新增：

| ID | 条目 | 来源 | 级别 | 状态 |
|----|------|------|------|------|
| ENV-1 | **dev dmxapi 供应商大面积超时/503**：deepseek context deadline exceeded + qwen-turbo-latest"无可用渠道"，12 分钟 106 次。打断 agent 长任务（run 130 写报告中途崩）。**已缓解**：dev agent 主模型 deepseek 路由切 aihubmix(健康 200)优先、dmxapi 降 fallback；qwen-turbo dmxapi 降走 ali（ai_service_route 41→p10/40→p5/58→p1） | run 130 取证 | 环境（非代码 bug） | `mitigated`（dev 切渠道；prod 用不同渠道） |
| HW-37 | **run 异常终止状态机不收尾**：LLM 超时打断 resume run 时 state_reason 停在"running"（status=terminated），前端显示成"卡住"而非"失败请重试"。run 130：ended_at=11:37(第一段 yield) 但 resume 第二段跑到 11:41 未回写状态 | run 130（state_reason=running 异常） | P1（健壮性，上线前修） | `open`（待查：multi-question yield 收尾 or resume 中断收尾） |
| RISK-1 | **agent 长任务对 LLM 稳定性高度敏感**：几十轮工具+LLM，供应商一抖整任务崩（ENV-1 实证）。上线前 prod 需：①最稳渠道 ②超时重试/断点容错（一超时不整崩） | ENV-1 暴露 | P1（上线 gate 考虑） | `open` |



| ID | 条目 | 来源 | 级别 | 状态 |
|----|------|------|------|------|
| BUG-1 | **内存预算护栏把 token 当积分**：单次 ~6k token 调用击穿 800 积分 cap → 所有实质性 agent run 首次工具调用即被杀（terminal used=6829 vs 真实扣费 ~5）。真实三池扣费不受影响 | CAP-1 预探针 run #113（2026-06-10） | P0（R1 挡路） | `closed`——hotfix budget-tracker-token-units（develop c2f93c59，TDD+双 review，5 P2 全修） |
| HW-31 | vision 工具（tool_annotate_image 等经包级 chatFn 直调 aiservice.Chat）usage 不进 usageStore → vision token 对内存护栏透明（真实计费不受影响） | BUG-1 review 发现（pre-existing） | P2 | `open` |
| BUG-2 | **UserSessionRule 拦死沙箱执行工具**：open-tools(06-01)×permission-backdoor-removal(06-02) 两 feature 对撞，run_python/bash_exec 被"可能修改你的数据"全拒 → 文档生成管线（docx/xlsx/pptx）全灭 | 预探针 run #115 | P0（上线阻断） | `closed`——hotfix permission-allow-sandboxed-exec（TDD+双 review 从严+沙箱兜底实证） |
| HW-32 | 工具注册 registry 无重名 duplicate-check：未来 MCP/CLI factory 实装后，重名工具可静默覆盖平台工具并继承沙箱豁免（当前单 factory 不可利用） | BUG-2 review 发现 | P2（MCP 实装前必修） | `open` |
| BUG-3 | **流式 yield 杀 run + 空転录**：多步 ReAct 中 ask_user_question 的 yield 从 einoAgent.Stream() 错误冒出，streamErr 分支无检查 → model_error"服务暂时不可用"+messages 空（刷新会话消失）。BLK-4 旧修复未盖此路径——**"已关闭"红线被 R1 实战证伪** | R1 现场用户上报（run #117） | P0（上线阻断） | `closed`——hotfix stream-yield-errpath（行为级 TDD+双 review；dev run #118 实弹验证 waiting+提问按方法论执行）；通用流错误现走 finalizeRun 持久化用户 turn |
| OBS-3 | GET run 详情 API 对 waiting 态返回 status="running"（DB 实为 terminated/waiting）——前端映射约定待 R2 核对是否引起 UI 歧义 | run #118 | P2 | `verify` |
| BUG-4 | **waiting 会话刷新一片空白**：yield 不写转录+快照不带 pending_question → 刷新后无任何记录、无法继续。所有会提问的 agent 必触发 | R1 现场用户上报（run #117/#118） | P0（上线阻断） | `closed`——hotfix yield-session-reload（跨仓库 TDD+双 review；快照合成 question_prompt + 前端恢复 currentRun） |
| BUG-5 | **resume 后 agent 完全失忆**：yield 不持久化转录+answer resume loadSessionHistory 排除当前 run→agent 不记得提问前的调研，又问已知信息；"继续"开新 run memory_read 查不到。对"边查边问"方法论 agent 致命 | R1 现场用户上报（run #119/#120） | P0（上线阻断） | `closed`——hotfix yield-resume-context（TDD+双 review 从严，含多重 yield P1 修复）；HW-33 一并解决 |
| ~~HW-33~~ | （并入 BUG-5）yield 持久化原始输入+resume 保留上下文 | — | — | `closed`（=BUG-5） |
| HW-34 | yield 转录完整性 follow-up：errpath stepCollector 空（仅 tool groups 无 assistant 推理文本）；finalizeRun resume 完成覆写 waiting 转录（历史回看缺 waiting 前部分，不影响 live 上下文） | BUG-5 review 发现 | P2（历史完整性，非阻塞） | `open` |
| D-9 | **ask_user_question 交互重设计**：选项强制→开放信息塞成元选择用户选了没用；无自由填写框。修：选项=具体候选答案+底部永远自由填写框+agent 行为引导（prompt） | R1 现场用户反馈（run 121） | D 类设计（用户拍板） | `closed`——feature ask-question-freetext（跨仓库+双 review，P0 free-text-only 断路已修） |
| BUG-6 | **ask_user_question >4 选项崩 run**：ask-question-freetext 教 agent"选项非穷举"→agent 给 10 选项→Execute 硬 2-4 校验崩→"服务不可用"。我刚部署 feature 的回归 | dev run #127 用户上报 | P0（回归） | `closed`——hotfix ask-question-options-tolerant（容错截断+0 选项开放，TDD+双 review） |
| HW-36 | QuestionPrompt.vue 两处 dead CSS（ask-question-freetext 引入 display:block/margin-top:8px 被原值覆盖，视觉正常仅冗余） | BUG-6 review 发现 | P3（micro 清理） | `open` |
| HW-35 | prompt 引导 agent"需要信息用工具问不要文本问完就停"是 soft 约束，LLM 仍可能输出纯文本提问→run completed（run 121 现象）。需 dev 验证；不行则 runner 层辅助（末步纯文本含提问意图时拦截 completed/强制 function-calling） | ask-question-freetext review 发现 | P1（agent 行为可靠性，待 dev 验证定级） | `verify` |
| OBS-1 | dev 上 ali-dashscope qwen-turbo 辅助调用 403 三连退款（free tier 耗尽，已知 PLT-3），fail-open 不致命但拖慢每轮+日志噪音（run #114：67 笔 reservation 中 40 笔是 403 退款）；建议 dev 把相关 task_profile 路由迁离 ali | run #113/#114 | P2（dev only，R1 前顺手修可提速） | `open` |
| OBS-2 | run 详情 API `credits_used` 恒 0（真实 reconcile 正常，聚合显示未接线）；create 响应 `estimated_credits_min/max` 也是 0-0 | run #114 | P2 | `open` |

## 变更日志

- 2026-06-10 Phase 0 创建：合流 prod-readiness-test-plan / wave1 / wave2 / runbook / multimodal-fallback / manifest follow-ups；红线复核 6/6 关闭（2 个残留子项→HW-4/HW-5）；dev 探测无阻断。
- 2026-06-10 R0 完成（§0 裁决补丁）；WK-1 合并；CAP-1 预探针发现并修复 BUG-1（同日 hotfix 上 dev）。
- 2026-06-10 R1 进行中：预探针抓 BUG-2（权限拦沙箱工具）、现场实战抓 BUG-3（流式 yield 杀 run）——均当日 TDD+双 review+部署+实弹复验闭环。R1 配置端走查收 D-2~D-8 八条裁决（见 design-baseline 更新）。
