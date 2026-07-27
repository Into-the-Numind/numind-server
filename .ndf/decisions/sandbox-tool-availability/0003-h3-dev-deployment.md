# H3 Dev Deployment

## Result

Dev deployment completed for `numind-server` at `develop-c1443129`.

## Evidence

- Image pushed: `ccr.ccs.tencentyun.com/youshunumind/numind-server:develop-c1443129`
- Registry digest: `sha256:67aea96bec3b7e1c60f8c6a8cefa88cd6f0c821c73e8b95f0bd1c9dce80547a9`
- Public health: `http://49.233.219.254:9091/healthz` returned `{"code":0,"message":"","data":{"status":"ok"}}`
- Container inspect: running and healthy
- Startup logs: sandbox pool initialized with `backend=docker`, `pool_min=5`, and `sandbox-skill:skills-v1.5.3`

## Scope

Production was not tagged or deployed.
