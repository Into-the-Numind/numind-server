# S4 Post-review Release Hardening

Date: 2026-07-30
Feature: `prod-sandbox-isolation`

## Context

Before moving from S4 implementation toward S5/S6 rollout verification, an
independent backend release review found two production blockers:

1. The sandboxd systemd service runs as `numind-sandbox`, but its rendered
   config/env files were root-owned and not group-readable by that service user.
2. The broker config validated a seccomp checksum, but the deployment bundle did
   not install the matching seccomp profile onto the production host.

During strict build-server artifact verification, package installation also
exposed slow default Alpine/Debian/Ubuntu sources. This was a release reliability
issue rather than a product behavior change.

## Decision

- Bundle `deploy/sandbox/seccomp.json` with the broker service units and require
  provisioning to verify its SHA256 before installing it to
  `/opt/numind-sandbox/seccomp/seccomp.json`.
- Render sandboxd config/env as `root:numind-sandbox` with `0640` mode and make
  `/etc/numind-sandbox` searchable by the sandbox service group.
- Keep `config_prod.yaml` unchanged; all production activation still goes
  through runtime `NUMIND_*` environment values.
- Default Docker build package installs to domestic mirrors:
  - Alpine: `https://mirrors.aliyun.com/alpine`
  - Debian: `https://mirrors.aliyun.com/debian`
  - Ubuntu: `https://mirrors.aliyun.com/ubuntu`

## Verification evidence

Passed locally:

```text
bash -n scripts/cicd/provision-sandbox-host.sh scripts/cicd/test-sandbox-provisioning.sh scripts/cicd/test-release-preflight.sh scripts/cicd/test-sandbox-isolation.sh scripts/cicd/test-sandbox-artifacts.sh
git diff --check
git diff -- config_prod.yaml
bash scripts/cicd/test-sandbox-provisioning.sh
bash scripts/cicd/test-release-preflight.sh
bash scripts/cicd/test-sandbox-isolation.sh
go test ./internal/numind/sandboxbroker ./cmd/numind-sandboxd ./cmd/numind-sandbox-reconcile -count=1
bash scripts/cicd/test-sandbox-artifacts.sh
```

Passed on the build server:

```text
NUMIND_SANDBOX_ARTIFACTS_STRICT=1 bash scripts/cicd/test-sandbox-artifacts.sh
```

The strict build-server run verified that the production runtime image builds,
contains the Sandbox artifacts, and does not include Docker CLI when built with
`WITH_DOCKER_CLI=false`.

## Production status

No production deployment was executed by this hardening step.
