# Agent Run Survives Exit S5 Verification

## Backend

- Focused tests:
  - `go test ./internal/numind/biz/agent/stream -run TestStreamExecutionRegistry -count=1` passed.
  - `go test ./internal/numind/biz/agent -run 'TestPrepareStreamRun|TestStartPrepared|TestCancel_|TestAnswerStream|TestAcquireStreamLock' -count=1` passed.
  - `go test ./internal/numind/controller/v1/agent -run 'TestCreateStream_|TestAnswerStream_|TestSubscribeEvents|TestObserveRunEvents_' -count=1` passed.
- Full tests: `go test ./... -count=1` passed.
- Lint: `PATH="$(go env GOPATH)/bin:$PATH" task lint` passed.
- Notes: `task lint` without the GOPATH bin prefix installed `golangci-lint` but failed to find it on PATH; rerunning with `/Users/zhiyuchen/go/bin` on PATH passed. Go commands emitted existing sqlite-vec macOS deprecation warnings from cgo.

## Frontend

- Focused Vitest:
  - `npm run test:unit -- src/stores/__tests__/agentChat.spec.ts src/stores/__tests__/agentChat-session-epoch.spec.ts` passed.
  - `npm run test:unit -- src/composables/__tests__/useAgentStream.spec.ts src/composables/__tests__/useAgentRun.spec.ts` passed.
  - `npm run test:unit -- src/views/agent/__tests__/AgentChatView.spec.ts` passed.
  - `npm run test:unit -- src/stores/__tests__/agentChat-streaming.spec.ts` passed.
  - Final targeted regression run `npm run test:unit -- src/composables/__tests__/useAgentStream.spec.ts src/stores/__tests__/agentChat-streaming.spec.ts src/views/agent/__tests__/AgentChatView.spec.ts src/composables/__tests__/useAgentRun.spec.ts` passed with 122 passing tests and 3 existing todo tests.
- Lint: `npm run lint` passed with 0 errors and 7 existing unrelated warnings.
- Type check: `npm run type-check` passed.
- Diff check: `git diff --check` passed.

## Browser QA

- Mocked Playwright: `npm run test:e2e -- --project=mocked e2e/agent-streaming.spec.ts` passed with 11 passing tests and 1 existing known-skipped multi-tab scenario.
- Covered browser contracts:
  - Happy streaming reaches final answer and re-enables input.
  - Explicit stop calls the cancel path.
  - Network interruption keeps the input stop control available for explicit cancellation.
  - `question_prompt` pause allows answer submission and `answer-stream` resume.
  - External-action/card continuation receives post-card realtime events.
- During QA, the first mocked Playwright run exposed a question-prompt pause regression. Fixes `1289ec9` and `e17a905` preserve in-app pause boundaries for initial and attached observers while keeping auth/external continuation behavior intact. The mocked Playwright suite passed after these fixes.
- Limitations: this QA used mocked browser routes instead of a real LLM Dev run. The full Dev acceptance checklist should still exercise a live run refresh/navigation-away flow, explicit stop, and a two-tab observation/polling case after deployment.

## AI Observability

- No new LLM/generation point was added in frontend T7-T10.
- Backend T6 recorded the AI-service observability audit in `.ndf/decisions/agent-run-survives-exit/0001-ai-observability.md`; `.claude/rules/ai-service.md` was not present in the checkout at that time.
