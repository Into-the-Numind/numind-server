# RAG 升级总计划（1/2/3/4/5）— compact 交接文档

> **用途**：本文档是 RAG 升级工程的完整交接计划，设计为可扛过 context compact。compact 后接手者**先读本文 + `docs/research/2026-06-20-rag-best-practices-and-weknora.md`**，即可在不依赖对话历史的情况下继续执行。
> **状态**：计划已定，尚未开工（截至 2026-06-20）。按 NDF Standard 逐项推进，每项先 dev 验证再上 prod。
> **关联记忆**：`project_rag_eval_harness_and_retrieval_topology`、`reference_rag_best_practices_and_weknora`。

---

## 第 0 部分：为什么做这件事（证据基础，别重复踩）

### 0.1 产品本质（代码已验证）
莫小派/有数的 **销售助手 salesrag** 是"销售教练 + RAG grounding"：`SalesRAGService.RetrieveForResponseV2`（`internal/numind/biz/salesrag/service/sales_rag.go:107`）对**每一条** query 无条件做：①选销售策略 `DetermineStrategy`（chatMode=sales）②主通道检索（产品/案例/FAQ 文档）③观点库通道检索（`OpinionEvidence`）→ 三者 + 策略一起喂答案生成。**没有"跳过检索"分支**。即：教练类回答也靠检索案例/观点 grounding，不是纯生成。

多客户平台：莫小派 + iDriveCareer(iDC) + 其它子账户，**各有自己的 KB**（产品/案例/FAQ/观点 doc 分别属于不同 user_id）。

### 0.2 真实用量分布（prod sales_message 实测，n=195）
- **教练类 coaching 83%**（话术/文案/策略/客户心理/跟进："怎么追单"、"发什么朋友圈"、"这客户什么心理"）。
- **事实查询 lookup 17%**（"价格怎么收"、"分期吗"、"保offer退多少"、"offer率"、"老师背景"）。
- 最高频提问者 user 455(50q) **无 KB**（doc_ids 空）→ 排除。可测的 ~117 条来自 359/348/345/360/322/1，各打各的 KB。

### 0.3 现有系统在真实问题上的得分（实测，务必记住）
- **事实 lookup 检索可答性**：no_floor=true 严格 15%，prod 保底 10%（n=20）。"分期吗/保offer/offer率/老师背景"这些**答案明明在 FAQ 里却捞不出来**。
- **grounding-usefulness（正确尺子，含教练类）**：旧切块 24%。
- **重切块 A/B（user 348，结构感知小块）**：24%→**40%**（+16pp，稳健）。改写器叠加：旧24/旧+改写28/新40/新+改写36 → **改写器 ±4pp 在噪声内、无叠加增益**。
- **天花板 ~40%**：剩 60% 是情境教练问题，瓶颈是 **matching/覆盖**，切块/改写碰不到。

### 0.4 根因（诊断已确认）
- 切块坏：`embedding_splitter.go` MaxChunkSize=4000 太大 → 块是 ~2500 字"大杂烩"（问卷流程+问候话术+FAQ目录+答案揉一起）；FAQ 切成"问题变体串"无答案；观点库 68 条观点塞进 5 块（~13条/块）；部分文档 UTF-8 乱码（灌库编码就坏，iDC doc29 明显）。
- 抽象鸿沟：情境提问（"客户老说再考虑怎么办"）与答它的案例（"客户预算犹豫，分期促成"）**几乎零字面重叠**，纯语义检索碰运气。这是第 4 项要解的。

### 0.5 三方调研收敛结论（详见 research 文档）
学术/工业界最佳实践 + 腾讯 WeKnora 真实代码 + 我们实测**三方一致**：
- **该做**：①结构感知小块切块 ②混合检索 dense+BM25+RRF ③重排硬化 ④桥接情境↔案例（通用自动版=doc2query/自动问题，非手工销售 facet）⑤拒答/兜底。
- **别碰**：GraphRAG（WeKnora 的"KG"是噱头：每chunk LLM抽实体+1跳CONTAINS，默认关）、HyDE/盲目改写（强embedder下无益甚至有害，我们实测无增益）、RAPTOR、embedding微调、ColBERT一阶段、语义(breakpoint)切块（NAACL2025证明不如固定切块）。

---

## 第 1 部分：现状代码/基建快照（接手者必读）

