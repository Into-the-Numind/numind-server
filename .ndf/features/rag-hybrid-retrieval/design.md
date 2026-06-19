# S0-S3 — rag-hybrid-retrieval（RAG 升级项 2：混合检索 dense + BM25 + RRF）

> RAG 总计划项 2。依赖项 1（已合 develop：结构感知小块）。全程 dev 验证、不碰 prod 在线数据、不上 prod。

## S0 需求
现状 salesrag 纯 dense（sqlite-vec 暴力余弦），无关键词检索。纯 dense 会糊掉**产品码/型号/精确 FAQ 措辞/专有名词**（语义相近但字面不同的块会挤掉字面精确命中）。研究三方收敛：dense+BM25+RRF 比纯 dense +2~7%，且命中术语/编号。补关键词检索 + RRF 融合。

**验收**：A/B 纯 dense vs dense+BM25+RRF，产品码/精确措辞类 query 命中提升；普通语义 query 不回归；flag 关=纯 dense（prod 零变化）。

## S1/S2 设计（AI 拍板）

### D1 FTS5 关键词索引（jieba 预分词）
- sqlite-vec DB 加 `fts_chunks` FTS5 虚拟表：`CREATE VIRTUAL TABLE fts_chunks USING fts5(id UNINDEXED, content)`。
- **中文分词**：用 gojieba（已是依赖）在**写入与查询两端**把中文切成空格分隔 token，FTS5 默认 unicode61 tokenizer 即可对预分词文本做 BM25（研究：中文 BM25 必须 jieba，否则退化字符匹配）。
- **构建标签**：mattn/go-sqlite3 的 FTS5 需 `-tags sqlite_fts5`。改 Dockerfile（`go build -tags sqlite_fts5`）+ Taskfile + cmd 本地。**降级守卫**：store 初始化时尝试建 fts_chunks，失败（无 FTS5）→ 记日志 + 标记 keyword 不可用 → 检索自动降级纯 dense（绝不让缺 FTS5 杀检索）。

### D2 双写 / 删除
- `SQLiteVecStore.Upsert` 在写 chunks+vec 后，额外写 `fts_chunks`（id + jieba 分词后的 content）。
- `DeleteByDocumentID` 额外删 fts_chunks 对应行。
- 注意：embed 用 EmbedText（项1），但 FTS5 索引用 **Content**（关键词匹配应基于干净正文，不含面包屑噪声）。

### D3 关键词检索方法
- `SQLiteVecStore.SearchKeyword(ctx, query, filter, limit) ([]KnowledgeChunk, error)`：query jieba 分词 → `SELECT c.* , bm25(fts_chunks) AS rank FROM fts_chunks f JOIN chunks c ON c.id=f.id WHERE fts_chunks MATCH ? AND c.document_id IN (...) AND (c.user_id=? OR c.user_id=0) ORDER BY rank LIMIT ?`。Score 用 bm25 负值归一（FTS5 bm25 越小越相关）。

### D4 RRF 融合（retrieve.Service）
- 新可选接口 `port.KeywordSearcher{ SearchKeyword(...) }`。Service type-assert store 是否实现 + flag 开 → 走混合；否则纯 dense（零回归，不破坏 memory/dashvector 等 store）。
- 混合路径：dense parallelSearch + keyword search → **RRF 合并** `score = vw/(k+rank_vec) + kw/(k+rank_kw)`，默认 k=60、vec=0.7、kw=0.3（WeKnora 默认）→ 取 TopK → 进 rerank（不变）。
- **单检索器旁路**：只有一路有结果时跳过 RRF、保原始序（护 FAQ 相似度语义）。
- flag `features.hybrid_retrieval.enabled`（dev 开、prod 不配=关）。Options 加 `Hybrid bool`。

### D5 多租户
FTS5 查询 JOIN chunks 按 user_id+document_id 过滤（与 dense 同；user_id=0 系统文档穿透）。

## S3 Task 分解
- **T1**：构建标签 sqlite_fts5（Dockerfile + Taskfile + Makefile/本地）+ store 初始化建 fts_chunks + 降级守卫（FTS5 不可用→keyword 关）。验收：build 带 tag 绿；无 tag 时 store 仍可用（降级）。
- **T2**：Upsert/Delete 双写 fts_chunks（jieba 分词 content）+ SearchKeyword 方法 + 单测（写入→关键词命中产品码）。
- **T3**：port.KeywordSearcher 接口 + retrieve.Service RRF 融合 + 单检索器旁路 + flag + Options.Hybrid + 单测（RRF 排序/旁路）。biz.go chatbot/salesrag 检索接线传 Hybrid（flag 开）。
- **T4**：config_dev flag 开 + reindex 兼容（reindex 走 pipeline→Upsert→双写 fts；新 doc 自动进 FTS5）。

## S5 验证策略（Rule 10）
- 后端 dev：上传一个含产品码/精确措辞的新文档（走 live pipeline 双写 vector+FTS5）→ rag-eval/retrieve 加 hybrid 开关 A/B：纯 dense vs hybrid，对"产品码X多少钱""精确FAQ措辞"类 query 看命中。
- 注意 dev 已存在 chunks 无 fts 数据（新表）→ 须对测试文档 reindex/新上传才有 BM25 数据；用新上传文档验证（避开 COS-404 旧文档）。
- 回归保护：FTS5 store + RRF 纯函数单测持久化。

## prod 安全 / 风险
- 构建标签 sqlite_fts5 影响所有环境构建（FTS5 是标准扩展，纯增能、不改既有行为；prod 仍 flag-off）。
- 现存 chunks 无 FTS5 数据 → 全局生效需重灌（灰度，user-gated，与项1重灌同批）。
- 降级守卫确保 FTS5 缺失/空索引时检索退纯 dense，绝不杀 run。
