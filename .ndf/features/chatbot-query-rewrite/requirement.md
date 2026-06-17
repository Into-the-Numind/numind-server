# 需求卡片：chatbot-query-rewrite（RAG ③ 查询处理）

> S0 · feature id: chatbot-query-rewrite · track: standard · 2026-06-17

## 1. 一句话
给 chatbot 检索加一步 **chatbot 专属【中性陈述】查询改写**：把用户口语化提问规整成保留领域锚词的检索短语后再做向量检索，抬高"正确 chunk"的 rerank 分以恢复召回——**不降 0.6 阈值、不破坏库外拒答**。

## 2. 为什么（证据驱动）
RAG 基线（`scripts/rag_eval/BASELINE.md`）：检索本身已强（raw 库内 recall 1.0），但 prod 0.6 阈值下 3 道改写/口语题（q10/q16/q18）的正确 chunk 重排分 <0.6 被丢。④ 阈值调优结论（`scripts/rag_eval/THRESHOLD_TUNING.md`）：**不能降阈值**（会削弱未采样 hard-negative 上的防幻觉）。③ feasibility（dynamic workflow 4 改写族 live 实测）：**intent_declarative 中性陈述改写**把 in-KB recall 0.842→1.0、MRR 0.789→0.947、**oob 拒答保持 1.0**、单次改写、不动阈值。HyDE 被否决（把"写 Python"泄漏进产品手册，oob 1.0→0.667）。

## 3. 范围
- **In**：chatbot stream 检索前的中性改写步（`rewriteQueryForRetrieval`）+ 改写 prompt（few-shot 用胜出改写做种子）+ feature flag `features.chatbot_query_rewrite.enabled`（默认 OFF）+ 失败/超时回退原话 + 改写后空召回→原话重试（anchor-drop 安全网）+ Langfuse span + 单测（fallback 守卫）。
- **Out**：HyDE/多查询（feasibility 已否决/不值）；阈值改动（④ 已定不改）；专用 task_profile（复用 salesrag.intent 路由，dedicated profile 列为后续 tech debt）；hard-negative golden 扩集（独立后续）。

## 4. Triage：Standard
触碰生产 chatbot 检索路径 + 新增每轮一次 LLM 改写调用（延迟/计费面）+ 新 prompt + 跨 stream.go/新文件/config/测试。无 DB schema、无新外部服务、无新 API 端点。flag 默认 OFF → prod 零影响直到显式开启。

## 5. 验收
部署 dev（flag ON）后，用评估 harness 思路复跑：chatbot 实际改写 q10/q16/q18 应使检索召回正确文档（recall→1.0），且库外题（天气/代码/闲聊）仍被拒答（oob 1.0）。改写非确定性 regress（anchor-drop）由低温 + few-shot + 空召回回退原话三重兜底；改 prompt 必重测 oob。