### 1.1 检索基座 `internal/pkg/retrieval/`
- **`retrieve/service.go`** — `Service{store, rewriter, docStore, gate}`。`NewService(store, rewriter, docStore)` + `.WithGate(g)`。
  `Retrieve()` 流程：resolveScope → **determineQueries**（PrewrittenQueries 优先 → rewriter.Rewrite → 原query；含 HyDE 追加）→ **parallelSearch**（多 query 扇出 + 按 chunk.ID 去重，limit=TopK）→ **rerankWithLimit**（RerankTopN 截断 + 阈值 `rerankScoreThreshold=0.3`/RerankMinScore + RerankNoFloor 保底）→ **可答性门**（opts.AnswerabilityCheck && gate!=nil → CanAnswer，不能答清空 chunks）→ 返回。
- **`retrieve/types.go`** — `Options{TopK, RerankTopN, RewriteQuery, PrewrittenQueries, History, BillingLabel, RerankMinScore, RerankNoFloor, AnswerabilityCheck}`；`Scope{UserID, DocumentIDs, AllEnabled}`。
- **`port/`** — `QueryRewriter.Rewrite(ctx,query,history)→RewriteResult{Queries,HyDE}`；`AnswerabilityGate.CanAnswer(ctx,query,chunks)→(bool,reason,err)`；`VectorStore`；`domain.KnowledgeChunk{ID,DocumentID,UserID,Content,Vector,Tags,Summary,SourceRef,Score}`。
- **`ingest/`**（切块在这，**第 1 项主战场**）— `embedding_splitter.go`（**MaxChunkSize=4000 / MinChunkSize=500**，调用 `scripts/semantic_server.py:9093` bge-small-zh 语义切块）、`hybrid_splitter.go`、`enhanced_splitter.go`、`splitter.go`、`splitter_adapter.go`。

### 1.2 我们这个 session 已建/已改（都在 develop）
- **`internal/numind/biz/rag/`**（新包，flag-gated 默认关）：`UniversalRewriter`（中性 prompt→{补全+多路改写+HyDE}）、`Gate`（可答性门，fail-open）、`FlaggedRewriter`（flag开=universal/关=fallback原改写器）。flag 常量 `FlagUniversalRewriter="features.universal_rewriter.enabled"`、`FlagAnswerabilityGate="features.answerability_gate.enabled"`。
  接线：`biz.go:668` chatbotRetrieve、`biz.go:263` agentRetrieve、`sales_rag.go:62` salesrag——都用 FlaggedRewriter；chatbot+salesrag主通道 `.WithGate()`+`AnswerabilityCheck=true`；salesrag观点库不挂门。
- **rag-eval 调试端点**：`POST /v1/admin/rag-eval/retrieve`（`router.go` ~590，flag `features.rag_eval.enabled`，dev开/prod关）。body：`{query,user_id,document_ids[],top_k,rerank_top_n,rewrite_query,rerank_min_score,rerank_no_floor}`，返回 `data.chunks[{document_id,score,preview(80 runes)}]`。用 `b.RagRetrieve()`=chatbotRetrieve。
- **模型**：改写器/门走 `aiservice.Chat(ctx, profile.SalesragIntent, ...)` → **deepseek-v4-flash**（dmxapi，非思考 enable_thinking_kwarg，本session 从 qwen-turbo 切的，dev+prod DB 都改了）。
- **实验残留（需清理/注意）**：dev `config_dev.yaml` 的 `universal_rewriter.enabled` 当前被我**临时改 true 部署了**（34825ca0-dirty，working-tree 未提交，develop 提交值仍是 false）。**实测改写器无用 → 应改回 false**（dev 容器到下次部署前还跑着 true，flag-gated 无害）。

### 1.3 向量库 / 模型 / 重排
- **sqlite-vec**：表 `chunks(id TEXT PK, document_id, user_id, content, summary, source_ref, tags)` + `vec_chunks` vec0(`id TEXT PK, embedding float[2048] distance_metric=cosine`)。检索按 user_id + document_id 过滤。dev：`/opt/numind/dev/sales_vector.db`；prod：`/opt/numind/prod/sales_vector.db`。
- **embedding**：text-embedding-v4 @2048，ali-dashscope compatible-mode（`https://dashscope.aliyuncs.com/compatible-mode/v1/embeddings`，provider id=4）。salesrag.embed **无 task_profile 绑定**（config 路由）。query 向量和 chunk 向量必须同模型同维。
- **rerank**：dmxapi bge-reranker-v2-m3-free（**不稳/限流**）+ cascade（prod 有 ali/qwen3-rerank fallback，svc31/svc22）。salesrag.rerank task。
- **admin 服务（9099）无 AI gateway + 无向量卷** → 检索/AI 端点必须在用户服务（9091）。

