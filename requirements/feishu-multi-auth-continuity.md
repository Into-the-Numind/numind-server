# Feishu multi-authorization continuity

## Problem

A single Agent task can require multiple Feishu scopes. After the first authorization succeeds, the backend continues, but the browser can discard the next durable action, stop following updated tool narration, and show an incorrect red/green state. The resumed model can also lose successful intermediate `lark_execute` results and repeat writes. An ambiguous write currently blocks the safe read needed to verify its outcome.

## Acceptance criteria

1. A second authorization action for the same run appears without a page reload. Snapshot identity is explicit and is also compatible with the previous backend response shape during rolling deployment.
2. A snapshot action never restores an authorization URL. The client refreshes a missing one-time URL once through the existing operation-bound refresh API and keeps all session/run/operation fences.
3. In-place narration updates trigger follow-scroll. The UI exposes concise progress only, never private chain-of-thought.
4. An explicit failed/rejected tool stays failed after the overall run completes. Only in-flight visual states may be finalized.
5. Successful intermediate `lark_execute` results survive a later authorization yield as provider-safe tool history, with arguments stripped and bounded result data.
6. After an ambiguous write, write retries remain blocked, but a bounded catalog-proven read-only command may verify the result.
7. No database migration, new endpoint, new Feishu capability, or production deployment.

## Required verification

- Customer regressions are committed RED before implementation.
- Focused Go and Vitest suites, `task lint`, `npm run lint`, and `npm run type-check` pass.
- Backend and frontend are merged to `develop`, deployed to Dev, and health checked. Final multi-authorization acceptance is performed by the user.
