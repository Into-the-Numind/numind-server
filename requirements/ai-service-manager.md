# AI 服务统一化管理（AI Service Manager）

## 来源
- 提出人：项目负责人
- 提出日期：2026-04-15
- 前置调研：2026-04-15 session 对现有 AI 调用（LLM + OCR + ASR）的审计
- 被吸收功能：`llm-model-switch`（stage=superseded，原产出继续演进不回滚）
- S0 review：由 code-reviewer subagent 完成，主要修订已落入本稿（§备注列出采纳/不采纳清单）

---

## 需求描述

现状：莫小派的 AI 服务调用散落在多个独立封装层，合规度参差不齐：

- ✅ `biz/ali/` 合规度最高（httpclient + Langfuse + token usage + retry 全链路）
- ❌ `biz/volc/` 的 `StreamChat` / `VisionAnalyze` / `StreamChatWithModel` 完全无 Langfuse，部分绕过 httpclient 直接裸 http
- ❌ `biz/baidu/ocr.go` 独立体系（无 Langfuse、无 billing、无 retry）
- ❌ `biz/monitor/transcriber.go` 的 FunASR 语音识别独立调用
- ❌ 全局无 error 路径的 generation error 记录（违反 `.claude/rules/ai-service.md §3` 硬规则）
- ⚠ 模型—计费—配置三处数据源，`seed_pricing_rules.sql` 与代码实际使用模型名不完全同步
- ⚠ 新增模型/服务需改代码 + 发版，无法运营自助管理
- ⚠ 已有 `llm-model-switch` 功能到 S6（prod）支持用户选 LLM 模型，部分产出（DMXAPIClient 提取到 `internal/pkg/llm/`、用户偏好存储）可作为本功能基础，但 schema/API 未经历二次演进

需求：**扩展现有 `internal/pkg/llm/` 为统一 AI Service Gateway**（本期含命名调整，包路径最终名由 S2 决定），所有 AI 服务调用（LLM / OCR / ASR / Embedding / Rerank / Vision）走同一接口、同一中间件链；并建立 **Service Registry（服务登记表）+ Task Profile（任务能力契约）** 机制，使：

1. **任务差异化调用**：每个业务场景声明需要的能力（文本/多模态/工具调用/OCR/ASR/长上下文等），Gateway 根据 Task Profile 绑定的服务执行
2. **管理端统一运营**：新增/下架服务、调整 pricing、绑定任务—服务关系，全部管理端完成，零发版（前提：服务所属 provider 已被 Gateway 支持；若新 provider 家族仍需发版扩 adapter）
3. **全链路可观测**：100% AI 服务调用都有 Langfuse trace（LLM 用 generation，OCR/ASR 用 span）+ token/用量记录 + error 记录
4. **全链路可计费**：100% 调用进入 `UsageRecord`，按 `task_profile` 维度可聚合
5. **统一容灾**：主服务故障自动 fallback 到备用服务（简单规则，不做 cost-aware）
6. **能力合法性校验（Capability Matching）**：管理员绑定任务—服务时，校验被绑服务的能力满足任务 requirements，不兼容拒绝保存

---

## 业务目标

| 目标 | 衡量 |
|---|---|
| 彻底消除"黑盒 AI 调用" | Langfuse 覆盖验证通过（详见§成功指标的可执行检查） |
| 降低新增/替换服务的运营成本 | 同一 provider 家族内新增服务从"改代码+发版"降为管理端操作 |
| 建立按任务维度的成本分析能力 | Langfuse 按 `task_profile` tag 过滤，聚合单位任务成本 |
| 为 Phase 2 的语义缓存 / cost-aware 路由铺路 | Gateway 抽象就绪，中间件可直接插入 |
| 提升可靠性 | 主服务超时/限流自动降级，单点故障不传导到业务 |
| 防止错误绑定 | Capability Matching 阻止"把纯文本模型绑给多模态任务"这类错误 |

---

## 优先级

**高** — 影响所有 AI 功能的可观测性和可运营性，是后续所有 AI 迭代的基础设施。

---

