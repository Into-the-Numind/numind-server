# H3 Dev Deployment: sandbox-dev-single-user-failure

## Deployment

Built and deployed local develop commit `bb4f3d7a` to Dev as:

- `ccr.ccs.tencentyun.com/youshunumind/numind-server:develop-bb4f3d7a`
- Registry digest: `sha256:b32c95662f8f520f62b3cef5d5e530a8f4cdcae5f13d90f44bba14cd942dc955`

Public health check passed:

- `http://49.233.219.254:9091/healthz` returned `{"code":0,"message":"","data":{"status":"ok"}}`

Runtime container inspect showed:

- Image: `develop-bb4f3d7a`
- State: running
- Health: healthy

## Sandbox Runtime Verification

After deployment, Dev had exactly five running sandbox containers with owner labels matching the new backend container. Startup logs showed one sandbox pool initialization:

- `sandbox pool initialized {"backend":"docker","pool_min":5,...}`
- `sandbox.Pool.reapOrphans: cleaned orphaned sandbox containers {"found":10,"reaped":10,"skipped_live_owners":0}`

The `found=10` containers were the stale pre-fix/duplicate-pool sandboxes from the prior runtime. They were cleaned on startup, then the fixed runtime warmed exactly five fresh sandboxes.

Idle sandbox memory observed after deployment:

- 632 KiB / 512 MiB
- 636 KiB / 512 MiB
- 636 KiB / 512 MiB
- 636 KiB / 512 MiB
- 648 KiB / 512 MiB

The 512 MiB value is the per-container memory cap, not preallocated memory.

## Remaining Operational Note

`ndf-done` merged the hotfix into local develop as `bb4f3d7a`, but GitHub push failed from the local machine due repeated HTTPS connectivity failures:

- connect timeout to `github.com:443`
- HTTP/2 framing layer error

Dev deployment used the local develop state through the normal rsync/build/deploy path and is healthy. Origin push and NDF worktree cleanup remain pending until GitHub connectivity recovers.
