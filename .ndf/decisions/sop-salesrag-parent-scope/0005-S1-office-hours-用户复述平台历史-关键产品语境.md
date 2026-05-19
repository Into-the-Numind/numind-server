# S1 office-hours 用户复述平台历史 — 关键产品语境

**Date:** 2026-05-18

**Feature:** `sop-salesrag-parent-scope`

**Migrated from:** `build-manifest.yaml` decisions[]

---

起点是 3 个硬编码内置功能（2 SOP + 1 销售智能体），后来加 self-service config 让客户自助搭建，分 2 类资源（SOP / chatbot），3 个内置功能被回头分类——2 SOP 入 SOP 资源池，销售智能体入 chatbot 资源池。销售智能体外观是 chatbot 卡片，但物理上独立存储（独立 sales_message/sales_session/路由/知识库机制）。统一规则：不管 SOP 还是 chatbot（含特殊 chatbot），谁创建/拥有，仅其和子账户可见。规则统一，仅 owner 字段落点不同。
