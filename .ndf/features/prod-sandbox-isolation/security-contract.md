# Prod Sandbox Isolation Security Contract

Date: 2026-07-30
Feature: `prod-sandbox-isolation`

## Product-level contract

Prod may expose Dev's Sandbox-backed Agent/plugin/document-generation features to
customers only through a constrained broker. The user-facing API may receive the
broker socket. The admin API must not receive the broker socket or any Docker
socket. Neither API may receive the host's main Docker control socket.

## Locked safety properties

- Global capacity is five containers and five active Sandbox tasks.
- A sixth task enters FIFO and does not bypass the five-slot gate.
- Each task container is fixed at 512MiB, 1 CPU, 64 PIDs, read-only root,
  `network=none`, cap-drop ALL, and `no-new-privileges`.
- Runtime requests cannot provide image, mount, network, device, privileged,
  capability, cgroup, or Docker socket fields.
- Broker transport is Unix socket only, with 64KiB metadata, 32 connections,
  four copy streams, one stream per lease/direction, and 100MiB/s aggregate copy.
- Sandbox image must be pinned by digest, not a floating tag.
- sandboxd refuses business `config_prod.yaml`.
- Rootless Sandbox user must not read Prod app data, config, certs, DB/cache
  directories, main Docker data, main Docker socket, or Prod secrets.
- Broker deployment must pass `/healthz` and `/readyz` before user API is
  deployed in broker mode.
- Broker readiness failure restores the previous broker binary and blocks user
  API deployment.
- Rollback/reconcile must not delete journal or data-root evidence.

## Evidence commands

```text
go test ./internal/numind/sandboxbroker -run 'IntegrationSecurityContract' -count=1
bash scripts/cicd/test-sandbox-isolation.sh
bash scripts/cicd/test-release-preflight.sh
bash scripts/cicd/test-sandbox-provisioning.sh
bash scripts/cicd/test-sandbox-artifacts.sh
```

Full S4 gate additionally runs:

```text
go test ./... -count=1
go test -race ./internal/numind/biz/sandbox ./internal/numind/sandboxbroker ./internal/numind/sandboxreconcile -count=1
task lint
bash scripts/cicd/test-sandbox-capacity.sh
```

## Current known limitations

- T3 residual isolation risks were explicitly accepted/deferred by the product
  owner on 2026-07-30: the Seccomp create-to-unlink same-UID race and one
  wrapped-archive off-by-one guard remain recorded and are not counted as
  formally reviewed.
- Local DockerHub metadata fetch is currently unstable; strict artifact image
  verification must be rerun on the build server with
  `NUMIND_SANDBOX_ARTIFACTS_STRICT=1`.
- No Prod deployment has been executed by this S4 work.
