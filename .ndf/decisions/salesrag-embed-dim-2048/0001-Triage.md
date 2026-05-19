# Triage

**Date:** 2026-05-19

**Feature:** `salesrag-embed-dim-2048`

**Migrated from:** `build-manifest.yaml` decisions[]

---

prod bug id=112 user_id=1 (admin) 上传 PDF 失败，error_msg = 'vector storage failed: ... Expected 2048 received 1024'。time-to-root-cause 约 15 分钟（SSH prod query error_msg → grep profile.SalesragEmbed → 比对 task_profile DB 与 collection schema）。
