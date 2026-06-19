# S0-S3 — rag-rerank-hardening（RAG 升级项 3：重排硬化）

> RAG 总计划项 3。依赖项 1/2（在新切块+混合候选上重排）。全程 dev、不碰 prod 在线数据、不上 prod。flag 门控零回归。

## S0 需求
现 rerank（`retrieve/service.go` rerankWithLimit + applyRerankFilter）：调 aiservice.Rerank（profile.SalesragRerank）→ 阈值过滤(0.3/RerankMinScore) + 保底1条/NoFloor。已有 cascade（dmxapi bge 主 + ali fallback）。问题：① rerank 输入是带 markdown/表格/链接噪声的原文，干扰打分；② 易返回多条雷同块（如 3 条相似产品介绍）；③ 阈值一刀切，0 结果时只能 top-1 兜底（可能 ground 在垃圾上）。

## 范围决策（AI 拍板）
**本期 MVP（确定性、高价值、可单测）**：
- ① passage 清洗（rerank 输入去噪）
- ② MMR-lite 多样性去重（trigram Jaccard 近重复剔除）
- ③ 降级链硬化（0 结果→阈值×0.7 重试→top-1 floor 仅当 ≥0.15；reranker 挂→原序已有）

**推迟（依赖调优数据/跑分，记 follow-up）**：复合分（0.6模型+0.3召回+0.1源×位置先验，需召回分/源权重 plumbing）、每库校准阈值（Cohere 法需每库 30-50 query 跑分）。这些是"调参"层，需 harness 数据，单独迭代。

## 设计
- flag `features.rerank_hardening.enabled`（biz/rag）→ Options.RerankHardening（retrieve 包不读 viper，biz wiring 注入）。**关=与现状逐位一致（零回归）**。
- **passage 清洗** `cleanPassageForRerank(content) string`（纯函数）：剥 markdown 标题/强调/链接/图片、表格 `|`、`$$math$$`、折叠空白。仅清洗喂给 reranker 的 `documents[i]`，**返回的 chunk.Content 不变**。hardening 开才清洗。
- **MMR-lite 去重** `dedupDiverse(chunks, simThreshold) []chunk`（纯函数）：按 rerank 顺序贪心保留，若某块与已保留块的 trigram Jaccard 相似度 > 阈值(0.82) 则丢弃（去雷同）。hardening 开、applyRerankFilter 之后应用。
- **降级链**（applyRerankFilter，hardening 开 + !NoFloor）：阈值过滤后若仅剩兜底/为空 → 用 threshold×0.7 重过滤回收边缘相关块；仍空 → top-1 floor **仅当 top1.score ≥ 0.15**（否则返回空，不 ground 在 <0.15 垃圾上）。NoFloor 模式不变（空=不 grounding，故意）。

## S3 Tasks
- **T1**：flag 常量 + Options.RerankHardening + biz wiring（chatbot/salesrag main 传 flag）+ config_dev 开。零行为变化（flag 关）。
- **T2**：cleanPassageForRerank + trigramJaccard + dedupDiverse 纯函数 + 单测。
- **T3**：rerankWithLimit 接 passage 清洗（hardening）+ dedupDiverse；applyRerankFilter 接 ×0.7 重试 + 0.15 floor（hardening）；单测（清洗后内容喂 rerank/Content 不变、去重剔雷同、×0.7 回收、0.15 floor 拒垃圾）。

## S5 验证策略（Rule 10）
- 后端 dev：rag-eval/retrieve A/B（hardening flag 通过 Options 暂不可由端点控——可加端点参数或临时 config 切换）。重点：①清洗不破坏正常 rerank（命中不变）②雷同块被去重 ③低相关 query top-1 不再硬塞 <0.15 垃圾。
- 回归保护：纯函数单测（清洗/去重/降级）持久化。flag 关时现有 rerank 测试全绿（零回归）。
- 注：rerank 质量是统计指标 + dev 免费档 reranker 限流不稳 → 跑前探活、趋势看不抠点。

## prod 安全
flag 默认 OFF=现状；推迟的复合分/校准不影响本期；不上 prod。
