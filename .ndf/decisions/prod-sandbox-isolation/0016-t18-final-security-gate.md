# T18 — Final integration/security gate

Date: 2026-07-30
Task: T18 final integration security gate

## Outcome

Added the final security/integration contract without changing T1-T17
implementation files in the T18 commit:

- Go integration tests for five-slot FIFO scheduling across owners, fixed
  runtime container/exec limits, forbidden Docker/runtime args, sensitive env
  rejection, and broker Unix transport ceilings.
- Shell gate for deploy/release/provisioning/artifact static contracts and
  `config_prod.yaml` immutability.
- Reader-facing security contract documenting product-level guarantees, evidence
  commands, known residual risks, and the fact that Prod has not been deployed.

T18 initially found one real pre-existing integration bug from T16/T17: the
provisioning path rendered `seccomp_sha256` as a raw 64-hex value while the
runtime requires `sha256:<64hex>`. Per T18 rules, that was fixed in a separate
follow-up commit before resuming T18:

```text
90a2b1ae fix(sandbox): align seccomp checksum config
```

## Verification

Passed:

```text
go test ./internal/numind/sandboxbroker -run 'IntegrationSecurityContract' -count=1
bash scripts/cicd/test-sandbox-isolation.sh
bash scripts/cicd/test-sandbox-capacity.sh
bash scripts/cicd/test-sandbox-provisioning.sh
bash scripts/cicd/test-release-preflight.sh
bash scripts/cicd/test-sandbox-artifacts.sh
go test ./... -count=1
go test -race ./internal/numind/biz/sandbox ./internal/numind/sandboxbroker ./internal/numind/sandboxreconcile -count=1
PATH="$(go env GOPATH)/bin:$PATH" GOPROXY=https://goproxy.cn,direct task lint
git diff -- config_prod.yaml
```

Local artifact Docker build still cannot fetch DockerHub metadata for
`golang:1.24-alpine`, so `test-sandbox-artifacts.sh` completed static-only with
a clear WARN. Strict build-server verification remains required:

```text
NUMIND_SANDBOX_ARTIFACTS_STRICT=1 bash scripts/cicd/test-sandbox-artifacts.sh
```

## Status

S4 implementation tasks are complete at 18/18. This is not a Prod deployment
authorization or execution. Next stages remain independent review, S5 local
same-shape verification, S6 merge/deploy to Dev, and S7 Prod preflight.