### 1.4 部署 / 环境
- dev 部署：`bash scripts/cicd/release.sh dev server`（rsync working-tree→构建机139.155.129.13→TCR→dev 9091）。**rsync 用 working-tree，改 config 不提交也能部署**。
- prod 是独立 cherry-pick release 线（剥 agent mode），**user-gated，要 git tag**。**本计划全程 dev 验证，prod 上线单独走、要用户确认**。
- SSH：env 变量 `DEV_SSH_*`/`PROD_SSH_*`/`BUILD_SSH_*`。MySQL 容器 `numind-mysql-prod`/dev，密码 `Numind2025`，库 `numind-prod`/`numind-dev`。

---

## 第 2 部分：数据/工具资产清单（含再生成配方）

> ⚠️ **`/tmp/*` 文件 compact 后大概率丢失，必须能重建。** 已 commit 的在 repo 里。

### 2.1 已 commit（在 repo，持久）
- `scripts/rag_eval/`：`golden_idc.yaml`（68 题 iDC golden，docs 26,29,37,41,49,50,55,56,57,58,61,97,98 / user 350）、`idc_eval.py`、`measure_idc.py`、`run_eval.py`、`REAL_DATA_RAG_FINDINGS.md`、`THRESHOLD_TUNING.md`。
- `docs/research/2026-06-20-rag-best-practices-and-weknora.md`（调研报告）。
- 本文档。

### 2.2 ephemeral（/tmp，需重建）+ 再生成配方
- `/tmp/prod_sales_q.tsv`（195 真实问题+doc_ids+verdict）。**重建**：
  ```
  sshpass -p "$PROD_SSH_PASS" ssh ... "docker exec -i numind-mysql-prod mysql -uroot -pNumind2025 -D numind-prod --default-character-set=utf8mb4 -N -e \"
  SELECT m.user_id, s.id, REPLACE(REPLACE(m.content,'\t',' '),'\n',' '),
   COALESCE(s.faq_doc_ids,'[]'),COALESCE(s.product_doc_ids,'[]'),COALESCE(s.case_doc_ids,'[]'),COALESCE(s.document_ids,'[]'),
   COALESCE(LEFT(m.verdict,40),'')
  FROM sales_message m JOIN sales_session s ON s.id=m.session_id WHERE m.role='user' AND m.deleted_at IS NULL ORDER BY m.user_id;\"" > /tmp/prod_sales_q.tsv
  ```
- `/tmp/prod_eval_chunks.json`（34 真实 doc / 292 chunk + 2048维 embedding）。**重建**：prod `cp /opt/numind/prod/sales_vector.db /tmp/psv.db`，python `sqlite_vec` + `db.text_factory=lambda b:b.decode('utf-8','replace')`（**内容有乱码，必须容错解码**），`SELECT id,document_id,user_id,content,summary,source_ref,tags FROM chunks WHERE document_id IN (34个id)` + `SELECT c.id, vec_to_json(v.embedding) FROM chunks c JOIN vec_chunks v ON v.id=c.id WHERE ...` → JSON。34 docs：13,14,15,26,29,31,33,38,40,41,42,44,46,48,50,52,54,69,71,72,73,74,76,80,82,83,84,93,96,97,99,100,106,109。
- `/tmp/gate_creds.txt`（dmxapi base_url+key，judge/改写用）：`SELECT base_url,api_key FROM llm_provider WHERE id=1`（dmxapi）。base_url=`https://www.dmxapi.cn/v1`，model=`deepseek-v4-flash`，judge 调用带 `chat_template_kwargs:{enable_thinking:false}`。
- `/tmp/ali_key.txt`（embedding key）：`SELECT api_key FROM llm_provider WHERE id=4`（ali-dashscope）。
- 评测脚本（重写即可，逻辑见下"验证方法"）：`gate_proto3.py`(可答性门原型)、`real_eval.py`(端到端可答性)、`classify.py`(lookup/coaching分类)、`lookup_eval.py`(事实题检索率)、`ab2x2.py`(2x2 A/B)、`pilot_gen.py`(348重切块+重嵌)。

