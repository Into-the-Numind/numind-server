# T17 — Broker deploy, rollback, cleanup, and runbook

Date: 2026-07-30
Task: T17 broker deploy rollback runbook

## Outcome

Added the automated broker release layer:

- `deploy-sandboxd-remote.sh` provisions the host base, extracts
  `numind-sandboxd` and `numind-sandbox-reconcile` from the server image,
  verifies checksums, drains the old broker, atomically replaces binaries,
  restarts broker, checks `/healthz` and `/readyz`, and restores the old binary
  if broker readiness fails.
- `deploy.sh` now copies the Sandbox deploy/provision scripts and systemd unit
  bundle to Prod before user API deployment when
  `NUMIND_SANDBOX_BACKEND=broker`.
- `deploy.sh` writes/reads the broker GID bridge file so the later user API
  deploy can `--group-add` the exact `numind-sandbox-api` GID reported by
  provisioning.
- `release.sh` now forwards the approved `NUMIND_SANDBOX_*` values from the Mac
  side to the build server, and `deploy.sh` forwards them onward to Prod.
- The runbook documents the AI-executed release sequence, stop conditions,
  rollback sequence, cleanup boundaries, and required evidence without asking a
  non-technical product owner to run SSH commands.

The original T17 file list did not include `scripts/cicd/deploy.sh`. This task
needed to touch it because `deploy.sh` is the build-server-to-Prod bridge; without
that change, T14's broker backend/socket/owner/GID contract would never reach the
remote `deploy-remote.sh`.

In product terms: Prod will only turn on Sandbox for the customer-facing API
after the separate Sandbox traffic-cop process is installed and ready. If the
traffic cop fails to come up, the customer API deploy is stopped before it can
depend on Sandbox.

## Verification

Passed:

```text
bash -n scripts/cicd/deploy-sandboxd-remote.sh scripts/cicd/release.sh scripts/cicd/deploy.sh scripts/cicd/deploy-remote.sh scripts/cicd/test-release-preflight.sh
bash scripts/cicd/test-release-preflight.sh
bash scripts/cicd/test-sandbox-provisioning.sh
bash scripts/cicd/test-sandbox-artifacts.sh
bash scripts/cicd/test-deploy-replace-race.sh
go test ./cmd/numind-sandboxd ./cmd/numind-sandbox-reconcile -count=1
git diff --check
git diff -- config_prod.yaml
```

`test-release-preflight.sh` now includes fake remote checks for:

- broker deploy success after readiness;
- new binary install and broker GID env output;
- old broker drain/restart/readiness;
- cleanup limited to unreferenced image prune;
- readiness failure blocking user API deployment;
- readiness failure restoring the old broker binary and running reconcile
  dry-run.

`test-sandbox-artifacts.sh` still reports local DockerHub metadata fetch failure
as a WARN and completes static-only locally. Strict full Docker verification must
be rerun on the build server with `NUMIND_SANDBOX_ARTIFACTS_STRICT=1`.

## Remaining dependency

T18 owns the final integration/security gate. No Prod deploy has been executed.
