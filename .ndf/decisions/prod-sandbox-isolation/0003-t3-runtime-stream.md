# T3 Fixed Sandbox Runtime and Streaming

Date: 2026-07-30

## Context

Prod must provide the Dev-proven document and code-execution tools on the
existing cloud server without letting a Sandbox task inherit the main API's
credentials, network, writable root filesystem, or unbounded host resources.
Large input/output files also cannot be accumulated in API process memory.

## Decisions

1. Accept only the production Sandbox image repository pinned by a lowercase
   SHA-256 digest. Tags and alternate repositories are rejected.
2. Verify the Seccomp file is an absolute, regular, single-link, privately
   writable file owned by root or the broker account and require its configured
   SHA-256 checksum to match.
3. Generate Docker launch arguments only through the verified runtime policy.
   RPC callers cannot supply image, mount, device, network, privilege,
   capability, cgroup, entrypoint, or security-option fields.
4. Fix every task at non-root `1000:1000`, all capabilities dropped, no added
   capabilities, no-new-privileges, read-only root, no network, 512 MiB memory,
   one CPU, 64 PIDs, and the Sandbox workload cgroup.
5. Use only fixed tmpfs mounts for `/workdir`, `/skills`, and `/tmp`; never
   expose the Docker socket or a host bind mount to a task.
6. Fix one command execution at 30 seconds and one active lease at the same
   non-extendable 300-second limit used by the lease journal.
7. Permit only literal command arguments and an explicit, non-sensitive
   environment-variable allowlist. HOME, PATH, Docker, proxy, database,
   credential, Feishu, cloud, and API-key variables are always rejected.
8. Bound combined stdout and stderr at 4 MiB.
9. Stream copy data with a 64 KiB buffer. Enforce 50 MiB per file,
   100 MiB aggregate input, 200 MiB aggregate output, and ten files per
   direction.
10. Canonicalize copy paths to `/workdir`, approved `/skills/<name>`, or
    `/workdir/output`. Reject absolute archive entries, parent traversal,
    symlinks, hardlinks, devices, FIFOs, overwrites, and destination symlink
    traversal.
11. Extract output archives with descriptor-relative, no-follow filesystem
    operations. Do not call a host `tar` process and do not buffer the archive.
12. Context cancellation closes both the stream reader and any closable stream
    writer so a blocked read or write cannot strand a broker goroutine.

## Verification

- Focused Runtime/Stream/Copy race tests pass.
- The focused race-test group passes twenty consecutive runs.
- Full `sandboxbroker` and legacy Dev `biz/sandbox` package race tests pass.
- Boundary, archive-attack, blocked-reader, blocked-writer, checksum,
  environment allowlist, label-injection, and combined-output tests pass.
- `go vet ./...` and `golangci-lint run ./...` pass through `task lint`.
- `config_prod.yaml`, the Prod database, and all environment switches remain
  untouched.
