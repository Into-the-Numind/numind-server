# S6 UI 一致性 fix iteration round 1

**Date:** 2026-05-15

**Feature:** `chatbot-session-rename-pin`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户 dev 人工验收反馈 3 件事 — (1) chatbot 图标改 lucide 匹配 salesrag 但保持 chatbot 置顶视觉 (2) chatbot 三点菜单下展列表 UI 完全匹配 salesrag (3) salesrag 置顶效果改为 chatbot 一样. Implementer (commit 23514dc): Fix A chatbot 三点按钮 ⋯ → <MoreVertical> lucide / Fix B 菜单 dropdown UI 加 Edit3/Pin/PinOff/Trash2 图标 + CSS 类名重命名为 salesrag 同款 (.session-menu-btn/-container/-dropdown/-item/.danger) + CSS 规则复制 salesrag / Fix C 保持 chatbot 现有 .session-item--pinned 左 2px 边框 + 📌 emoji (用户 '保持现在置顶效果') / Fix D salesrag SessionSidebar 移植 chatbot 视觉 (加左边框 + 📌, 删 Pin v-if/MessageSquare v-else 条件渲染 改为 MessageSquare 始终).
