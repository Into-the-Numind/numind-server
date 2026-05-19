# S3 plan written

**Date:** 2026-05-13

**Feature:** `chatbot-session-rename-pin`

**Migrated from:** `build-manifest.yaml` decisions[]

---

8 task plan (T1-T7 实施 + T8 验证策略 NDF Rule 10 强制)。后端 T1-T4 (migration/store/biz/controller+router) 先于前端 T5-T7 (types+api/store+Vitest/ChatbotChat.vue)。S2 holistic reviewer 的 2 P1 指引已写入 plan §0：fetchSessions breaking change + types 前置依赖。预估工作量 CC+gstack 3.5h / 人类 2d；含 S4 review 后 wall time 6-9h