## Triage
- **推荐轨道：Standard**
- 分类理由：
  1. 数据库 schema 变更：**是**（Service Registry + Task Profile + 关联表 + 既有模型/pricing 表演进）
  2. 新增 API 端点：**是**（管理端 CRUD ~10+ 端点）
  3. 新外部服务集成：**否**（复用现有 Langfuse、DMXAPI、阿里云、火山、百度、百炼、FunASR）
  4. 影响文件数：**>>3**（跨 ali/volc/baidu/salesrag/sop/monitor/chatbot 7+ 模块，估计 20+ 文件）
  5. 高风险业务逻辑（支付/权限）：**是**（迁移期间所有 AI 功能有中断风险，且涉及 billing 全链路）
- 人类决定：**确认 Standard**

---

## 范围

### ✅ 纳入（Phase 1 MVP）

**后端 Gateway 层**
- 统一 AI 服务调用接口（Chat / Embedding / Rerank / Vision / OCR / ASR 等能力类型；精确接口签名属 S2 设计）
- 覆盖所有现有 provider：阿里云 / 火山 / DMXAPI / 百度（OCR）/ 百炼 / FunASR（ASR）
- 中间件链：包含 tracing（Langfuse）、billing、retry、fallback 四类中间件；具体顺序与实现契约属 S2 设计
- 所有 error 路径按 `ai-service.md §3` 记录失败追踪

**Service Registry（DB）**
- 记录每个服务的基础信息（服务类型、provider、family、display_name）
- 记录能力字段（约 8 项核心维度，例如输入/输出模态、上下文窗口、是否支持工具调用/JSON 输出/流式/视觉等；最终字段清单属 S2 设计）
- 记录运营字段（pricing、latency_tier、quality_tier、status、tags）
- 吸收 `llm-model-switch` 既有模型表做 schema 演进
- 合并 `seed_pricing_rules.sql` 的 pricing 数据（处理单位一致性）

**Task Profile（DB）**
- 每个业务场景登记一条，声明所需能力（requirements）+ 默认服务 + fallback 服务 + 允许服务集合
- **初版覆盖场景示例（非完整清单）**：`sop.step`、`sop.vision`、`salesrag.chat`、`salesrag.embed`、`salesrag.rerank`、`monitor.summary`、`monitor.briefing`、`monitor.analyze`、`monitor.transcribe`（ASR）、`chatbot.stream`、`card.generate`、`ocr.baidu`、`file.parse`（qwen-long 文件上传）
- ⚠ **S2 spec 前置 gate**：implementer 必须对 `numind-server/internal/numind/biz/` 做穷举扫描，输出完整 Task Profile 清单并在 spec 中列出；本 S0 列表仅为范围示例，非完整清单

**Capability Matching**
- 管理端保存"任务-服务"绑定时，校验服务能力是否满足任务 requirements
- 不兼容时拒绝保存并返回具体原因（如"该服务不支持图片输入"）

**管理端 CRUD（numind-admin-web）**
- 服务管理页：列表 + 新增 + 编辑能力/pricing + 上下架
- 任务管理页：列表 + 编辑 requirements + 绑定 default/fallback/allowed services
- 软删除 + 核心操作二次确认（避免误删线上正在用的服务）

**迁移策略（新老并存 + 单模块灰度）**
- 新 Gateway 与老封装层并存运行期间，按业务模块逐个迁移
- 每个模块迁移后必须独立验证：lint / 关键流程手跑 / Langfuse 确认 trace 落地 / billing 写入
- 全部迁移完成、灰度稳定后，才删除老封装层

### ❌ 不纳入（明确排除）

- **前端 C 端用户模型选择 UI 改版**：保持 `llm-model-switch` 现有 UI，数据源从新 Registry 读（需保证接口向后兼容）
- **语义缓存**（Phase 2）
- **Cost-aware 自动路由**（Phase 2）
- **Guardrails / PII 过滤**（Phase 2+）
- **模型质量自动评测 / A-B 对比**（Phase 2+）
- **新 Provider 接入**（本期只收编现有的，不新增）

---

## 成功指标（可执行的验证方式）

