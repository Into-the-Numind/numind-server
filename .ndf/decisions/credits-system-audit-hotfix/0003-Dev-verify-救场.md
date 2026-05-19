# Dev verify 救场

**Date:** 2026-05-18

**Feature:** `credits-system-audit-hotfix`

**Migrated from:** `build-manifest.yaml` decisions[]

---

push develop 后 dev container 健康但 sweeper started log 没出现。深查发现 Agent 2 把 sweeper 注册到 server.go 而 server.go 是 dead code，真入口是 numind.go。补 commit 1cba35b 移到正确位置。如果没 dev-first 验证直接 tag prod，P0-2 修复就完全无效。
