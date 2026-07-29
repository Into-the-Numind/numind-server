# S6 Dev Deployment — feishu-stability-recovery

Date: 2026-07-29

## Deployment

- Merge commit: `a7ad5a3d`
- Image: `ccr.ccs.tencentyun.com/youshunumind/numind-server:develop-a7ad5a3d`
- Digest: `sha256:b9103252fdd9584f11dfc6237a56139d910260c4b14e9e63c9e841325d235af8`
- Target: `numind-server-dev`

## Verification

- `bash scripts/cicd/release.sh dev server` completed successfully.
- Deploy script reported `numind-server-dev` healthy.
- Public smoke check passed: `curl -fsS http://49.233.219.254:9091/healthz` returned `{"status":"ok"}` inside the response data.

## Notes

- `ndf-done` initially hit transient GitHub HTTPS errors. A repo-local `git config http.version HTTP/1.1` made pull/push stable, then `ndf-done` completed normally.
