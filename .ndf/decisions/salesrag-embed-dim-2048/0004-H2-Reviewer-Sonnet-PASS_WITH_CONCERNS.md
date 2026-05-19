# H2 Reviewer Sonnet PASS_WITH_CONCERNS

**Date:** 2026-05-19

**Feature:** `salesrag-embed-dim-2048`

**Migrated from:** `build-manifest.yaml` decisions[]

---

0 P0 / 1 P1（migration 漏更新 task_profile.description 文本，会误导未来排障）/ 1 P2（未来加第二个 embedding service 时 migration 不覆盖，暂无风险）。P1 就地修复 — migration UPDATE 加 description SET 子句。SalesragEmbed grep 整库唯一调用点 = biz.go:121，无遗漏路径。
