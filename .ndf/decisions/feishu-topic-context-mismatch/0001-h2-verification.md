# H2 Verification: Feishu Topic Customer Mismatch

## Context

Dev customer report: the uploaded/profile-extracted customer context was real estate (`Yvonne`), but the final Feishu topic document included emotional-consulting topics. Direct document inspection showed the profile card was real-estate specific while the topic document was an old/generic managed topic container with unrelated first-round content.

## Fix

`lark_execute` now keeps a process-local per-run guard from successful `docs +fetch` results:

- profile managed docs record the current profile customer;
- topic managed docs record the fetched target document and its customer title when present;
- managed topic writes are rejected before Feishu execution when the fetched target belongs to another customer or is a managed topic document missing the required customer-specific title.

This keeps Agent guidance as the first line of defense and adds a host-side write fence for the customer-crossing failure mode.

## Verification

- `go test ./internal/numind/biz/agent -run 'TestLarkExecuteBlocksAgent3AppendWhen(ProfileAndTargetCustomersMismatch|TargetTopicDocHasNoCustomerTitle)' -count=1` PASS
- `go test ./internal/numind/biz/agent -count=1` PASS
- `go test -p 1 ./...` PASS
- `GOPROXY=https://goproxy.cn,direct PATH="$(go env GOPATH)/bin:$PATH" task lint` PASS

Notes:

- The required RED commit exists: `7b8360d3 test(qa): reproduce feishu topic customer mismatch`.
- Initial `task lint` failed while installing `golangci-lint@latest` from `proxy.golang.org` due an I/O timeout; the retry with a reachable Go proxy passed.
- Initial parallel `go test ./...` reported `internal/numind/biz/agent` FAIL amid unrelated external-model/quota test logs. The same package passed when rerun directly, and the full suite passed with `-p 1`.