| 指标 | 验证方式 |
|---|---|
| AI 调用零裸 http | 静态扫描：`grep -rn "http.Post\|http.NewRequest" internal/numind/biz internal/service \| grep -v _test.go` 返回 0 条 AI 相关裸调用（Gateway 自身路径 `internal/pkg/llm/` 及 S2 决定的新路径排除在扫描范围外） |
| 业务代码不直接依赖 provider 包 | 静态扫描：`internal/numind/biz/` 下不允许 `import` `biz/ali` / `biz/volc` / `biz/baidu` / `biz/salesrag/adapter/dmxapi_client` 等 provider 包（Gateway 自身内部除外）；由 lint 规则或 PR checklist 保证 |
| Langfuse 调用覆盖 | S5 跑完关键用户路径（SOP / SalesRAG / 卡片 / 监控 / ChatBot），Langfuse 控制台按时间窗过滤，`count(observations) ≥ count(UsageRecord)` |
| 新增同 provider 家族服务工时 | 管理端操作 ≤ 5 分钟（**前提**：provider adapter 已支持该服务家族；若新 provider 家族仍需发版扩 adapter） |
| 管理端 capability matching 有效 | 手测：尝试把纯文本服务绑给 `sop.vision` → 保存被拒绝 + 返回具体原因 |
| 回归质量 | 所有现有 AI 功能在 dev 环境验证通过（SOP 运行、SalesRAG 问答、卡片生成、监控简报、ChatBot、OCR、ASR） |
| 测试覆盖 | Gateway 中间件链：每类中间件（tracing/billing/retry/fallback）至少 1 个 biz 层 unit test；每个 provider adapter 至少 1 个 roundtrip test（mock httpclient）；迁移前后同一业务的 usage 记账行为需有对比测试 |

---

## 主要风险

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| 迁移期间 AI 功能中断 | 中 | 高 | 新老并存 + 单模块灰度迁移，每模块独立验证后才迁下一个；保留回滚路径 |
| 管理端误操作（删错服务、改错 pricing） | 中 | 高 | 软删除 + 变更审计日志 + 关键操作二次确认 |
| Task Profile 抽象不足以覆盖所有场景 | 低 | 中 | S2 前穷举所有调用点再定字段；留 `extra_metadata` JSON 字段做逃生舱 |
| Capability Matching 过严导致合法服务被拒 | 低 | 中 | 管理端显示"因什么能力不满足而被拒"；支持超管 override |
| `llm-model-switch` 现有 schema/API breaking change | 中 | 中 | S1 proposal **前置任务**：对 llm-model-switch 做 schema + API inventory，逐字段/逐接口决策去留；保留旧接口 shape 向后兼容 |
| Fallback 级联失控 | 低 | 中 | 明确"最多 1 次 fallback 跳转，即最多 2 次 upstream 调用"；fallback 目标必须在 `allowed_services` 白名单内 |
| **Langfuse 挂掉阻塞主请求** | 低 | 高 | **硬约束**：Langfuse SDK 调用必须超时 + 异步 flush，不得阻塞主请求路径；Langfuse 不可用时主流程继续，仅降级为无追踪 |
| **流式调用 token usage 采集时机** | 中 | 中 | Stream 模式下 token 数据仅在 `usage` 字段或最后 chunk 返回；客户端中途断开可能丢数据；S2 需定策略（中断时估算补记/后台异步对账） |
| **Pricing 变更污染历史成本数据** | 中 | 中 | **硬约束**：`UsageRecord` 必须快照**当时 pricing** 到行级字段（冗余存 input/output 单价），不走实时关联计算 |
| **双层 retry 叠加触发 provider 限流** | 中 | 中 | 迁移时关闭 adapter 层 retry，仅保留 Gateway Retry 中间件（单一 retry 源） |
| 多租户可见性未定 | 低 | 低-中 | Registry 默认全站共享可管理；超管/普通管理员权限粒度在 S2 明确 |
| Pricing 单位不一致（元/分/USD、per 1K / per 1M） | 中 | 中 | S2 统一单位（LLM：元 / 每 1M tokens；OCR：元 / 每次调用；ASR：元 / 每秒）；迁移 SQL 做转换并 diff 检查 |
| OCR/ASR 与 LLM 的抽象差异（无 prompt/token 语义） | 中 | 中 | S2 在 Registry 用 `service_type` 字段区分，不同类型对应不同 capability 字段子集；业务层调用入口形态（单入口 `ai.Call(taskID, req)` 还是分 `CallChat/CallOCR/CallASR` 多 method）在 S1 前置任务中决策 |
| **迁移期双路径双记账**（新老并存时同一请求可能被重复写 UsageRecord） | 中 | 高 | 迁移期 billing 必须**只在新 Gateway 中间件**写 UsageRecord；老封装层迁移前关闭其 billing 写入（或加幂等 key 防重）；S2 必须明确交接方案 |
| Gateway 自身故障（进程级）无告警 | 低 | 高 | S6 部署时接健康检查 `/healthz` + 关键 metrics（调用次数、错误率、p95 延迟），挂接现有监控 |

