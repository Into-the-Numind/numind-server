# 业界最佳 RAG 调研 + 腾讯 WeKnora 深扒 + 落地路线图

> 2026-06-20。三路独立来源：①学术/工业界最佳实践（含 benchmark/ablation），②腾讯 WeKnora 真实代码（commit ae90387，MIT，微信对话开放平台引擎），③我们自己在 prod 真实问题上的实测。
> **核心结论：三方高度收敛——通往业界最佳的路径不是花哨技术，是一小撮被反复验证的杠杆，按序做。**

---

## 0. 结论先行（三方交叉验证）

| 杠杆 | 学术/业界证据 | WeKnora 真实做法 | 我们的实测 | 判定 |
|---|---|---|---|---|
| **结构感知 + 小块切块** | Chroma: 200tok recall 88%；NAACL2025: 语义切块不如固定切块 | 自适应 3 档(heading/heuristic/recursive)+512字+标题面包屑+父子块(384/4096) | 重切块 24%→40%(+16pp)，最大杠杆 | ✅ **第一杠杆** |
| **混合检索(dense+BM25)+RRF** | 混合比纯 dense +2~7%，命中产品码/术语 | dense+keyword(bleve)+RRF(k=60, 0.7/0.3) | 我们目前**纯 dense，无 BM25** | ✅ **要补** |
| **重排 + 每库阈值校准 + MMR** | 检索后第一精度杠杆 +10~25% | 复合分(0.6模型+0.3召回+0.1源)+MMR+降级兜底 | 我们有 cascade 但免费档不稳 | ✅ **要硬化** |
| **可答性/拒答工程** | UAEval4RAG: 提示工程单独 +80% | "列出库里有什么"的兜底，不硬拒 | 我们建了 gate(flag-off) | ✅ **是我们最难问题的解** |
| **情境元数据/facet 标注** | 工业界: matching 瓶颈的真解 | 每库自配实体抽取(弱) | 我们无 facet | ✅ **教练类的真解** |
| **查询改写/HyDE** | 强 embedder 下**无益甚至有害**(HyDE 作者自己说只是冷启动) | 有 rewrite 但**意图门控**(问候/闲聊不检索) | 实测 +0~负，无叠加增益 | ❌ **别盲目上** |
| **GraphRAG** | 仅多跳全局有用；简单检索基础 RAG 更好；成本 ~400x | **不是真 GraphRAG**(每 chunk LLM 抽实体+1跳 CONTAINS，默认关) | 未用 | ❌ **跳过** |

**一句话**：把劲花在 ①结构切块 ②混合检索 ③重排硬化 ④情境 facet ⑤拒答/兜底 上；**不要碰** GraphRAG / HyDE / RAPTOR / embedding 微调 / ColBERT 一阶段。

---

## 1. 业界最佳 RAG 的真实杠杆（按 ROI 排，含噱头标注）

1. **结构感知 + 右尺寸小块（200–512 tok，~15% overlap，表格/代码/公式不切）**。最大、最便宜、最稳。中文按 `。！？；` 正则切，**别用 NLTK**。FAQ/观点本身就是天然切块单元。
2. **交叉编码重排 + 每库校准阈值**。检索后第一精度杠杆（真实 +10~25%，不是厂商吹的 +40%）。阈值校准（Cohere 的 30–50 query 取平均分法）免费且兼任拒答门。换模型要重新标定。
3. **可答性/拒答工程**（reranker 分门控**而非裸 cosine**——cosine 各向异性会判反；+ 显式"不在库内就说没有"提示 + 便宜的 groundedness 检查）。这是 KB 覆盖瓶颈的唯一解，切块/改写都碰不到它。
4. **强中文 embedder + 混合(dense+BM25/RRF k=60)**。中文首选 Qwen3-Embedding-4B(72.27 C-MTEB) 或 BGE-M3(唯一一次出 dense+sparse+multi-vec)。⚠️ 商用模型(OpenAI/Cohere)中文明显弱、且跨境不可用。BM25 catches 产品码/型号/精确 FAQ 措辞——但**中文 BM25 必须 jieba 分词 + 自定义产品词典**，否则退化成字符匹配白搭。
5. **父子检索**（小块匹配 → 父块喂 LLM）。近免费、无 per-query LLM 成本。
6. **判官无关的评估**（recall@k/MRR/nDCG 确定性指标打底）；**文档级 vs 块级 recall 分开报**（我们正踩中这个：对文档错块）；LLM judge 必须用人工标定切片校准(报 Cohen κ)；RAGAS 中文相关性只 ~0.55，当相对信号别当真值。

