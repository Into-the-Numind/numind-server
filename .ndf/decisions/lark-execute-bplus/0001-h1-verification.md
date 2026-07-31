# lark-execute-bplus H1/H2 Verification

## Summary

Implemented the short-term B+ hardening for `lark_execute`: a minimal protocol card in the tool description, structured model-to-backend input errors for missing `argv`, and a two-strike stop-loss for repeated missing `argv` calls before any Feishu request is made.

## Verification

- `go test ./internal/numind/biz/agent -run 'TestLarkExecuteBPlusModelInputProtocolErrors|TestLarkPersonalWorkspace_Run359WriteCommandContract|TestLarkPersonalWorkspace_ExecuteRejectsUntrustedIdentityAndStrictJSON|TestLarkExecuteLangfuseNeverRecordsStdinOrProviderErrors'` — PASS
- `go test ./internal/numind/biz/agent` — PASS
- `GOPROXY=https://goproxy.cn,direct PATH="$(go env GOPATH)/bin:$PATH" task lint` — PASS
- `go test ./...` — PASS after merging latest `develop`.

## Gate Unblocker

- Initial `go test ./...` was blocked by an unrelated fixture issue in `internal/numind/biz/credit`: `TestCheckAndEstimateBudget_PaidModel_MemberWithBalance_OK` seeded a 0-credit member while asserting the paid-model path for a member with balance.
- That fixture was fixed and merged to `develop` in `b0f9e4bc` before this hotfix was re-verified.
