# S4 Task 8 validation strategy doc

**Date:** 2026-05-14

**Feature:** `chatbot-session-rename-pin`

**Migrated from:** `build-manifest.yaml` decisions[]

---

plan §3 内容物质化为独立 .md 文件 numind-server/docs/superpowers/specs/2026-05-13-chatbot-session-rename-pin-validation-strategy.md (NDF Rule 10 强制末尾 task). 含 3 要素: 验证方式 (Playwright E2E + 后端 Go test + 前端 Vitest 三件套) / 5 条理由 (含为什么不选 gstack /qa) / 10 条关键用户路径 (含 D2 updated_at 不变量验证 + EC-14 重复置顶刷新双重验证). Reviewer Round 1 PASS_WITH_CONCERNS, 1 P1 (§4 Playwright 覆盖列表漏 #2 空白校验) 已修