### 2.3 dev 向量库当前状态（已注入，可复用做 A/B）
- iDC user 350（golden_idc 用）。
- 34 个 prod 真实 doc（**真实 user_id**：1/322/345/346/348/350/351/359/360）——prod 问题端到端测用。
- **pilot user 990348**（348 的 KB 结构感知重切块版，docs 990069/990071/990073/990074，111 小块）——A/B 已证 +16pp。
- 注入法：python `sqlite_vec` INSERT 进 `chunks`+`vec_chunks`（embedding 传 vec_to_json 的 JSON 串），WAL 在线插不重启，id 已存在则跳过。

### 2.4 隧道（本地连 dev）
```
sshpass -p "$DEV_SSH_PASS" ssh -fN -L 19091:localhost:9091 -L 19099:localhost:9099 "$DEV_SSH_USER@$DEV_SSH_HOST"
# admin login 拿 token: POST 19099/v1/admin/login {username:admin,password:admin123456} → data.token
# 检索: POST 19091/v1/admin/rag-eval/retrieve (带 Bearer token)
```

---

## 第 3 部分：五项计划详述

> 每项：目标 / 做法 / 关键技术决策 / 改动点 / 验证 / NDF档 / 依赖。全部**先 dev、用 §4 harness 验证、再 user-gated 上 prod**。

### 项 1：结构感知重切块 + 父子块 + 预览工具 + 灰度重灌
- **目标**：把"大杂烩巨块"换成聚焦小块，让"含答案的块"能被检索到。实测这把 grounding-usefulness 拉 +16pp。
- **做法**（学 WeKnora 自适应切块）：
  - 切块器改造（`internal/pkg/retrieval/ingest/`）：目标块 ~**512 字**、overlap ~15%；**结构感知**——FAQ→按问答对（"N.问 + 回答"边界）、观点库→按单条观点（"观点N"/"###"）、案例→按单案例、产品→按节；保护单元（表格/代码/`$$math$$`/图片链接不切）。
  - **标题面包屑注入**：每块 embedding 文本前缀 `# 顶 > ## 节`（WeKnora 实证：+5% token 换 30-50% 更少块 + 检索大涨）。
  - **父子块**：子块 ~384 字（匹配用，嵌入）、父块 ~2048-4096 字（喂 LLM）；`retrieve.Service` 检索命中子块后扩展回父块（新逻辑）。
  - **document profiler + validator**：探测结构选档，validator 拒绝退化输出（如产生大量单行块）回退固定切块。
  - **切块预览工具**：`POST /v1/admin/chunker/preview`（admin/flag-gated），返回某文档会被切成哪些块 + 选了哪档 + 为何拒绝别档。re-index 前可调，不盲灌。
- **关键决策**：从**干净源文本**重切（不是从已乱码的旧 chunk 再切——旧 chunk 有 UTF-8 乱码 + 已被坏边界切过）。源文本来自 `knowledge_document.file_path` 重新解析，或 docreader。**修编码**是这一项的隐含收益。
- **改动点**：`ingest/*splitter*.go`（切块逻辑）+ 父子块 schema/检索（`retrieve/service.go` + chunks 表加 parent_id 或单独父块表）+ 新预览端点（controller+router）+ 重灌 migration/脚本。
- **验证**：dev 上对 348（+ 再选 1-2 客户 359/360）重切重嵌，用 §4 A/B（旧切块 vs 新切块）测 grounding-usefulness，要复现 +16pp 且无大面积回归。
- **NDF**：Standard（多文件 + schema + 重灌，高风险）。**这是地基，先做。**

### 项 2：混合检索 dense + BM25 + RRF（中文分词）
- **目标**：补上关键词检索，命中产品码/型号/精确 FAQ 措辞（纯 dense 会糊掉）。
- **做法**：
  - **关键词索引**：sqlite-vec 无 BM25 → 用 sqlite **FTS5**（自带 BM25）建关键词索引；中文用 **jieba 分词 + 产品名/术语自定义词典**（FTS5 自带 trigram 兜底但弱，优先 jieba）。
  - **融合 RRF**：`RRF=vw/(k+vecRank)+kw/(k+kwRank)`，默认 **k=60, vec=0.7, kw=0.3**（WeKnora 默认）。只有一路命中时旁路 RRF、保原始分（护 FAQ 相似度语义）。
  - 在 `retrieve.Service.parallelSearch` 之后/之内加 keyword 检索 + RRF 合并，再进 rerank。
