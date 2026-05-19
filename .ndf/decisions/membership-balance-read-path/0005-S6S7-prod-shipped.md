# S6/S7 prod shipped

**Date:** 2026-05-14

**Feature:** `membership-balance-read-path`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户打 prod tag v2.1.17（numind-server）+ 53268da (numind-web-v3)。Prod CI deploy 一次 success。和 credits-deduct-cycke-wiring 同期上 prod（同一组 commits 涵盖两个 feature）。real-traffic 验证：prod 用户能正常登录 + healthz 200 + 真实 SOP/chatbot 调用通过（在后续 credits-deduct-cycle-wiring 实测中观察到调用栈走到正确路径）。stage 标 completed。
