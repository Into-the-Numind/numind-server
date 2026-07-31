# agent-thinking-overflow H1/H2 Verification

Date: 2026-07-31T19:30:59+0800
Track: hotfix
Repo: numind-web-v3

## Diagnosis

Playwright reproduced the Agent frontend bug with a mocked SSE stream that appends 180 `reasoning_delta` chunks to one live thinking block.

Before the fix, `.thinking-content` had `clientHeight=4000`, `scrollHeight=5051`, `scrollTop=0`, and the final paragraph was clipped by `overflow:hidden`. The Agent run continued, but the latest thinking text stayed below the visible area until another timeline update changed the surrounding layout.

## Fix

Limit the scrollable thinking viewport only for Agent-mode `ThinkingBlock` usage (`autoCollapse=true`) and auto-scroll live, unfinished thinking content to the tail whenever reasoning content grows. Sales/chatbot expanded thinking blocks keep their previous layout behavior.

Commits:

- `51887ca test(qa): reproduce agent thinking overflow`
- `89794f7 fix(agent): keep long thinking output visible`

## Verification

- `npm run lint && npm run type-check` passed.
- `npm run test:e2e -- --project=mocked e2e/agent-streaming.spec.ts` passed with 10 passed and 1 existing known-flaky skip.
- Focused regression after the fix reported `clientHeight=346`, `scrollHeight=5051`, `scrollTop=4705`, and `tailClipped=false`.
- Dev deployment completed for `numind-web-v3` image `develop-340c772` with registry digest `sha256:460c063c984d48018b1f8c27d7b8188e62be80499a936b8fe0a58bda72c9aad7`; public `/health` returned `healthy`.

## Decisions

- Keep the behavior scoped to `autoCollapse` Agent usage to avoid changing existing sales/chatbot transcript reading layout.
- Preserve the regression as Playwright E2E because the bug is a runtime clipping/scroll-follow interaction, not a pure unit behavior.
