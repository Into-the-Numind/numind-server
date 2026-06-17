# 提案 + PRD：semantic-chunk-reliability

> S1 · 2026-06-17

## 1. 产品级思考

真问题不是"强制用语义",而是"**让用户每次上传都成功且检索效果好,同时我们能看见后台到底发生了什么**"。所以方案的灵魂是**可观测先行**:先让"走了语义还是兜底"变得可测可查(这是当前最大的盲区),再在此之上保证"永不失败 + 兜底也好 + 语义更稳"。

为什么可观测先行:现在连"到底多少比例在兜底""为什么兜底"都测不出来——所有"修复"都是盲修。先点亮仪表盘,真实数据自然指出要不要更深的修。

## 2. 技术可行性

- **留痕**:`HybridSplitter.SplitWithDetails` 已返回 `details["strategy"]`(semantic/rule/rule_fallback)+ reason。把它沿 `CompatibilitySplitter`→`SplitterAdapter` 暴露出来,`pipeline` 改调它,strategy+reason 持久化到 `knowledge_document` 新列 + 兜底时打 WARN 日志。`knowledge_document` 是普通 GORM model(`model/knowledge_document.go`),加列 + migration 即可。
- **永不失败**:`HybridSplitter.Split` 在语义出错时本就自动兜底(`hybrid_splitter.go:91-95`),`pipeline` 仅在 `chunks==0` 或 splitter 返回 err 时 fail——而兜底路径不返回 err、非空文本不会 0 块。所以上传已不会因语义挂而失败;本 feature **确认 + 加回归测试锁死这个不变式**。
- **好兜底**:规则兜底 `MaxChunkSize 6000`→降到 ~1500-1800,贴近语义档(500-2000),保留 jieba 分词 + markdown 分级。
- **语义可靠性**:① `EmbeddingSplitter.Split` 调 `/split` 瞬时失败(超时/5xx)先重试 1 次再兜底;② `HybridSplitter` 当前只在"曾不可用"时重探健康,改成**周期性重探**(语义崩溃恢复后能重新启用,无需重启容器)。

风险:动入库核心,任何改动必须保证"上传永不失败"不变式 + salesrag 检索零回归。

## 3. 工作量

约 4 个 task + 1 个 migration + 测试,单仓库,一个 session 量级。

## 4. PRD

### 4.1 功能
知识库入库切块:可观测(留痕 strategy+reason)+ 永不失败 + 好兜底 + 语义更稳。

### 4.2 涉及仓库
numind-server。无前端。

### 4.3 验收标准
- **AC1 留痕**:入库后 `knowledge_document.split_strategy ∈ {semantic, rule_fallback, no_split}` + `split_detail`(原因/比例);兜底有 WARN 日志。可 SQL 查询"哪些文档走了兜底"。
- **AC2 永不失败**:语义服务整段不可用时,上传仍 COMPLETED、chunk 非空、strategy=rule_fallback。回归测试覆盖。
- **AC3 好兜底**:规则兜底块上限降到 ~1500-1800,产出块大小合理。
- **AC4 可靠性**:`/split` 瞬时失败重试 1 次;语义恢复后周期重探自动重新启用。
- **AC5 语义优先**:语义可用时 strategy=semantic（dev 实跑确认）。
- **AC6 可量测**:dev 留痕能统计真实兜底率。

### 4.4 trace topology
切块不走 LLM(语义是本地 bge 服务,embed/tagging 才是 LLM)。本 feature 的"可观测"是**业务留痕(DB + 日志)**,非 Langfuse generation。embed/tagging 的既有计费/trace 不动。

## 5. 客户确认
用户已授权:按 UX 原则(永不失败>语义优先>好兜底+留痕)重排范围,自主跑到 deploy dev(prod 不碰,最终和 strategy_service 修复一起上 prod)。
