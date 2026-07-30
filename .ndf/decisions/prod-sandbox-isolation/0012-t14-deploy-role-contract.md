# T14 — Server/Admin deploy role contract

Date: 2026-07-30
Task: T14 server/admin deploy role contract

## Outcome

Prod deploy commands now encode the Sandbox access contract instead of relying on
manual operator memory:

- Prod user API (`TARGET=server`) can run with `NUMIND_SANDBOX_BACKEND=broker`,
  but only if the remote host exposes a real `sandboxd.sock`, a dedicated
  numeric broker group GID, and an explicit stable
  `NUMIND_SANDBOX_BROKER_OWNER_ID`.
- Prod user API never receives `/var/run/docker.sock`, `/run/docker.sock`, or
  any `*/docker.sock` path through this deploy script.
- Prod user API can still deploy with Sandbox off by setting
  `NUMIND_SANDBOX_BACKEND=disabled`; in that mode it receives no broker socket
  and no Sandbox group.
- Prod admin API (`TARGET=admin`) is forced to
  `NUMIND_SANDBOX_BACKEND=disabled` and receives no broker socket, Docker
  socket, or Sandbox group.
- `config_prod.yaml` remains unchanged; the product switch is still the
  runtime `NUMIND_` override path.

In product terms: the customer-facing API may get the "run code in Sandbox"
door key, but the admin backend never gets that key, and neither process gets
the master Docker key.

## Verification

Passed:

```text
bash -n scripts/cicd/deploy-remote.sh scripts/cicd/test-release-preflight.sh
bash scripts/cicd/test-release-preflight.sh
git diff --check
bash scripts/cicd/test-deploy-replace-race.sh
git diff -- config_prod.yaml
```

The fake Docker preflight now asserts the positive server broker flags and
negative admin/server Docker-socket strings directly.

## Remaining dependency

This task only defines the remote deploy role contract. T17 still owns the
release orchestration that copies/starts `sandboxd`, forwards the deployment
time `NUMIND_SANDBOX_*` values to the Prod host, waits for broker readiness, and
performs rollback/drain sequencing.
