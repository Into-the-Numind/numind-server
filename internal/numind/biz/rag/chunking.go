package rag

// FlagStructureAwareChunking 控制 salesrag ingest 是否使用结构感知切块器
// （StructureAwareSplitter：FAQ→问答对 / 观点→单条 / 案例→单案例 / 通用→按节小块
// + 标题面包屑注入 EmbedText）。关（默认/缺省）→ 走现状 CompatibilitySplitter，
// prod 行为零变化。dev 开启验证。
//
// 注意：本 flag 仅影响**新入库/重灌**的文档切块，不会改写已入库的旧 chunk；
// 切换后需对目标文档走 reindex 才生效（见 chunker reindex 端点）。
const FlagStructureAwareChunking = "features.structure_aware_chunking.enabled"
