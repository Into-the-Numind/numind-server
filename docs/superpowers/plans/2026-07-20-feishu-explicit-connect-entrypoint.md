# Feishu Explicit Connect Entrypoint Implementation Plan

1. Preserve RED commits in server and web reproducing the missing Agent tool and inert settings CTA.
2. Add `ConnectionOnly` encrypted operation semantics and `FeishuOperationService.Connect`, including disconnected yield, already-connected success, idempotency and no-runner tests.
3. Add `lark_connect`, inject/register it atomically with the controlled Lark tool set, update policy, metadata, manifests and tool contracts.
4. Wire settings CTA to `store.connect`, complete the exact manual session, resume operation-bound connect actions, restore URL-free actions, and guard loading/errors/repeated clicks.
5. Fence Settings, explicit-connect, and business-recovery entries to one account-generation bootstrap owner, with bounded stale-owner takeover after restart.
6. Turn focused tests GREEN; add Playwright coverage proving start and exact-session completion stay on Settings.
7. Run dual review, Go tests/race/lint, Vue unit/lint/type-check and Feishu Playwright.
8. Merge both worktrees through `ndf-done`, deploy exact develop revisions to Dev, then verify health and the explicit-connect acceptance path. Production remains untouched.
