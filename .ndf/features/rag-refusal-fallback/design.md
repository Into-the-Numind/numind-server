# S0-S3 — rag-refusal-fallback（RAG 升级项 5：拒答/兜底工程）

> RAG 总计划项 5（第一阶段收尾）。依赖项 1/3（切块+重排好门才不误拒）。全程 dev、不碰 prod 在线数据、不上 prod。flag 门控。

## S0 需求
检索不到相关内容时现状：salesrag 答案 prompt 的"知识库内容"区直接省略 → LLM 无 grounding，可能幻觉具体产品信息，或干巴巴拒答。研究（WeKnora + UAEval4RAG）：销售助手永不该 dead-end；空检索应"列出库里有什么 + 如实说所问没有 + 推荐相邻"，且显式拒答提示单独可 +80%。

## 范围决策（AI 拍板）
本期交付（低风险、flag 门控）：
- ① **"列出库里有什么"兜底**：检索为空（主+观点通道都空）时，取库内文档清单 + 如实拒答指令注入答案 prompt。
- ② **显式拒答提示**：兜底说明本身即含"只依据检索资料、没有就说没有、绝不编造价格/政策/数据"——覆盖项5的"拒答提示"。
- ③ **启用可答性门**：项1切块已上线（前提满足）→ dev 开 `answerability_gate`（门已在上一 session 建好，flag-gated）。门拒答致空 → ①兜底优雅接住。
- **reranker 分门控**：现状 no_floor + rerank 阈值已用 rerank 分（非裸 cosine）决定召回/空，项5"分门控"实质已具备。

## 设计（最低 churn，复用既有渲染，零回归）
- flag `features.kb_fallback.enabled`（biz/rag）。关 → 不生成兜底，现状（零回归）。
- `RetrievalVerdict += KBFallbackNote string`（service）。
- `RetrieveStream`（salesrag.go biz）：retrieval 后，flag 开 + `len(Evidence)==0 && len(OpinionEvidence)==0` → `buildKBFallbackNote(ctx, 主+观点 docIDs)`：`GetByIDs` 取已启用且 COMPLETED 文档名 → `formatKBFallbackNote`（纯函数）拼"库内容范围 + 如实拒答指令"。
- `buildPromptMessagesV2`：`knowledgeContext=="" && opinionContext=="" && KBFallbackNote!=""` → `knowledgeContext = KBFallbackNote`。**复用既有"### 知识库内容"区渲染**，不改 4 个 prompt builder 签名（sales/free × langfuse/hardcoded）→ 最低回归面。
- config_dev：`kb_fallback.enabled=true` + `answerability_gate.enabled=true`（项1完成翻开）。

## S3 Tasks（合并实现）
- flag + verdict 字段 + RetrieveStream 兜底生成 + buildPromptMessagesV2 注入 + buildKBFallbackNote/formatKBFallbackNote + config_dev + 单测（formatKBFallbackNote）。

## S5 验证策略（Rule 10）
- 后端 dev：用一个**确定不在 348 库内**的问题（如"你们卖不卖汽车保险"）问 salesrag → 期望回复"如实说没有 + 列出库里有产品/案例/百问百答/观点 + 推荐相邻"，而非编造或空答。再用库内问题确认不被误拒（answerability_gate 不过度拒答）。
- 回归保护：formatKBFallbackNote 纯函数单测；flag 关时 salesrag 答案路径零变化（既有测试绿）。
- 注：answerability_gate 开启后须 harness A/B 确认 in-KB recall 不掉、oob 拒答率高（沿用 REAL_DATA_RAG_FINDINGS 口径）——此为门的调参验证，单独迭代。

## prod 安全
两 flag 默认 OFF=现状；不改 4 个 prompt builder 结构；不上 prod。
