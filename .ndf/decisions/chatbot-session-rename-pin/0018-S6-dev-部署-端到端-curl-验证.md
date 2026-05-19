# S6 dev 部署 + 端到端 curl 验证

**Date:** 2026-05-15

**Feature:** `chatbot-session-rename-pin`

**Migrated from:** `build-manifest.yaml` decisions[]

---

merge feature → develop 两仓 push 成功 (numind-server commit 04a3298 / numind-web-v3 commit b6d5732). CI 两仓全 success (run 25899997071 + 25900074235). Backend container 重启 + dev backend curl 端到端 8 paths PASS: Path 1 rename happy (200) / Path 2 空白校验 (errno '标题不能为空') / Path 3-5 pin/unpin/重排 / Path 6 D2 不变量在真实 MySQL 上 PASS (updated_at 在 rename+pin+unpin 4 操作后保持原值 2026-05-14T03:55:59 — P0 fix 最强证据) / Path 8 chatbot_id query param filter 生效 / Path 9 未登录 401 / Path 10 单元测试覆盖跳过 curl; EC-14 重复置顶 pinned_at_2 > pinned_at_1 PASS (2.1s 间隔)。Playwright E2E spec 已补写 numind-web-v3/e2e/chatbot-session-rename-pin.spec.ts (mock-based 8 paths). Dev 数据已恢复 (session 52 title='会议记录沉淀' pinned_at=null updated_at=2026-05-14T03:55:59 不变).
