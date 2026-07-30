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
   The mutable spawn and exec specifications are package-private. RPC callers
   cannot supply image, mount, device, network, privilege, capability, cgroup,
   entrypoint, user, workdir, or security-option fields.
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
    `/workdir/output`. A fixed Python helper then traverses every path component
    inside the container with descriptor-relative `O_NOFOLLOW` operations.
11. Frame copy input into at most 64 KiB chunks and finish with an explicit
    terminal frame plus SHA-256 digest. The isolated Python helper publishes
    its private temporary regular file only after that terminal verifies, so
    Docker CLI disconnect, cancellation, and over-limit EOF cannot publish a
    truncated file. Publication uses a no-overwrite hardlink operation.
12. Run both helpers with Python isolated mode so `/workdir`, `PYTHONPATH`, and
    task-authored modules cannot replace trusted standard-library modules.
    Copy output through a fixed streaming tar writer that rejects symlinks,
    hardlinks, devices, FIFOs, inode replacement, and special types before
    opening them. No caller-controlled tar option is ever executed.
13. Bound the output helper itself to depth 16, 56 entries, 10 regular files,
    50 MiB per file, and 200 MiB total. Directory iteration is streaming, and
    descriptor use is bounded by the depth ceiling.
14. Extract output archives with descriptor-relative, no-follow filesystem
    operations starting at `/`, including every host destination ancestor.
    Do not call a host `tar` process and do not buffer the archive.
15. Read the checksum-verified Seccomp profile into a private startup snapshot.
    Each Docker launch receives a new anonymous, unlinked file descriptor
    inherited as fd 3; Docker reads `/dev/fd/3`, never the configurable
    pathname. Replacing or deleting that pathname after startup cannot affect
    a task launch.
16. Recognize a missing copy-out source only through the helper's dedicated
    exit code and exact marker. Mid-traversal producer errors remain failures
    even when their text contains “No such file”.
17. Context cancellation closes the stream reader and the active stream writer
    so a blocked read or write cannot strand a broker goroutine.

## Verification

- Focused Runtime/Stream/Copy race tests pass.
- The focused race-test group passes twenty consecutive runs.
- Full `sandboxbroker` and legacy Dev `biz/sandbox` package race tests pass.
- Boundary, archive-attack, container and host ancestor-symlink,
  hardlink/FIFO, blocked-reader, blocked-writer, checksum-replacement,
  environment allowlist, label-injection, and combined-output tests pass.
- Controlled Docker CLI behavior tests execute the fixed Python helpers and
  cover copy-in, copy-out, option-like filenames, missing sources, cancellation,
  overwrite rejection, and input/output ceilings.
- Additional regressions cover task-authored Python module poisoning,
  incomplete framed input, anonymous Seccomp fd pinning, exact missing-source
  classification, helper entry/depth ceilings, and trusted macOS temp-root
  canonicalization without weakening no-follow checks below that root.
- `go vet ./...` and `golangci-lint run ./...` pass through `task lint`.
- `config_prod.yaml`, the Prod database, and all environment switches remain
  untouched.
