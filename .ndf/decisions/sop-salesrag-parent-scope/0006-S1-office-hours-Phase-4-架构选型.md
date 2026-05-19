# S1 office-hours Phase 4 架构选型

**Date:** 2026-05-18

**Feature:** `sop-salesrag-parent-scope`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户提出 Option B（建 custom_agent 通用表 + 新前端类别'自定义智能体'，3-4 天 13-15 文件）vs AI 提的 Option A（建 sales_agent_owner 小表，1 天 ~7 文件 仅 numind-server）。决定性问题：3-6 个月内是否有第 2、3 个同类'自定义智能体'计划。用户拍板 A（YAGNI 友好选择，先止血再说，未来真有 N>1 时升级 B 成本不高）。落地变更：销售智能体 owner tag 落点从 user.has_sales_agent BOOLEAN 列改为新建 sales_agent_owner(parent_user_id PK) 独立小表——理由是未来若新增同类'特殊智能体卡片'，独立表更易扩展，user 表不会被 N 个 boolean 列污染。
