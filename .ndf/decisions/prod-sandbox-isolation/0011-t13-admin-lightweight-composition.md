# T13 lightweight admin composition

## Scope

Implemented a lightweight admin API composition root so the management backend does not initialize user-side Agent, Sandbox, Feishu, document runtime, memory, or XHS workers.

## What changed

- Added `biz.NewAdminBiz`, which constructs only the admin route dependencies currently needed:
  - SOP admin service
  - credit service
  - pricing calculator
  - monitor service
  - notification center via the existing lazy getter
- Updated `admin_router.go` to use `biz.NewAdminBiz(store.S)` instead of falling back to full `biz.NewBiz(store.S)`.
- Updated Agent Run admin cancel wiring to pass a nil in-memory runner, preserving DB cancellation markers without starting the user-side Agent runtime.
- Updated the previous production lifecycle contract test: user routes still must reuse entrypoint `biz.B`, while admin routes must now use the lightweight admin composition root.

## Product meaning

The admin system can still manage users, credits, SOP, monitor, AI service settings, Agent Run cancellation, marketplace, and notifications. It no longer silently starts customer-facing AI Agent/Sandbox/Feishu/XHS background machinery.

## Verification

- `go test ./internal/numind/biz ./internal/numind -run 'AdminBiz|Admin.*Sandbox' -race -count=1`
- `go test ./internal/numind/biz ./internal/numind ./internal/numind/controller/v1/admin -race -count=1`
- `go test ./...`
- `PATH="$(go env GOPATH)/bin:$PATH" GOPROXY=https://goproxy.cn,direct task lint`

All passed. `config_prod.yaml`, Prod server, Prod database, and customer data were untouched.
