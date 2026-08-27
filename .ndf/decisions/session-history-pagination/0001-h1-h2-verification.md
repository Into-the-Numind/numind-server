# Session history pagination: H1/H2 verification

Date: 2026-08-27

## Customer-visible behavior

- Opening a conversation requests the newest 100 Agent runs.
- Those 100 runs render in normal reading order, oldest to newest within the page.
- Reaching the top requests the next older page with `offset=100&limit=100`.
- Older messages are prepended without moving the message the reader was looking at.
- A failed older-page request leaves the current conversation visible and offers retry instead of replacing the page with a fatal error.

## Database safety

- MySQL sorts only fixed-width run IDs by `started_at DESC, id DESC`.
- Complete run rows, including large message JSON, are hydrated afterward by ID.
- No message content is truncated.
- No database buffer setting or production configuration is changed.

## Regression protection

- Server store test proves the ordered query selects IDs rather than `SELECT *`.
- Server controller test proves `offset` and `limit` reach the service and pagination metadata is returned.
- Frontend browser test proves requests `[0, 100]`, prepends exactly 100 older messages, and keeps anchor drift at or below 2px.
- The browser regression passed 5 consecutive runs and one trace-enabled run on the mocked Playwright project.
- Successful trace contains no console errors or page errors.

## Quality gates

- Server targeted store, biz, and controller tests: pass.
- Server `golangci-lint run ./...`: pass.
- Server full `go test ./...`: one unrelated existing failure in `TestControlledLarkCLIRunner_CompleteUserAuthOutcomeMatrix/timeout_wins_over_truncated_output`; reproduced unchanged on develop.
- Frontend lint: 0 errors, 7 existing unrelated warnings.
- Frontend type check: pass.
- Frontend full unit suite: 1190 passed, 11 skipped, 3 todo; extension suite 40 passed.
- Frontend pagination Playwright test: 5/5 repeated passes plus 1 trace-enabled pass.

## Scope boundary

This Hotfix repairs only conversation snapshot loading and older-history pagination. It does not change Feishu write behavior, model-provider handling, billing, permissions, database schema, or production configuration.

## H3 merge and Dev deployment

- `ndf-done` merged and pushed backend develop at `5d1db3b5` and frontend develop at `8d295c5`.
- Both feature worktrees and local feature branches were removed successfully.
- The backend Dev image compiled and passed its in-image binary checks.
- TCR rejected the image push because `youshunumind/numind-server` has reached the personal-edition limit of 100 tags.
- The registry contains 66 rebuildable `develop-<sha>` tags, but the configured registry credentials do not have tag-delete authority. A delete-token request and a single exact-tag probe were denied; no tags were deleted.
- No Dev container was replaced. The frontend was intentionally not deployed alone, avoiding a mismatched Dev pair.
