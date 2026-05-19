# Agent teams 7 个并行

**Date:** 2026-05-18

**Feature:** `credits-system-audit-hotfix`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户拍板调度 7 个 subagent 按 file-scope 并行（A1 grant_membership / A2 sweeper / A3 credit_service Reconcile / A4 cycle.go / A5 backend cleanup / A6 payment idempotency / A7 frontends），主会话 review+集成+部署。每 agent 强制 task test + task lint。
