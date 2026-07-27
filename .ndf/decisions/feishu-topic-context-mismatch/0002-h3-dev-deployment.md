# H3 Dev Deployment: Feishu Topic Customer Mismatch

## Merge

`ndf-done` merged `fix/feishu-topic-context-mismatch` into local `develop` as `9653409a`, but failed during `git push origin develop` with GitHub HTTPS connectivity errors. The hotfix branch and worktree were intentionally retained by the script for retry/cleanup.

Manual push retry with HTTP/1.1 also failed because `github.com:443` was unreachable from the local machine.

## Dev Deployment

Deployed local `develop` commit `9653409a` to Dev server:

- image: `ccr.ccs.tencentyun.com/youshunumind/numind-server:develop-9653409a`
- registry digest: `sha256:b0da5c69b06d41f17e180245e9f56e3886026a4d192ea246e2ff879f3922ec2b`
- release command: `bash scripts/cicd/release.sh dev server`

## Verification

- `curl -fsS http://49.233.219.254:9091/healthz` returned `{"code":0,"message":"","data":{"status":"ok"}}`
- `docker inspect` through the build-server jump host reported: `ccr.ccs.tencentyun.com/youshunumind/numind-server:develop-9653409a running healthy`

## Remaining

- Push local `develop` to origin when GitHub connectivity recovers.
- Rerun `ndf-done` from `/private/tmp/wt-feishu-topic-context-mismatch-numind-server` to let it complete branch/worktree cleanup.