**最大噱头-证据落差**：语义(embedding-breakpoint)切块（NAACL2025 证明不如固定切块）、Anthropic"67%"（含重排+top20+小说语料，单独上下文化≈35%）、"重排+40%"、ColBERT 一阶段、"cosine 阈值就行"、"引用消除幻觉"(57% 引用不忠实)、"改写/HyDE 总有用"。

---

## 2. WeKnora 真正值得学的（vs 它的营销噱头）

**值得偷的 6 个设计**：
1. **自适应切块：profiler 探测文档结构 → 选档(heading/heuristic/recursive) → validator 拒绝退化输出(如 200 个单行块)回退 → 切块预览工具**(`POST /chunker/preview`，看选了哪档、为何拒绝别档，re-index 前先调)。这是最该学的一招。
2. **标题面包屑注入 embedding 文本**（`# 顶 > ## 节` 前缀）：多 ~5% token，换 30–50% 更少块 + 结构文档检索大幅改善。便宜高杠杆。
3. **重排阶段的防御式设计**：passage 清洗(剥 markdown/LaTeX/表格噪声 + 补图片 OCR/caption) → 复合分 → MMR 去重(别返 3 条雷同产品介绍) → 阈值降级(0 结果就 ×0.7 重试) → top-1 兜底(≥0.15) → reranker API 挂了退回原序。**正面解决我们踩过的"rerank 静默返空/挂掉杀 run"**。
4. **RRF(k=60, vec0.7/key0.3) + 单检索器旁路**（只有一路命中时跳过 RRF、保原始分，护住 FAQ 相似度语义）。~120 行，好移植。
5. **单次 LLM 调用做查询理解(rewrite+intent+图像分析) + 意图门控检索**（问候/闲聊跳过检索，空意图=检索兜底）。省一轮往返 + 不给闲聊瞎检索。
6. **"列出库里有什么"的兜底**(`fallback_strategy: model`)：检索空时给 LLM 喂该库文档清单(≤50)，让它说"库里有 X/Y，你要的没有"，**而不是死板拒答**。销售助手永远不该 dead-end。

**别学的（噱头/过度工程）**：
- **知识图谱**：营销成差异化，真相是"每 chunk 一次 LLM 抽实体 + 查询时 1 跳 `CONTAINS` 模糊匹配"，边无权重，召回提升微薄、成本高(每 chunk LLM + Neo4j 依赖)，默认关(`ENABLE_GRAPH_RAG=false`)。Cypher 支持"开发中"。**跳过。**
- 两套图子系统 + 一坨没接线的 PMI 死代码 = 困惑面积。
- 8 个向量库/7 存储/6 IM 渠道/MCP/Wiki = 企业平台铺面，不是 RAG 质量。

**WeKnora 技术栈**：Go 1.26 单体 + Python gRPC 解析 sidecar(docreader 只出 markdown+图，不切块/不 OCR；OCR 在 Go 侧走 VLM)；pgvector(默认,HNSW) + bleve(关键词) + 可选 Neo4j；asynq/Redis 异步；Langfuse 唯一追踪。切块默认 512 字/overlap 80/父块 4096/子块 384。

---

## 3. 落到我们这套 sales-RAG 的路线图（按 ROI + 依赖排）

> 现状：salesrag = 改写(v4-flash) + 主检索(产品/案例/FAQ) + 观点库检索 + 策略 → grounded 答案。纯 dense(sqlite-vec)，无 BM25，无 facet。重排走 dmxapi 免费 bge-reranker(不稳)。本 session 建了通用改写器+可答性门(flag-off)。

