# T16 — Rootless host provisioning

Date: 2026-07-30
Task: T16 rootless host provisioning

## Outcome

Added the idempotent host-provisioning layer for running Sandbox on the same
Prod server with a separate Rootless Docker base:

- non-login `numind-sandbox` user
- dedicated `numind-sandbox-api` broker socket group
- 8GiB ext4 data-root image
- cgroup v2/controller validation
- systemd slices/drop-ins for parent/control/workload memory, CPU, and task caps
- rootless prerequisite checks
- `sandboxd.yaml` and `sandboxd.env` rendering from pinned release evidence
- negative permission checks for Prod app data, config, certs, main Docker, and
  database/cache directories

Existing state is accepted only when it matches the expected contract. Conflicts
such as a wrong user home, missing cgroup controller, readable Prod directory,
or floating Sandbox image tag fail closed.

In product terms: this prepares a separate "Sandbox yard" on the same Prod
server. The user API can later be given the gate to that yard, but Sandbox does
not get access to customer data, app config, certificates, or the host's main
Docker control socket.

## Verification

Passed:

```text
bash -n scripts/cicd/provision-sandbox-host.sh scripts/cicd/test-sandbox-provisioning.sh
bash scripts/cicd/test-sandbox-provisioning.sh
git diff --check
git diff -- config_prod.yaml
```

The test suite uses a fake root directory and validates idempotency, UUID/capacity
rendering, broker GID output, cgroup controller enforcement, directory ACL
failure, and pinned image digest enforcement without touching the real machine.

## Follow-up dependency

T17 must call this provisioning script before broker deployment, then use the
printed `NUMIND_SANDBOX_BROKER_GID` when deploying the prod user API in broker
mode.
