# S6 Dev deployment — Feishu binding continuity

Date: 2026-07-20

## Deployment

- Backend merged commit: `e2a0f2de`; Dev tag `develop-e2a0f2de`.
- Backend registry digest: `sha256:5b92ad2a6a1d4a4f70fc66b3fd6489eac01ac68ba4225706f43b24b068b1edcd`.
- Backend runtime image: `sha256:c8d3e09e9f53266a202e7ff72717cd7e173a2c61e6988ccde532c0fb39ce2255`.
- Frontend merged commit: `3c67cda`; Dev tag `develop-3c67cda`.
- Frontend registry digest: `sha256:ac516fffda4216c17abf76b4ec9352086b3dc673367837237c8fe6af3880e1c7`.
- Frontend runtime image: `sha256:b5d0b89499a1a940f8579ab47302e25b7ef27fbc95432c5e06cc7067501723ab`.
- Deployment order was backend first, then frontend, preserving the rolling compatibility contract.

## Dev verification

- `http://49.233.219.254:9091/healthz` returned the healthy backend payload.
- `http://49.233.219.254:9200/health` returned `healthy`.
- `http://49.233.219.254:9200/api/healthz` reached the backend through the frontend proxy and returned the healthy payload.
- `numind-server-dev` and `numind-web-v3-dev` both report `running|healthy` with the exact runtime images above.
- No `panic` or `fatal` entries appeared in either container's post-deployment logs.

## Acceptance boundary

Automated S5 gates remain ALL_PASS: full Go tests, focused race, Go lint, Vue lint/type-check, 1,135 unit tests, and the 10-case Playwright Feishu journey. The remaining S6 gate is real-user confirmation from user 438 that the existing stale card converges to the current authorization step and continues the original Agent task.

Production was not tagged or deployed.
