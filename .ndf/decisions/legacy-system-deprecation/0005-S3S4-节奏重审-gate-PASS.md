# S3→S4 节奏重审 + gate PASS

**Date:** 2026-05-18

**Feature:** `legacy-system-deprecation`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户质疑 4 周时间线。重审：prod 0 用户在 legacy 路径，T1-T3 全是死代码删除，原 1 周 soak buffer 风险评估偏保守。用户拍板 1 天 all-in 节奏。Plan 各 task gate 改为 smoke verify (10-30min)；spec §8.1 同步更新；T4 仍保留 backup + dry-run 仪式（唯一不可逆步骤）。
