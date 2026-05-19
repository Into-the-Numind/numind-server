# H3 chat error 统一 toast

**Date:** 2026-05-15

**Feature:** `ui-error-friendly-mapping`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户反馈 chatbot 把 SSE 'error' 事件渲染成左对齐红色内联气泡（ChatbotChat.vue .stream-error，视觉上像 AI 回复消息），与 SOP/创建 run 失败的 toast 模式不统一。salesrag ChatArea.vue 有同样的 .stream-error pattern。Fix: stores/chatbot.ts + stores/sales.ts 在 SSE 'error' case 和 catch 块均改为调 notifications.warning() (橙色 ⚠ toast, 4s 自动消失) + 移除两个 view 的 .stream-error 渲染块和 CSS。streamError ref 保留作内部清理标志（sales.ts:482 preservedError 跨 resetStreamState 的 2026-04-19 incident 逻辑依赖它）。commit 7808669 fix/chat-error-toast-unify merge develop fc3063f + push。
