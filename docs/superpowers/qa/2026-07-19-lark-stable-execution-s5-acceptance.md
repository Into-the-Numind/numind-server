# Stable Lark Execution — S5 Acceptance

Date: 2026-07-19
Feature: `lark-stable-execution`
Repositories: `numind-server`, `numind-web-v3`

## Result

PASS. The Agent now supplies Feishu business intent only. The trusted platform derives the current user, command catalog entry, scopes, risk, confirmation, generation fence and idempotency. Legacy `skill_receipts` inputs are accepted only for rolling compatibility and are ignored. Explicitly recoverable command corrections render as progress; real failures remain terminal errors.

## Customer RED and browser diagnosis

- Backend RED commit `68b0bb43` reproduced valid Drive search being rejected before the executor when the model omitted or malformed a receipt.
- Frontend RED commit `fa9eded` reproduced a completed Agent run that still displayed red `执行出错`.
- Playwright diagnostics captured HTTP 200 for the routed run, no JavaScript errors, final output visible, and the incorrect DOM text `执行出错` before the fix.
- After the fix the same diagnostic captured `调整执行方式`, zero `.tl-line.error`, zero visible `执行出错`, and the final answer.

## Quality evidence

All commands exited 0 unless noted as an expected customer RED before implementation.

### Backend

- `go test ./internal/numind/biz/agent/... ./internal/numind/biz/feishu/... -count=1`
- `go test ./...`
- `go test -race ./internal/numind/biz/agent/... ./internal/numind/biz/feishu/...`
- `PATH="$(go env GOPATH)/bin:$PATH" task lint`
- Focused policy tests cover current-user identity and idempotency derivation, forbidden/unknown commands, connection isolation across users, scope preflight, unknown started writes, and legacy receipt compatibility.

The only emitted compiler messages were the repository's existing macOS `sqlite3_auto_extension` deprecation warnings; tests and lint passed.

### Frontend

- `npm run test:unit -- --run`: 96 files passed; 1116 tests passed, 11 skipped, 3 todo.
- `npm run lint && npm run type-check`: passed with zero errors; seven pre-existing unused-variable warnings outside this feature.
- `npx playwright test e2e/agent-tool-recovery.spec.ts --project=mocked --reporter=line`: passed.
- Focused store and component suites passed, covering both recoverable and hard-error branches.

## Security and compatibility review

- Tool schemas no longer expose or require receipts.
- Old `skill_receipts` keys are decoder-compatible but are neither parsed nor copied into execution requests.
- `lark_skill_read` no longer returns a model-visible receipt.
- User ID, Agent run ID, tool-call ID and idempotency key remain server-context-only.
- Command catalog normalization still rejects `auth`, `config`, `whoami`, `im`, unknown verbs/flags and injected platform-owned flags.
- Docs/Base/Wiki/Drive remain the only business domains.
- Account generation fencing, per-user encrypted CLI homes, scope preflight, high-risk confirmation, durable idempotency and unknown-write stop rules are unchanged.
- Recoverable UI semantics require an explicit backend `recoverable:true`; legacy and real errors stay red and terminal.
- Changed production files contain no credentials, tokens, App IDs, secrets or private runtime paths.

## Dev acceptance prompts

1. `请读取飞书文档「有数飞书二次连接测试」，告诉我文档标题和正文内容。不要创建或修改任何飞书内容。`
2. `请把飞书文档「有数飞书二次连接测试」中的“当前状态：待验证”替换为“当前状态：更新成功”，并在末尾追加“测试编号：DOC-UPDATE-002”。完成后告诉我文档链接。`
3. In a new Agent conversation, repeat prompt 1 and confirm there is no receipt/configuration request and no false red execution error during a successful correction.
4. Trigger a genuinely invalid or unavailable operation and confirm a real terminal failure is still shown as an error.
