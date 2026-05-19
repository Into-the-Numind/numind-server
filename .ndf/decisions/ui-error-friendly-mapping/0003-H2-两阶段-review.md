# H2 两阶段 review

**Date:** 2026-05-15

**Feature:** `ui-error-friendly-mapping`

**Migrated from:** `build-manifest.yaml` decisions[]

---

Spec compliance reviewer + Code quality reviewer 都 PASS_WITH_CONCERNS。共识 1 P1：缺 chatbot/salesrag client-disconnect 检测 → 修。共识 1 P1：salesrag 3 处 Analyze SSE 漏改 → 修（同一文件同模式，扩展到 9 处 SSE site）。P2：translate_test.go 双层 wrap + SetMessage 路径补测（已加 2 测）+ frontend errorMessage 缺单测（已加 17 测）。0 P0。NDF Rule 7 按“能现在修则现在修”全部修完。
