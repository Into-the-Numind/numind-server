# lark-execute-bplus H1 Verification

## Summary

Implemented the short-term B+ hardening for `lark_execute`: a minimal protocol card in the tool description, structured model-to-backend input errors for missing `argv`, and a two-strike stop-loss for repeated missing `argv` calls before any Feishu request is made.

## Verification

- `go test ./internal/numind/biz/agent -run 'TestLarkExecuteBPlusModelInputProtocolErrors|TestLarkPersonalWorkspace_Run359WriteCommandContract|TestLarkPersonalWorkspace_ExecuteRejectsUntrustedIdentityAndStrictJSON|TestLarkExecuteLangfuseNeverRecordsStdinOrProviderErrors'` — PASS
- `go test ./internal/numind/biz/agent` — PASS
- `GOPROXY=https://goproxy.cn,direct PATH="$(go env GOPATH)/bin:$PATH" task lint` — PASS

## Gate Blocker

- `go test ./...` — FAIL in `internal/numind/biz/credit`: `TestCheckAndEstimateBudget_PaidModel_MemberWithBalance_OK` returns `credit: insufficient balance: need 60 credits, have 0`.
- This blocker is outside the hotfix file set. The hotfix modified only `internal/numind/biz/agent/tool_lark_execute.go`, `internal/numind/biz/agent/tool_lark_retry_budget.go`, and tests committed in `internal/numind/biz/agent/tool_lark_write_command_guidance_test.go`.
