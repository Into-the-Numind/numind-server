# 用户产品决策 Option A

**Date:** 2026-05-19

**Feature:** `salesrag-embed-dim-2048`

**Migrated from:** `build-manifest.yaml` decisions[]

---

现有 90 份 COMPLETED docs 中 76 份属于 user_moxiaopai(30) + 11 个 B2B 子账户的真实客户数据，决定保留所有 2048 维向量，把代码切回 2048。备选 Option B（重建 collection 1024 + backfill 537 chunk）和 Option C（重建 + 让客户重传）被否。