---

## 预估工时（粗估，S3 再精细化）

基于 reviewer 拆分核算：

| 阶段 | 工时 |
|---|---|
| S0 需求卡（本文档） | 已完成 |
| S1 proposal + PRD（含 llm-model-switch schema/API inventory） | 0.5–1 天 |
| S2 详细 spec（接口、schema、中间件链、迁移策略、pricing 单位统一） | 1–1.5 天 |
| S3 task plan | 0.5 天 |
| S4 编码 | 5–7 天（Gateway + 5 个 provider adapter + Registry + Task Profile + 管理端 CRUD + 10+ 调用点迁移） |
| S5 验收 | 1–1.5 天（跑完四大业务关键路径 + Langfuse 核对） |
| S6 dev 部署 + 验证 | 0.5 天 |
| S7 收尾 | 0.5 天 |
| Buffer（stream token 采集、volc vision 格式差异等意外） | 1 天 |

**总计：10–14 天（单人全职）**

若 volc 流式 token 采集或 llm-model-switch schema 合并出现意外，再 +2–3 天。**诚实高估 > 乐观低估后中途砍范围**。

---

## 备注

### 治理承诺
本功能完成后：
- 任何新 AI 功能必须通过 Task Profile + Gateway 接入
- **禁止**再出现绕过 Gateway 的 AI 服务调用
- `.claude/rules/ai-service.md` 需同步更新：从"所有 LLM 调用必须集成 Langfuse + 走封装层"升级为"所有 AI 服务调用必须通过统一 Gateway 入口，禁止业务代码直接导入 provider 包"

### S0 Review 采纳清单

| 建议 | 严重性 | 处理 |
|---|---|---|
| Langfuse 故障阻塞主路径风险 | P0 | ✅ 采纳（写入风险表 + 硬约束） |
| Stream 模式 token 采集风险 | P0 | ✅ 采纳（风险表） |
| "100% trace 覆盖率" 无检测手段 | P0 | ✅ 采纳（改为可 grep / 可 count 指标） |
| Task Profile 清单有遗漏 | P1 | ✅ 采纳（声明为非完整示例 + 设 S2 穷举 gate） |
| "5 分钟"隐藏前提 | P1 | ✅ 采纳（加前提脚注） |
| 缺 pricing 快照 / fallback 精确语义 / retry 层叠风险 | P1 | ✅ 采纳（写入风险表） |
| 吸收 llm-model-switch 过于轻巧 | P1 | ✅ 采纳（S1 前置 schema/API inventory 任务） |
| 越级 S1/S2 细节（字段清单/接口签名/目录路径/中间件顺序） | P1 | ✅ 采纳（删精确字段名和顺序断言，保留意图） |
| 工时 7-9 天低估 | P1 | ✅ 采纳（改为 10-14 天） |
| `internal/pkg/llm/` 已存在，不是"新建" | 事实纠错 | ✅ 采纳（改为"扩展"） |
| Capability Matching 作为 stretch | P2 | ❌ **不采纳**，保留为 Phase 1 核心（见下方反驳） |
| OCR/ASR 不是 LLM，抽象会变脏 | P1 | ✅ 部分采纳：项目负责人决策一并纳入；通过 `service_type` 字段区分不同抽象，并改名为 AI Service Manager |
| Phase 1 范围过多 | P2 | ❌ 不采纳（capability matching + 审计日志保留为核心） |

