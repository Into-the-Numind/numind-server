# H3 toast polish 跟随

**Date:** 2026-05-15

**Feature:** `ui-error-friendly-mapping`

**Migrated from:** `build-manifest.yaml` decisions[]

---

用户 dev 验收时反馈红色 ✕ error toast + 长文案 '请购买积分包或联系管理员' 像系统崩溃。产出 _work_toast_showcase.html 4 方案对比，用户选 B。后端 3 处硬编码 → '积分不足，请充值积分'（commit c7a3f95，fix/credit-toast-wording）；前端 SOPRunView.vue:440 notifications.error → notifications.warning + 去 '创建 run 失败：' 前缀（commit 5e5af03，fix/credit-toast-wording）。两仓 push 一并触发 dev CI 重部署。
