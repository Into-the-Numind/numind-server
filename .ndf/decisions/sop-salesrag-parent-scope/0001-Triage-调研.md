# Triage 调研

**Date:** 2026-05-18

**Feature:** `sop-salesrag-parent-scope`

**Migrated from:** `build-manifest.yaml` decisions[]

---

prod 实测确认 (a) sop_template 4 行：id=1,2 creator_user_id=NULL 已发布；id=3 草稿归 30；id=4 已发布归 30 (b) chatbot_config 列表按 user_id 过滤正常工作（admin 看不到 user 30 的 chatbot），SOP 列表无此过滤是 bug (c) user_feature_permission 48 行 sales_agent 子账户授权（parent=30 或 1），数据形态不变 (d) parent_user 仅 2 个：admin(1) + user_moxiaopai(30)
