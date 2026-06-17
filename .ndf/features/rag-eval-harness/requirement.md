# 需求卡片：rag-eval-harness（RAG 检索评估集 + 打分工具）

> S0 · feature id: rag-eval-harness · track: standard · 2026-06-17

## 1. 一句话

建一套"标准考卷 + 自动打分工具",把 RAG 检索质量变成**可量化的分数**,让后续每一项改进(混合检索/查询处理/阈值调优/上下文组装)都能科学地"改前改后跑分对比",这是把 RAG 打造成业界一流的**地基与前提**。

## 2. 为什么必须先做(地基)

没有评估集,"一流"无法证明、改动无法判断好坏——所有调优都是盲调、靠感觉。有了它,RAG 提升变成科学闭环:**测基线 → 改一处 → 再测 → 涨了留/降了回退 → 迭代到达标**。

## 3. 方法论(科学全面)

### 3.1 黄金集(golden set)
一批"问题 + 正确答案应来自哪份文档"的标注数据。覆盖多种问题类型(业界标准):
- **库内单文档**:答案明确在某一篇里。
- **库内多文档**:答案需要跨几篇。
- **专有名词/数字精确**:产品名、价格、人名(考验关键词召回——纯向量易漏)。
- **改写/口语**:同义不同词,考验语义召回。
- **库外/应拒答**:知识库没有 → 期望"检索不到/不编造"(考验阈值 + grounding)。

### 3.2 指标(业界标准检索指标)
- **Recall@k**(该找到的文档有没有进前 k 名)——首要。
- **MRR / nDCG**(正确文档排得够不够靠前)。
- (后续可加)**答案忠实度/相关度**:LLM-as-judge 评端到端回答是否有据、不编。

### 3.3 工具(harness)
复用**真实检索栈**(`retrieve.Service.Retrieve`,同 chatbot/salesrag 路径)+ 读黄金集 YAML + 跑每题 + 算指标 + 输出报告,可重复执行。**不碰生产行为**。

> **架构落地(对 S0 设想的偏离,已评审采纳)**:最初设想是 dev 容器内独立运行的 Go CLI `cmd/rag-eval`。实际改为 **admin-gated 只读端点 `POST /v1/admin/rag-eval/retrieve` + 外部 Python 打分脚本 `scripts/rag_eval/run_eval.py`**。原因:① 端点直接复用已 wiring 好的 `retrieve.Service`(经 biz.RagRetrieve()),零重复构建检索栈;② 打分逻辑(recall@k/MRR/nDCG)用 Python 迭代更快、不进编译产物。代价:多了一个 admin-gated 端点(只读、admin token 守卫、BillingLabel="rag_eval"、不在任何生产用户流程引用)。
>
> **端点落在【用户服务 9091】而非 admin 服务(运行时踩坑后定位)**:检索栈在 admin 服务上**跑不起来**——(a) admin 进程历史从不初始化 AI gateway → 首个 embed 触发 `aiservice.Default()` panic(且 embed 在 goroutine 里,gin.Recovery 兜不住,整进程崩);(b) 即便补了 gateway,admin 容器**未挂载** sqlite-vec 向量卷(`/opt/numind/dev`),检索读到空库返回 0 结果。两者都只在用户服务进程/容器具备。故把端点注册在 `router.go`(用户服务),用 `AdminAuthMiddleware` 守卫(admin token+IsAdmin,可移植),复用的正是生产 chatbot 同一个 `retrieve.Service`——**评估即真实链路**。曾短暂尝试给 admin 加 gateway(hotfix rag-eval-admin-gateway)后已整体回退。

## 4. 必须由你(业务方)定/确认的两件事(科学前提)

1. **评估锚定哪个 KB 范围**:dev 知识库是多用户混杂的(~95 文档、大量重复/FAILED)。评估必须锚定一个**干净、一致的目标**——建议用一个代表性账号的真实 KB(比如莫小派的产品/案例/百问百答那套),或你指定。
2. **黄金集的"标准答案"**:我从真实聊天记录(Langfuse)+ 该 KB 内容**起草** 30-50 题 + 标注"答案该来自哪篇",**你抽查确认对不对**(尺子准不准全看这步)。

## 5. Triage

**Standard**:新增评估包/CLI + 方法论需严谨设计 + 多文件。无 DB schema/无新生产 API/无新外部服务。纯 dev 工具,风险低,但作为地基要做扎实。

## 6. 范围

- **In**:黄金集 YAML(锚定一个 KB)+ Go CLI 打分工具(recall@k/MRR/nDCG)+ 基线报告 + 文档化"如何跑/如何加题"。
- **Out**:答案忠实度 LLM-judge(后续增量);评估 UI;自动化 CI 集成(后续);多 KB/多用户全量评估(先一个干净锚点)。

## 7. 后续(这是整个 RAG 一流计划的第 1 步)

地基建好后,按序:② 混合检索 → ③ 查询处理(改写/一题多搜/HyDE)→ ④ rerank 阈值调优 → ⑤ 上下文组装深度优化。每步都用本评估集跑分验证。