- **关键决策**：FTS5 vs 引入外部搜索引擎（ES/bleve）——**优先 FTS5**（轻、随 sqlite 走、零新依赖）。中文分词是成败点，必须配产品词典。
- **改动点**：新建 keyword store（FTS5 表 + jieba 分词，可能用 cgo 或 Go 分词库如 gojieba）+ RRF 融合逻辑 + ingest 时同步写 FTS5。
- **验证**：A/B 纯 dense vs dense+BM25+RRF，重点看产品码/精确措辞类 query 的命中。
- **NDF**：Standard。依赖项 1（切块产出的块要同时进向量库 + FTS5）。

### 项 3：重排硬化
- **目标**：修可靠性（免费档 reranker 限流静默失效）+ 提精度 + 优雅降级。
- **做法**（学 WeKnora rerank.go）：
  - **passage 清洗**：rerank 前剥 markdown/LaTeX/表格/图片/链接噪声，补图片 OCR/caption + 块的自动问题（见项4）。
  - **复合分**：`0.6*模型分 + 0.3*召回分 + 0.1*源权重` × 位置先验。
  - **MMR 去重**（lambda~0.7）：避免返回 3 条雷同产品介绍。
  - **每库校准阈值**：Cohere 法——跑 30-50 条代表性中文 query 取边界分均值做该库阈值；换模型重标定。
  - **降级兜底链**：0 结果→阈值×0.7 重试→top-1 floor(≥0.15)→reranker API 挂退回原召回序。**修免费档不稳**（接 cascade / 换稳定源）。
- **改动点**：`retrieve/service.go` 的 rerankWithLimit 增强（清洗/复合分/MMR/降级）+ 阈值配置（每库）。我们已有 no_floor/cascade 基础。
- **验证**：A/B + 故意打挂 reranker 验降级不杀 run。
- **NDF**：Standard（或 Hotfix 拆分：可靠性修复可先单独走）。依赖项 1/2（在新块+混合候选上重排）。

### 项 4：桥接情境↔案例（通用自动版，**非手工销售 facet**）
- **目标**：解情境教练那 ~60% 天花板的真问题——情境提问与案例零字面重叠。**用通用自动技术，进基座，不做销售专属 taxonomy。**
- **做法（优先级）**：
  1. **doc2query / 自动问题**（WeKnora 已用）：ingest 时给每个 chunk（尤其案例/观点）LLM 自动生成"它能回答哪些问题"（如案例"预算犹豫分期促成"→生成"客户嫌贵犹豫怎么办/客户说再考虑如何跟进"），**这些问题一起嵌入索引** → 情境提问就能命中。全自动、零 taxonomy、通用、进基座。
  2. （选）**Contextual Retrieval**（Anthropic）：每块 LLM 预生成一段上下文再嵌入 ≈ 自动 facet。成本较高，观点库试点、held-out recall 验证后再推。
  3. （远期/可选）手工销售 facet（异议类型/客户阶段/结果/行业）——**仅当某客户有清晰 taxonomy 时叠加**，不作为通用基座。
- **关键认知（compact 别丢）**：用户曾担心"facet 只针对销售不通用"——澄清过：**目标（桥接抽象鸿沟）是通用 RAG 难题**（法律/医疗/客服都有），**手工 facet 只是最专属最重的实现**，doc2query/contextual 是通用自动实现、该进基座。越偏教练/咨询的 RAG 越吃这个。
- **改动点**：ingest 时调 LLM 生成每块问题（走 aiservice，注意成本/计费）+ 把生成问题并入嵌入文本或作为子块 + rerank 时用上（项3 的 passage enrich 已含"块的自动问题"）。
- **验证**：A/B（项1/2/3 全开 vs 再加 doc2query）测**情境教练类**问题的 grounding-usefulness，看能否突破 40% 天花板。这是检验第 4 项价值的关键实验。
- **NDF**：Standard。依赖项 1（在新切块上生成问题）。**第二阶段**（1/2/3/5 测完再上）。

### 项 5：拒答/兜底工程
- **目标**：检索不到相关内容时优雅处理，不幻觉、不 dead-end。我们已建 Gate（flag-off）。
- **做法**：
  - **reranker 分门控**（非裸 cosine——cosine 各向异性会判反）：用校准后的 rerank 分判"够不够相关"。
  - **可答性门**：复用已建 `biz/rag/Gate`（CanAnswer），但**前提是项1切块修好后再开**（否则误拒，实测原型证明检索捞不到答案时门会大面积拒答）。
  - **显式拒答提示**：system prompt 加"只用检索到的内容答，没有就说库里没有"（UAEval4RAG：提示工程单独 +80%）。
  - **"列出库里有什么"兜底**（学 WeKnora）：检索空时给 LLM 喂该库文档清单，回"库里有 X/Y，你要的没有"+给相邻案例/产品，**不死板拒答**。
