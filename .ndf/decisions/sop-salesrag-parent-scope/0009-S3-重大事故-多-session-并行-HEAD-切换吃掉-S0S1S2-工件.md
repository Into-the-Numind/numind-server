# S3 重大事故 — 多 session 并行 HEAD 切换吃掉 S0/S1/S2 工件

**Date:** 2026-05-18

**Feature:** `sop-salesrag-parent-scope`

**Migrated from:** `build-manifest.yaml` decisions[]

---

主 session 在 numind-server 上工作，但其他并行 AI session（credits-audit 修复）通过 git checkout 切换 HEAD 24+ 次，把未提交的 requirement / proposal / spec 工作树文件全部吃掉。仅 plan 幸存（被 1cba35b 意外合入）+ manifest 条目（已 merge 入 develop）。memory feedback_check_head_before_commit 警告过此模式。补救：(1) 建 git worktree 在 /private/tmp/worktree-sop-salesrag-parent-scope 隔离 (2) 从对话历史重建 3 个丢失文件 (3) 修 plan 的 P0 问题 (4) 全部 commit 到 feature 分支防再丢。