### 二轮 Review 追加修订（2026-04-15）

| 建议 | 严重性 | 处理 |
|---|---|---|
| 迁移期双路径双记账风险 | P1 | ✅ 加入风险表 |
| UsageRecord schema 需容纳 token/次数/秒三种计量 | P1 | ✅ 加入 S1 前置任务 #5 |
| `ai.Call` 单入口 vs 多 method 决策 | P1 | ✅ 加入 S1 前置任务 #7 |
| `config_*.yaml` 模型配置与 Registry 的关系 | P1 | ✅ 加入 S1 前置任务 #6 |
| Gateway 中间件 / adapter 测试策略 | P1 | ✅ 加入成功指标"测试覆盖" |
| Capability Matching 反驳论据"运行时崩"不严格 | P2 | ✅ 反驳文案修正：区分运行时 check vs 管理端静态 check |
| OCR/ASR 调用点少，"灰度"语义退化 | P2 | ✅ 迁移策略补充"调用点 ≤ 2 可一次性切换" |
| Gateway 自身故障无告警 | P2 | ✅ 加入风险表 + 健康检查要求 |
| S6 部署顺序未明说 | P2 | ✅ 迁移策略补充发布顺序 |

### 反驳：Capability Matching 为何必须是 Phase 1 核心
澄清：此处的 "Capability Matching" 特指**管理端保存时的静态校验**，与 Gateway **运行时**的 capability check 是两回事，两者都要有但职能不同：
- **运行时 check**（Gateway 中间件执行前）：必须 Phase 1，否则一旦绑定错了就在用户请求时报错，体验极差
- **管理端静态 check**（保存绑定时）：本项

保留为 Phase 1 核心的理由：
- 防止非法绑定持久化到 DB，避免运行时 check 成为唯一防线
- 管理员保存瞬间即时反馈（"该服务不支持图片输入"），而不是发版后等用户投诉
- 实现成本极低（对着 Registry 字段做几行比对），推迟的节省工时可忽略，但推迟后 Registry 就退化为一个"好看的列表"，Task Profile 的核心价值被削弱

### S1 Proposal 前必做
1. 对 `numind-server/internal/numind/biz/` 穷举扫描，列出所有 AI 服务调用点（不止 LLM），输出完整 Task Profile 候选清单
2. 对 `llm-model-switch` 既有产出做 inventory：
   - 模型表每个字段的去留/合并决策
   - 已暴露的 API（`/v1/models`、用户偏好读写等）的向后兼容方案
   - 管理端已有的模型管理 UI（如有）的去留
3. 明确 `chatbot.stream` 与 `salesrag.chat` 是同一 Task Profile 还是分开
4. 明确 OCR/ASR 的 Registry schema 子集设计（复用还是独立字段组）
5. 明确 pricing 单位统一策略（建议：LLM 元 / 每 1M tokens；OCR 元 / 每次调用；ASR 元 / 每秒）**并同步设计 UsageRecord 如何容纳三种计量**（token/次数/秒并存字段？nullable？JSON `usage_detail`？）
6. 决策 `config_*.yaml` 中现有模型配置（`.claude/rules/ai-service.md` §2 表格所列）的去向：全部迁入 Registry（单一数据源）/ 保留 config 作为 bootstrap 种子 / 双源。推荐"迁入 DB，config 仅保留 provider 凭据"
7. 决策业务层调用入口形态：**单入口** `ai.Call(ctx, taskID, req)` + 类型断言/any，还是**多 method** `ai.CallChat / ai.CallOCR / ai.CallASR`（Go 类型系统下两者 trade-off 明显，S2 需正面回答）

### 迁移策略补充
- 调用点 ≤ 2 的模块（如 Baidu OCR、FunASR ASR）可一次性切换 + 保留回滚 commit，不需要灰度周期
- 调用点密集的模块（SalesRAG、SOP）按 Task Profile 逐个迁移
- **S6 部署顺序**：新 Gateway 代码先部署（此时无调用方走它）→ 录入 Registry + Task Profile 种子数据 → 各业务模块逐一切换 → 观察 Langfuse 和 billing 正常后，下一个模块 → 全部迁完后删除老封装层并发版
