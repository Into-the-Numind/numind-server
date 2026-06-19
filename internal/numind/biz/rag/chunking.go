package rag

import "github.com/spf13/viper"

// FlagHybridRetrieval 控制检索是否启用混合检索（dense 向量 + BM25/FTS5 关键词 + RRF 融合）。
// 关（默认/缺省）→ 纯向量检索，prod 行为零变化。dev 开启验证。
//
// 注意：
//   - 仅 SQLiteVecStore 实现 KeywordSearcher（FTS5）；其它 store 即使 flag 开也自动退回纯向量。
//   - 二进制须以 -tags sqlite_fts5 构建且 fts_chunks 有数据，关键词通道才生效；否则降级纯向量。
//   - 已入库的旧 chunk 无 FTS5 数据，需 reindex/重新上传才进关键词索引（与项1重灌同批）。
const FlagHybridRetrieval = "features.hybrid_retrieval.enabled"

// HybridRetrievalEnabled 返回混合检索 feature flag 是否开启。封装 viper 读取，使调用点
// （chatbot/salesrag 检索构造）无需各自直接依赖 viper key 字面量。
func HybridRetrievalEnabled() bool {
	return viper.GetBool(FlagHybridRetrieval)
}

// FlagRerankHardening 控制重排硬化（passage 清洗去噪 + MMR-lite 去雷同 + 降级链：
// 0 结果阈值×0.7 重试 / top-1 floor 仅当 ≥0.15）。关（默认/缺省）→ 重排逐位维持现状，
// prod 零变化。dev 开启验证。复合分/每库校准阈值不在本 flag 范围（后续迭代）。
const FlagRerankHardening = "features.rerank_hardening.enabled"

// RerankHardeningEnabled 返回重排硬化 feature flag 是否开启。
func RerankHardeningEnabled() bool {
	return viper.GetBool(FlagRerankHardening)
}

// FlagStructureAwareChunking 控制 salesrag ingest 是否使用结构感知切块器
// （StructureAwareSplitter：FAQ→问答对 / 观点→单条 / 案例→单案例 / 通用→按节小块
// + 标题面包屑注入 EmbedText）。关（默认/缺省）→ 走现状 CompatibilitySplitter，
// prod 行为零变化。dev 开启验证。
//
// 注意：本 flag 仅影响**新入库/重灌**的文档切块，不会改写已入库的旧 chunk；
// 切换后需对目标文档走 reindex 才生效（见 chunker reindex 端点）。
const FlagStructureAwareChunking = "features.structure_aware_chunking.enabled"

// FlagKBFallback 控制检索为空时的"列出库里有什么"优雅兜底（RAG 升级项5）。关（默认/
// 缺省）→ 现状（空检索不注入兜底）。开 → salesrag 把库内文档清单 + 如实拒答指令填入答案
// prompt 的知识库区，让模型告知范围而非编造或死板拒答。与 answerability_gate 组合：门拒答
// 致空 → 兜底优雅处理。
const FlagKBFallback = "features.kb_fallback.enabled"

// KBFallbackEnabled 返回 KB 兜底 feature flag 是否开启。
func KBFallbackEnabled() bool {
	return viper.GetBool(FlagKBFallback)
}
