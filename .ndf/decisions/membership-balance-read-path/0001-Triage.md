# Triage

**Date:** 2026-05-14

**Feature:** `membership-balance-read-path`

**Migrated from:** `build-manifest.yaml` decisions[]

---

P0 prod bug — user 427 granted 2-month sub via B2B2C but shows free/no credits. Root cause: write path (subscription table) vs read path (credit_package table) mismatch. Standard track, accelerated.
