# S5 Verification — standard-office-output-tools

Date: 2026-07-27

## Commands

- `go test ./internal/numind/biz/agent -run 'CreateDocx|CreateXLSX|CreatePPTX|DisabledSandboxRuntimeExcludesSandboxTools|IsSandboxIsolatedExecTool|PlatformToolFactory_LoadTools|RunnerStreamArgsDelta' -count=1`
- `go test ./internal/numind/biz/agent -count=1`
- `go test ./internal/numind/biz/skill -count=1`
- `go test ./...`
- `PATH="$(go env GOPATH)/bin:$PATH" GOPROXY=https://goproxy.cn,direct task lint`

## Result

PASS.

## Notes

- The first `task lint` attempt failed while downloading `golangci-lint` from `proxy.golang.org`.
- A second attempt using `GOPROXY=https://goproxy.cn,direct` downloaded the tool, but the local `GOPATH/bin` directory was not on `PATH`.
- The final lint command with `GOPATH/bin` added to `PATH` passed.
- `go test ./...` passed after updating tool-count and three-Agent manifest contract tests for the two new native tools.
- The sqlite cgo deprecation lines are existing macOS compiler warnings, not test or lint failures.
