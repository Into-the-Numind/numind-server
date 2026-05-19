# Prod DB migration applied — closed

**Date:** 2026-05-19

**Feature:** `salesrag-embed-dim-2048`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户授权直接 apply migration 20260519_010000 到 prod numind-prod DB（无需等容器部署 — runtime 路由跳过 capability matching，DB 1024 不阻塞 dim=2048 写入，但元数据对齐避免未来 admin UI 误导）。BEFORE: task_profile.dim=1024 + ai_service.dim=1024。AFTER: 两张表 dim=2048 + task_profile.description 同步对齐。Prod 容器 tag v2.1.28 + push 由用户后续自行完成。Manifest 本条目 close。
