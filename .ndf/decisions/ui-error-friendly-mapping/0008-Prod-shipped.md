# Prod shipped

**Date:** 2026-05-15

**Feature:** `ui-error-friendly-mapping`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户打 prod tag — numind-server v2.1.19（commit 319ecc7，含本 feature + CI fail-fast + polling health check 等改进）+ numind-web-v3 v1.0.20（commit d0e6876，含本 feature 所有前端改动 + 5 条 H3 follow-up：toast wording / chat-error-toast-unify / salesrag-403-no-logout / sub-user-multi-select / .dockerignore + _work_* gitignore）。Prod CI deploy success，两仓容器 healthy。stage H3 → completed。
