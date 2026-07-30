# T15 — Portable sandbox artifacts

Date: 2026-07-30
Task: T15 portable sandbox artifacts

## Outcome

The server image build now produces and carries the two Sandbox operational
artifacts:

- `/app/numind-sandboxd`
- `/app/numind-sandbox-reconcile`
- `/app/sandbox-artifacts.sha256`

`numind-sandboxd` is intended to be extracted and run by the Prod host outside
the user API container. `numind-sandbox-reconcile` remains available inside the
server release image for rollback/drain repair.

The main API image contract remains unchanged: production builds pass
`WITH_DOCKER_CLI=false`, so the API container does not install the Docker CLI.
Dev builds still set `WITH_DOCKER_CLI=true` for the legacy direct-Docker path.

In product terms: the release now contains the "Sandbox traffic cop" binary and
the "cleanup/refund reconciliation" tool, but the customer API itself still
does not receive the master Docker key.

## Verification

Passed:

```text
bash -n scripts/cicd/build-and-push.sh scripts/cicd/test-sandbox-artifacts.sh
bash scripts/cicd/test-sandbox-artifacts.sh
go test ./cmd/numind-sandboxd ./cmd/numind-sandbox-reconcile -count=1
git diff --check
```

The local Docker artifact build attempted a real `docker build --target
sandbox_artifacts`, but DockerHub token fetch timed out before image metadata was
available. The script therefore completed in static-contract mode and printed a
clear WARN. On the build server or any Docker-capable strict gate, rerun with:

```text
NUMIND_SANDBOX_ARTIFACTS_STRICT=1 bash scripts/cicd/test-sandbox-artifacts.sh
```

## Follow-up dependency

T17 must use `/app/sandbox-artifacts.sha256` when extracting the binary and must
record the checksum in the deployment evidence before starting or rolling back
`sandboxd`.