### P0 — 最高 ROI，先做
1. **结构感知重切块 + 父子块 + 预览工具 + 重灌**（学 WeKnora）：FAQ→问答对、观点库→单条观点、案例→单案例、产品→按节 + 标题面包屑注入。实测这能把有用率拉 +16pp。配预览工具，每库可调不盲灌。
2. **加混合检索：BM25/关键词 + RRF(k=60, 0.7/0.3)**，中文 jieba 分词 + 产品名/术语自定义词典。sqlite-vec 无 BM25 → 需引入关键词索引(轻量 FTS / bleve 式)。catches 产品码/型号/精确 FAQ。
3. **重排硬化**：passage 清洗 + 复合分 + MMR 去重 + 每库校准阈值 + 阈值降级/top-1 兜底/API 挂退原序。**并修免费档 reranker 不稳**(可靠性)。

### P1 — 我们最难问题(情境教练 grounding)的真解
4. **案例库/观点库打情境 facet**（objection_type / customer_stage / channel / outcome / industry）：把情境提问分类到 facet → 过滤/加权。这是 matching 瓶颈的真解，比任何检索架构都管用（情境提问和案例几乎无字面重叠，只能在"情境/异议类型"层面匹配）。
5. **拒答/兜底工程**：保留我们建的 gate(配 reranker 分门控 + 显式拒答提示) + 学 WeKnora"列出库里有什么"的兜底（永不 dead-end，给相邻案例/产品）。
6. **KB 覆盖度**：~60% 天花板是覆盖问题——扩充案例/观点库让情境能匹配到东西。这是内容/产品工作，不是工程。

### P2 — 选择性 / 跳过
7. （选）Contextual Retrieval（LLM 给每块预生成上下文 ≈ 自动 facet）——在观点库试点，必须 held-out recall 验证后再推。
8. **明确跳过**：GraphRAG、HyDE/盲目改写（**我们建的通用改写器实测无增益→保持 flag-off 或删**）、RAPTOR、embedding 微调、ColBERT 一阶段。

### 贯穿 — 评估纪律
用我们建的真实 prod 问题 harness，但：**文档级 vs 块级 recall 分开报**；judge 用人工切片校准；用 grounding-usefulness 而非字面 answerability；每改一项跑 A/B。

---

## 4. 对我们之前结论的修正/印证
- ✅ **印证**：切块是第一杠杆；改写/HyDE 无益；覆盖/匹配是天花板。三方一致。
- 🔧 **修正**：我们之前把"教练类"误判为"不查知识库"——错。代码证实每条都检索(观点/案例/策略 grounding)。检索的职责是"为话术找相关案例/观点"，不是"字面回答"。所以评估要用 grounding-usefulness。
- 🔧 **新认知**：matching 瓶颈的真解是**情境 facet 元数据**（之前没意识到），不是更花哨的检索。

---

## Sources（精选）
- Chunking: Anthropic Contextual Retrieval; Chroma chunking research; NAACL2025 semantic-chunking(arXiv:2410.13070); Jina late chunking.
- Embedding/Retrieval: Qwen3-Embedding(arXiv:2506.05176); BGE-M3(arXiv:2402.03216); RRF; HyReC 中文混合(arXiv:2506.21913).
- Rerank: Qwen3-Reranker; bge-reranker-v2-m3; Cohere reranking best practices; ZeroEntropy LLM-rerank.
- Query/HyDE: HyDE(arXiv:2212.10496); RAG-Fusion not-recommended(arXiv:2603.02153); HyDE −7.3%(arXiv:2604.01733).
- Answerability: UAEval4RAG(ACL2025); Vectara HHEM-2.1; "Cosine Similarity Lies".
- Frontier: GraphRAG(arXiv:2404.16130); LightRAG(2410.05779); LazyGraphRAG; When-to-use-Graphs(2506.05690); CRAG(2401.15884); RAPTOR(2401.18059).
- WeKnora: github.com/Tencent/WeKnora (CHUNKING.md, KnowledgeGraph.md, chat_pipeline/, knowledgebase_search_fusion.go, commit ae90387).
