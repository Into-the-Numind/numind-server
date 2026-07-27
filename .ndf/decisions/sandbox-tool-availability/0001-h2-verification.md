# H2 Verification — sandbox-tool-availability

Date: 2026-07-27
Track: Hotfix
Repo: numind-server

## Scope

This hotfix addresses two related product failures reported from Agent file generation:

- Sandbox-backed tools could be exposed to the Agent even when the sandbox runtime was disabled, allowing the model to plan `create_docx`, `run_python`, or `bash_exec` calls that could never succeed.
- A busy warm pool did not request extra containers while all warm sessions were borrowed, so modest concurrency could surface `ErrPoolExhausted` as a user-facing sandbox unavailable failure.

## Commits

- `01dba615 test(qa): reproduce sandbox tool availability failures`
- `b0220187 fix(agent): stabilize sandbox-backed tool availability`

## Verification

- `go test ./internal/numind/biz/sandbox -run TestPool_BorrowScalesWhenWarmPoolBusy -count=1` — PASS
- `go test ./internal/numind/biz/agent -run TestSelectToolsForRun_DisabledSandboxRuntimeExcludesSandboxTools -count=1` — PASS
- `go test ./internal/numind/biz/sandbox -count=1` — PASS
- `go test ./internal/numind/biz/agent -count=1` — PASS
- `go test ./...` — PASS
- `task lint` — PASS after retrying with `GOPROXY=https://goproxy.cn,direct` because the first run timed out downloading `golangci-lint` from `proxy.golang.org`.

## Result

H2 passes. H3 can proceed with `ndf-done` and Dev deployment when approved.
