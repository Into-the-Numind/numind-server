# H2 Verification - Feishu Expired Auth Cleanup

## Scope

- Preserve the one-Numind-account-to-one-current-Feishu-connection model.
- Treat expired pending auth sessions as non-blocking for new user-visible connection flows.
- Preserve exact operation-linked recovery for expired sessions that still belong to the same operation.

## Verification

- `go test ./internal/numind/store -run TestFeishuWorkspaceStore_ExpiredPendingSessionDoesNotOwnCurrentConnectionSlot -count=1`
- `go test ./internal/numind/store ./internal/numind/biz/feishu -count=1`
- `PATH="$(go env GOPATH)/bin:$PATH" GOPROXY=https://goproxy.cn,direct task lint`

## Notes

- The first `task lint` attempt failed while downloading `golangci-lint` from `proxy.golang.org`.
- The second attempt installed the linter via `goproxy.cn`, but the Taskfile shell did not include `$(go env GOPATH)/bin` in `PATH`.
- The final lint run added Go's bin directory to `PATH` and passed.