- **改动点**：`biz/rag/Gate` 调 reranker 分 + salesrag 答案生成的兜底分支 + system prompt。
- **验证**：A/B 库外问题拒答率（要高）+ 库内不被误拒（要低）+ 兜底回答质量。
- **NDF**：Standard。**依赖项 1/3**（切块+重排修好，门才不误拒）。最后做。

---

## 第 4 部分：验证方法（贯穿，dev harness）

- **数据**：prod 真实问题（`/tmp/prod_sales_q.tsv`，§2.2 重建）+ 各客户 KB 已注入 dev（§2.3）。
- **指标**（用对尺子，别再用字面 answerability）：
  - **grounding-usefulness**（主指标）：LLM judge（dmxapi deepseek-v4-flash，非思考）判"检索到的资料是否相关且能支撑优质销售回答（事实答案 or 话术/案例引用都算）"，输出 `{useful:true/false}`。
  - **文档级 vs 块级 recall 分开报**（我们踩过"对文档错块"：doc-recall 0.845 但块级可用 0.29）。
  - **lookup vs coaching 分桶报**（17%/83%），分别看改善。
- **A/B 协议**：同一批真实问题，retrieve（rag-eval 端点，no_floor=false + min_score=0.01 取 top5 候选给 judge）跑各 arm → judge → 比。注意 **n=25 有 ±8pp 判官噪声**，看趋势别抠点；要结论稳就扩到 ≥100 题或多客户复跑。
- **judge 校准**：上线前用 50-100 条人工标注切片校准 judge（报 Cohen κ），别盲信 LLM judge 绝对值。
- **dev rerank 不稳**：免费档限流会让检索静默返空 → 跑 harness 前先探活，失败重试/退避。

---

## 第 5 部分：执行顺序、依赖、prod 安全

### 5.1 顺序（依赖驱动）
```
项1 (切块+预览+重灌, 地基)
  └→ 项2 (混合检索, 块要同进向量+FTS5)
  └→ 项3 (重排硬化, 在新块+混合候选上重排)
        └→ 项5 (拒答/兜底, 切块+重排好门才不误拒)  [第一阶段收尾]
  └→ 项4 (doc2query 桥接, 在新块上生成问题)  [第二阶段, 1/2/3/5测完再定]
```
第一阶段 = 1→2→3→5（用户已定）。第二阶段 = 4（破 60% 天花板的钥匙，做完一阶段测完用数据决定）。

### 5.2 prod 安全（硬约束）
- **全程 dev 验证**，不碰 prod 在线数据 / 不改 config_prod.yaml。
- 重灌（项1）：dev 重切重嵌验证收益 → prod 走**灰度重灌**（单客户先行）、user-gated。
- prod 上线走独立 release-no-agent cherry-pick 线、要 git tag、要用户确认。
- 改写器实验残留：dev `config_dev.yaml universal_rewriter` 改回 false（见 §1.2）。

### 5.3 NDF 流程
每项 `ndf-start standard <slug>` → S0 需求 → S1 提案 → S2 设计 → S3 计划（含 S5 验证策略）→ S4 编码（每 task 双 reviewer 并行）→ S5 dev 验证 → S6 ndf-done → 部署 dev。每项独立 feature。

---

## 第 6 部分：未决问题 / 风险
1. **干净源文本来源**（项1）：`knowledge_document.file_path` 重新解析 vs 从旧 chunk 拼——前者干净但重，需确认源文件可取（COS/本地）。
2. **中文分词落地**（项2）：gojieba(cgo) vs 纯 Go 分词；产品词典从哪来（客户 KB 抽取）。
3. **FTS5 多租户隔离**：keyword 索引也要按 user_id+document_id 过滤。
4. **doc2query 成本**（项4）：每 chunk 一次 LLM，ingest 变贵；走 v4-flash 控成本 + 计费归属（internalCallCtx userID=0 不扣用户）。
5. **父子块改 schema**：chunks 表加 parent 关系，影响现有检索/重灌，要 migration。
6. **judge 噪声**：A/B 结论要扩样本（≥100 或多客户）+ 人工校准，别凭 n=25 拍板。
7. **天花板预期**：1/2/3/5 大概率拉不动情境教练 60%，那是项4。别把一阶段没破 60% 当失败。

---
*最后更新 2026-06-20。接手者：读完本文 + research 文档即可继续，无需对话历史。*
