# Triage

**Date:** 2026-05-13

**Feature:** `sop-chatbot-visibility-scope`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户描述'SOP/chatbot 增加显示权限'。AI 发现已存在 child-run-permission（运行权限 / 用户视角 / deny-all）。用户确认两套语义并存：本需求 = 可见范围（列表过滤 / 实体视角 / allow-all 默认）；现有 = 运行权限（403 gate / 用户视角 / deny-all）。两层 gate 串行：可见范围 → 运行权限。
