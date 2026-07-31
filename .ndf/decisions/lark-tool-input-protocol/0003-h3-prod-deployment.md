# lark-tool-input-protocol H3 Prod Deployment

## Summary

With explicit user approval, tagged and deployed the backend user API to production.

## Release

- Tag: `v2.1.75`
- Commit: `9efd1a7d`
- Image: `ccr.ccs.tencentyun.com/youshunumind/numind-server:v2.1.75-9efd1a7d`
- Digest: `sha256:0c667deab41e97c42bb694b0a6ceb208452df31732805368c2001d3806512d4a`
- Previous image reported by deploy script: `ccr.ccs.tencentyun.com/youshunumind/numind-server:v2.1.74-0f19951d`

## Verification

- `prod-secret-hygiene` passed before build.
- Prod secrets env-file validation passed on the deployment host.
- Release script reported `Deploy success: numind-server-prod is healthy`.
- Production host local health `http://localhost:9095/healthz` returned `{"code":0,"message":"","data":{"status":"ok"}}`.
- Docker reported `numind-server-prod ccr.ccs.tencentyun.com/youshunumind/numind-server:v2.1.75-9efd1a7d Up ... (healthy)`.
- Public gateway health `https://youshu.asia/api/healthz` returned `{"code":0,"message":"","data":{"status":"ok"}}`.

## Note

Direct bare-IP access from the local machine to `129.28.125.51:9095` timed out, so external acceptance used the public gateway URL plus production-host local health, matching prior backend deployment practice.
