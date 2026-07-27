# S6 Dev Deployment — standard-office-output-tools

Date: 2026-07-27

## Result

PASS.

## Deployment

- Merged to `develop` with merge commit `5424868c`.
- Pushed `origin/develop`.
- Built and deployed image `ccr.ccs.tencentyun.com/youshunumind/numind-server:develop-5424868c`.
- TCR digest: `sha256:70c983989c0be80dcc60144def6380b0b25b283e88e84ead51c741bab1be5e38`.

## Dev checks

- Public `http://49.233.219.254:9091/healthz` returned `{"status":"ok"}`.
- Docker inspect reported `numind-server-dev` running and healthy on image `develop-5424868c`.
- Startup logs show `sandbox pool initialized` with `pool_min=5`.
- Runtime tool metadata upsert shows:
  - `create_docx`: `requires_sandbox=false`
  - `create_xlsx`: `requires_sandbox=false`
  - `create_pptx`: `requires_sandbox=false`
  - `run_python`: `requires_sandbox=true`
  - `bash_exec`: `requires_sandbox=true`
- Docker listed five current sandbox containers with the new backend container owner label.

## Scope

Prod remains untouched. User acceptance and any later prod tag/release are separate steps.
