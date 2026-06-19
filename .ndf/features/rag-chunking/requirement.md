# S0 需求卡 — rag-chunking（结构感知重切块 + 父子块 + 预览工具 + 灰度重灌）

> RAG 升级总计划项 1（地基）。计划文档：`docs/plans/2026-06-20-rag-upgrade-master-plan.md`。
> Track: Standard。仅 numind-server。全程 dev 验证，不碰 prod 在线数据、不改 config_prod.yaml。

## 问题（实测证据）
- 销售助手 salesrag 对每条 query 都检索（产品/案例/FAQ/观点）做 grounding，但**含答案的块捞不出来**：事实 lookup 检索可答性仅 10-15%，grounding-usefulness 旧切块 24%。
- 根因 = 切块坏：生产 `CompatibilitySplitter`（Semantic max=2000 / Rule max=1800 字符）产出无结构感知的"大杂烩块"——FAQ 切成无答案的问题串、68 条观点塞进 5 块、案例/产品揉一起；**embedding 文本不含标题面包屑**（headers 只进 tags）。
- 实测 A/B（user 348，结构感知小块重切）：grounding-usefulness 24%→**40%（+16pp，稳健）**。这是全工程最高 ROI 的第一杠杆。

## 目标
把"大杂烩巨块"换成**结构感知的聚焦小块（~目标 400-512 字）**，并在 embedding 文本注入**标题面包屑**，让"含答案的块"能被检索命中。dev 上复现 ~+16pp grounding-usefulness、无大面积回归。

## 验收标准（S5）
1. 新切块器对 FAQ/观点/案例/产品文档分别按问答对/单条观点/单案例/按节切，产出块数显著增多、单块聚焦（avg 字数显著下降，无 >1500 字大块、无大量 <80 字碎块）。
2. embedding 文本含 `# 顶 > ## 节` 面包屑前缀；返回给 LLM / 展示的 `Content` 保持干净（不含面包屑、不含 `[上下文衔接]` 标记）。
3. 预览端点 `POST /v1/admin/chunker/preview` 可对任意文本/已存在文档返回将被切成的块 + 选中的策略档 + 拒绝其它档的原因。
4. 可对 dev 上 user 348 的文档重切重嵌（reindex 路径），用 §4 grounding-usefulness harness A/B（旧 vs 新）复现 ~+16pp，且事实 lookup 桶不回归。
5. flag 默认关 → prod 行为零变化；flag 开 → 走新切块器。`task lint` + `go test ./...` 全绿。

## 非目标 / 边界
- 不做混合检索 BM25/RRF（项 2）、不做重排硬化（项 3）、不做拒答兜底（项 5）、不做 doc2query（项 4）。
- 父子块（child 384 / parent 2048）：见 design.md 的范围决策——本期实现结构感知小块 + 面包屑（已验证的 +16pp 来源），父子块作为后续增量，理由是结构感知切块本身使块成为自包含语义单元（FAQ 问答对/单观点/单案例天然有界），父块扩展边际收益低、且改 schema 风险高。
- 不在 prod 上线（独立 user-gated 走）。
