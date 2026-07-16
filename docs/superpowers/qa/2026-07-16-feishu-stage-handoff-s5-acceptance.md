# Feishu Stage Handoff — S5 Acceptance

## Customer regressions

- Backend first reproduction: `9ac10b92 test(qa): reproduce feishu authorization stage handoff failure`.
- Frontend first reproduction: `46ca269 test(qa): reproduce stale feishu refresh failure`.
- Follow-up red commits permanently cover a locked Agent run after refresh, terminal states created inside the current acknowledgement, malformed refresh unions, stale store overwrite, and confirm/cancel races.
- Every production correction follows its failing test commit; no customer reproduction was removed.

## Final behavior matrix

| Stored/observed operation state | Backend action | Frontend action |
|---|---|---|
| `succeeded` | Compensate only the idempotent Agent continuation; never replay Confirm, Cancel, or lark-cli | Close URL controls, set `external_resume_ready`, keep observing the original run |
| `failed` | Terminalize the exact user/run/tool wait with the allowlisted failed result | Close card, show safe resend guidance, unlock input |
| `unknown` | Terminalize the exact wait with an honest unknown result | Require Feishu verification before retrying, unlock input |
| `cancelled` | Terminalize the exact wait with the cancelled result | Report cancellation, unlock input |
| `executing` | Return the current state without starting a second operation | Keep observing the existing operation |

The same matrix is covered for `user_completed`, `confirmed`, `cancelled`, and terminal refresh, including states committed during app approval, auth dispatch, confirmation, and concurrent cancel races.

## Automated verification

- Changed lifecycle/race tests: PASS.
- Full Feishu business and controller packages: PASS.
- Full backend suite at bounded package concurrency, `go test -p 4 ./... -count=1`: PASS.
- `PATH="$(go env GOPATH)/bin:$PATH" task lint`: PASS.
- Frontend full unit suite: 96 files, 1064 tests passed; 11 skipped and 3 todo are pre-existing.
- Feishu Playwright contract suite: 3/3 PASS, including terminal-card input unlock and removal of “处理中/取消任务”.
- `npm run lint`: PASS with zero errors and seven pre-existing unrelated warnings.
- `npm run type-check`: PASS.
- `git diff --check`: PASS; both code worktrees clean before the documentation update.

One default-concurrency backend run triggered the pre-existing controlled-runner one-second process-start flake. The exact test passed alone, the complete Feishu package passed, changed-focused race tests passed, and the full suite passed with package concurrency capped at four. No changed state-machine test failed.

## Safety and contract coverage

- Terminal settlement is fenced by user, operation, generation, run, and tool-call identity.
- `succeeded` repair cannot invoke the Feishu write path; Task11 continuation remains idempotent.
- Refresh returns an exclusive runtime-validated `action | terminal` union.
- Terminal responses contain no URL, device code, scopes, argv, account identifier, or provider output.
- Late refresh responses cannot overwrite shared connection state or a newer route/session card.
- Unknown outcomes never instruct blind replay.
- No schema, permission scope, secret, or production configuration changed.

## Independent review

- Code-quality/state-machine review: PASS, P0/P1/P2 = 0.
- Specification review: PASS, P0/P1/P2 = 0. Final requirements, proposal, design, plan, backend, and frontend state machines are aligned.

## Dev deployment

- Backend merged and pushed as `a7b96778`, then deployed as `develop-a7b96778`; `/healthz` returned `status=ok`.
- Frontend merged and pushed as `9a57eba`, then deployed as `develop-9a57eba`; `/health` was healthy and the public page returned HTTP 200.
- Browser canary loaded in 0.878 seconds with real content and no console errors.
- Authenticated Agent page loaded successfully; all observed application API requests returned HTTP 200.
- Running container image identities matched both deployed commits; post-deploy critical-log scan found zero `panic`/`fatal` lines.

## Remaining product acceptance

The original account must repeat the expired-card action once to confirm its exact persisted operation is settled. Production deployment is explicitly out of scope.
