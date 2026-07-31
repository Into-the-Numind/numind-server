# lark-tool-input-protocol H3 Dev Deployment

## Summary

Merged `fix/lark-tool-input-protocol` into `develop` and deployed the dev server image that contains both the prior `lark_execute` B+ hardening and this companion-tool protocol hardening.

## Deployment

- Commit: `c8f8dbbb`
- Image: `ccr.ccs.tencentyun.com/youshunumind/numind-server:develop-c8f8dbbb`
- Digest: `sha256:c72ed8d077eb716c92a8dc9b39de1805d2294425c079c7cfe5b32d34e9bf1aaa`
- Release command: `bash scripts/cicd/release.sh dev server`

## Verification

- Release script reported `Deploy success: numind-server-dev is healthy`.
- External health check `curl -fsS http://49.233.219.254:9091/healthz` returned `{"code":0,"message":"","data":{"status":"ok"}}`.
