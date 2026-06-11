# F3 Handoff：清洗 chunk 内容污染 + 重新切块入库（给新 session）

> 复制下面整段到一个新的 Claude Code session（cwd = `numind-server`）即可开始。
> 这是 `retrieval-base` feature 的 Phase 3 后续——前面 P1/P2 + F1/F2 已合并 develop 并部署 dev。

---

## 任务

修复知识库 chunk 的**内容污染**：chunk 正文里混进了 `## V013 ·` 这种**源文档章节标记/版本码** + `[上下文衔接]` 这种**切块重叠标记**。这些被一起 embed 进向量、也被喂给 LLM，导致：
1. 检索相关度被噪声拉偏；
2. LLM 把 `V013-V022`、`[上下文衔接]` 复述进回答，用户看不懂（实测 chatbot "AI 时代的创业第一课" 出现"检索资料 V013-V022"）。

## 根因（已用 Langfuse 实证）

- `internal/pkg/retrieval/ingest/enhanced_splitter.go:505 / :524`：切块重叠逻辑把字面量 `"\n\n[上下文衔接]\n\n"` 拼进 `chunk.Content`（同时用 `CoreStart/CoreEnd` 记录"真实内容"边界）。**重叠是为给邻居上下文，但不该把字面标记塞进被 embed/喂 LLM 的正文。**
- 源文档（如《创始人手册》）的 markdown 有 `## V013 ·`、`## V020 ·` 这种带版本码的章节头；切块保留了它们，且常切出"## V013 ·"后面是空标题的孤儿头。
- 这些 chunk 是**用污染内容 embed 进向量库的**，所以光改切块器不够——**必须对受影响文档重新切块 + 重新嵌入**（re-ingest）。

## 实证方式（务必先看真实数据再动手）

Langfuse（dev）抓了完整 trace，能看到喂给 LLM 的 chunk 原文：
```
PK="pk-lf-41015256-bdc3-46aa-92b7-6ffe8de9605a"
SK="sk-lf-5a6c2185-d8ce-4903-9930-c9d72a9d0112"
LF="http://110.42.221.25:3100"
# 查 chatbot-chat traces：
curl -sf -u "$PK:$SK" "$LF/api/public/traces?name=chatbot-chat&limit=5&orderBy=timestamp.desc"
# 取某 trace 的 observations，看 GENERATION(name=chatbot.stream) 的 input.context_fragments：
curl -sf -u "$PK:$SK" "$LF/api/public/observations?traceId=<TID>&limit=20"
```
dev 测试目标：chatbot "AI 时代的创业第一课"（`http://49.233.219.254:9200/chatbot/10`，KB=《创始人手册》），登录 admin/admin123456（gstack browse 自动化），问"创业要经过哪几个阶段"复测。

## 修复范围（建议 Standard 轨道，因为要重新嵌入全部受影响 chunk）

1. **切块器去污染**（`internal/pkg/retrieval/ingest/`）：
   - 不把 `[上下文衔接]` 字面量塞进 `chunk.Content`。重叠上下文要么放 chunk metadata（`CoreStart/CoreEnd` 已有边界信息可复用），要么 embed/render 前 strip 掉。
   - 处理源文档章节头：去掉 `V0XX` 版本码噪声（保留章节语义/标题），避免孤儿头 `## V013 ·`（空标题）。
2. **（可选，提案 Phase 3 一并做）**：chunk 从现在的大块（规则兜底 6000 字符）降到 ~512 字符；抄 WeKnora 的 `ContextHeader`——把 `# 章 > ## 节` 面包屑**嵌入前**拼到正文前但**不持久化**（治孤立子块丢上下文）。见 `proposals/retrieval-base-unification-proposal.md` §5 Tier3。
3. **重新 ingest 受影响文档**：
   - 先查清重新处理入口——`internal/pkg/retrieval/ingest/pipeline.go` 是入库链；salesrag/admin 是否有"重新解析/重新嵌入文档"的 controller/endpoint？没有就要加一个 admin 触发的 re-ingest（删旧 chunk → 重切 → 重嵌 → 三处存储 MySQL `knowledge_chunk` / sqlite `chunks` / `vec_chunks` 同步刷新）。
   - 至少对 dev 上 chatbot 10 的 KB《创始人手册》文档重切重嵌验证。
   - **维度不变（2048），不用重建 vec_chunks 虚拟表**。

## 必须遵守

- **NDF 流程**：先 `bash numind-server/scripts/ndf/ndf-status.sh` 看活跃 feature；新需求做档位判定（这个建议 Standard）；`ndf-start standard <slug>` 起 worktree，禁止手动 `git checkout -b`；每阶段产出后双 Sonnet reviewer；完成 `ndf-done`（worktree 内跑）。详见 `numind-server/CLAUDE.md` + `.claude/skills/ndf-workflow.md`。
- **salesrag 逐位一致**：salesrag 检索走同一底座，改切块/重嵌前后必须保 `go test -tags=integration -run TestRetrievalGolden ./internal/numind/biz/salesrag/service/...` 仍 PASS（但注意：重切重嵌会改变 chunk_id/内容，golden 的"逐位一致"是针对检索**逻辑**不变；切块改动属于有意的内容变更，需重新评估 golden 基线或用新的质量评估）。
- **aiservice 统一入口**：embed 走 `aiservice.Embed(profile.SalesragEmbed, dim 2048)`，禁止裸调 provider。
- 部署：`/deploy-dev server` → gstack `/qa` 在 dev 复验（别碰 prod）。

## 背景文档（都在 develop 上）

- 提案：`proposals/retrieval-base-unification-proposal.md`（完整愿景，Phase 3 = 本任务 + 小块 + 面包屑）
- 设计：`.ndf/features/retrieval-base/spec.md`（§3.1 chunk 改造）
- 计划：`.ndf/features/retrieval-base/plan.md`
- manifest：`.ndf/manifest.yaml`（id: retrieval-base 的 decisions 有全程记录）

## 已知现状（前序 feature 已完成，别重做）

- 底座 `internal/pkg/retrieval/`（domain/port/ingest/retrieve/adapter）已立，四通道（chatbot/salesrag/agent 已接，SOP 留口）共用 `retrieve.Service.Retrieve(query, Scope, Options)`。
- chatbot grounding（"仅依据资料/不得编造"+`[知识N](相关度%)`+引用解析）已加；F1 已让 chatbot 用原话检索（停销售改写）；F2 已加 `Options.RerankMinScore/RerankNoFloor`（chatbot 0.6+NoFloor，低相关度全丢→纯聊天）。
- **还没做的就是本任务（F3 chunk 内容清洗 + 重切重嵌）+ 一个独立小 bug：`internal/numind/biz/salesrag/service/strategy_service.go:43` 硬编码绝对路径致销售策略静默失效。**

---
*生成于 retrieval-base 部署 dev 后的实测诊断（2026-06-11）。*
