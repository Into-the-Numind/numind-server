# T12 user API broker wiring and soft fallback

## Scope

Implemented user API Sandbox backend selection so AI Agent / plugin / document-tool execution can use the constrained `sandboxd` broker on Prod, while the main product API still boots if the broker runtime is unavailable.

## What changed

- Added `sandbox.NewPoolFromConfig`, which selects exactly one backend:
  - `disabled`: no runtime client.
  - `docker`: dev-only direct Docker CLI client.
  - `broker`: Unix-socket broker client, with no Docker CLI/socket access in the API process.
- Updated `biz.NewBiz` to use the backend-aware factory instead of always constructing the Docker CLI client.
- Added broker lease activation in `SandboxHookManager`: after the audit row is created, the hook calls broker `Activate(leaseID, agentRunID, sandboxSessionID)`.
- If activation fails, the lease is destroyed via `Pool.Return`, the audit row is marked failed with a stable message, and no session is exposed to the executing tool.
- Broker config errors degrade to a disabled pool and are logged; they do not stop user API startup.
- Broker runtime failures after client construction remain asynchronous pool failures; they do not block `NewBiz`.

## Product meaning

For customers, this means the new AI Agent / plugin / document-system sandbox path can be turned on through Prod config, but if the Sandbox sidecar is down, normal product areas such as login, SOP, chatbot, settings, XHS, and Feishu connection are not taken down with it.

## Verification

- `go test ./internal/numind/biz/sandbox ./internal/numind/biz/agent ./internal/numind/biz -run 'ClientFactory|SandboxWiring|SandboxHookManager_PreToolCall_Broker|SandboxHookManager_PreToolCall_HappyPath|SandboxHookManager_PreToolCall_StoreCreateError' -race -count=1`
- `go test ./internal/numind/biz/sandbox ./internal/numind/biz/agent ./internal/numind/biz -race -count=1`
- `go test ./...`
- `PATH="$(go env GOPATH)/bin:$PATH" GOPROXY=https://goproxy.cn,direct task lint`

All passed. `config_prod.yaml`, Prod server, Prod database, and customer data were untouched.
