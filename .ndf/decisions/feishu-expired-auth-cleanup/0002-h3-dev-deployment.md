# H3 Dev Deployment - Feishu Expired Auth Cleanup

## Merge

- `ndf-done` merged `fix/feishu-expired-auth-cleanup` into `develop`.
- `develop` was pushed to origin at merge commit `93d79f51`.
- Hotfix worktree and local branch were removed by `ndf-done`.

## Deployment

- Command: `bash scripts/cicd/release.sh dev server`
- Image: `ccr.ccs.tencentyun.com/youshunumind/numind-server:develop-93d79f51`
- Registry digest: `sha256:dded1f1fa4ef4b3d84d5e1eb6d719bf1ffd9326f591fe87c4bb7c22a7fbfc5cd`

## Smoke Verification

- `curl -fsS http://49.233.219.254:9091/healthz` returned `{"status":"ok"}`.
- `docker inspect` reported `numind-server-dev` as `running healthy` on image `develop-93d79f51`.
- `/v1/feishu/status` for the E2E user returned `state=connected` with no active authorization action payload.
