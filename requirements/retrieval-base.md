# 知识库统一检索底座（Retrieval Base）— 第一批

> 完整愿景与可行性见 `proposals/retrieval-base-unification-proposal.md`。本卡仅覆盖**第一个 feature 的范围**（提案的 P0+P1+P2）。

## 来源
- 提出人：用户（创始人）
- 提出日期：2026-06-10
- 触发：用户反馈"chatbot 挂知识库后回答怪怪的" → 审计发现 chatbot RAG 是 salesrag 的残血复制 → 用户拍板要把知识库做成四通道（SOP/chatbot/salesrag/agent）共用的统一检索底座。

## 需求描述
把检索能力从 salesrag 包抽成领域无关的通用底座（`internal/pkg/retrieval`），四通道共用**一套"查"**（query 改写→混合检索→重排→打分片段），**"答"各通道自己拼**。本批先做地基 + 验证两个消费方：

- **P0 评估 harness**：Recall@k / MRR（标注 30-50 题），作为后续重构的安全网与回归保护。
- **P1 抽底座骨架**：迁通用 `domain`（KnowledgeChunk/KnowledgeDocument/SearchFilter）+ `port`（vector_store/parser/tagger）+ `Retrieve()→[]chunk` 出口 + ingest 链；先不动 salesrag 的 strategy/opinion/prompt。
- **P2 迁 chatbot + agent**：chatbot 删裸检索改调底座 + 补 grounding/重排/query 改写 + importance 按 score；agent `kb_search` 改调底座原始片段出口（修双重 LLM）。

## 业务目标
- 根治 chatbot"回答怪"（grounding 缺失 + 无重排 + 原话检索）。
- 消除四通道检索逻辑重复，后续 RAG 提升一次做进底座、四通道受益。
- 建立可量化的检索质量基线（此前完全无评估）。

## 优先级
高

## Triage
- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：否（本批不改表；混合检索/重嵌在后续 feature）
  2. 新增 API 端点：否（复用现有入口；底座是内部包）
  3. 新外部服务集成：否（复用 aiservice/向量库）
  4. 影响文件数：**>3**（新建 `internal/pkg/retrieval` 包 + 拆 `salesrag/domain` 跨包改名 chatbot/agent/knowledgebase + 迁 ingest/retrieve + 评估）
  5. 高风险业务逻辑：**是**（触及 chatbot/agent 用户可见回答路径；为后续 salesrag 收入路径收口打地基）
- 人类决定：**确认 Standard**（用户 2026-06-10 拍板"正式开始"）

## 已定关键决策（2026-06-10）
1. **scope 模型**：底座契约=必须有明确 scope；"全部启用文档"作显式可选（给 agent）。
   - **chatbot/salesrag 没选时提示用户去选知识库**（不静默翻全部/不给默认 KB/不返回空答案）。
   - **agent 空 scope（`kb_search` 不传 doc_ids）默认解析为"全部启用文档"**——既符合 agent 自主特性，也顺手修掉 `kb_search` 空结果隐患（提案 §9.2）。S1 扫 `tool_kb_search.go` 确认落点后定稿。
2. **SOP 本批不接**（留到后续 feature；本批只确保底座接口可被 SOP 复用）。
3. **节奏**：本批 = P0+P1+P2；salesrag 收口（P5）放最后单独 feature。
4. **架构**：复用全局入口 + 各通道保留自己的"答"，**不合并四路**。

## 验收口径（S5 验证策略将在 S3 细化）
- P0：评估 harness 可跑、产出改前基线 Recall@k/MRR。**标注数据方案**：从 salesrag 会话 / agent_run 历史对话采样 30-50 条真实 query → AI 辅助生成 expected chunk_ids 初稿 → 创始人复核确认（标注是 P0 主要成本，依赖创始人复核，见 §备注依赖）。
- P1：底座包编译通过、`go test ./...` 绿。**P1 是纯代码搬迁、不改任何检索算法 → 验收口径为"精确逐位一致"**：对相同 query，改前/改后 top-K `chunk_id` 集合**完全相同**（fixture 精确比对，不是统计近似）；P0 评估 Recall@k/MRR 作为附加 sanity check 不退化。
- P2：chatbot 经底座检索，回答带 grounding + 来源标注；评估指标 vs 改前**不退化、目标提升**（P2 引入重排/改写=算法变更，故为统计口径而非逐位）；agent `kb_search` 返回原始片段不再双重 LLM。

## 备注
- **依赖（创始人）**：P0 评估标注集需创始人复核 30-50 条 `query → expected chunk_ids`（AI 出初稿）。这是 P0 的关键人工依赖，工期受其影响。
- **S1 前置确认项**：`grep -r "salesrag/domain"` 扫出拆包跨包改名涉及的具体文件数（影响 P1 task 拆分粒度，提案估"chatbot/agent/knowledgebase 三处"但未给文件数）。
- 独立待办（不在本批）：`service/strategy_service.go:43` 硬编码绝对路径致销售策略静默失效 → 单独排查（用户 2026-06-10 确认）。
- 已并入决策 1：agent `kb_search` 不传 doc_ids 检索空的隐患（提案 §9.2）→ 本批随 agent 迁移修复（空 scope 默认"全部启用文档"）。
