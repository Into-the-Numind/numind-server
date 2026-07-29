# Weekly Membership Implementation Plan

## Track

Standard, across:

- `numind-server`
- `numind-web-v3`
- `numind-admin-web`

## Tasks

1. Add backend constants, schema migration, and subscription model fields.
2. Implement weekly subscription grant/reopen/renew behavior.
3. Generalize credit-cycle bounds and credit grants by subscription plan.
4. Include weekly paid events in B2B billing reports.
5. Expand grant-membership controller API validation and response.
6. Add backend regression tests.
7. Add weekly controls and labels to user frontend.
8. Add weekly labels to admin frontend.
9. Run backend lint/tests and frontend lint/type-check.
10. Merge with `ndf-done`.
11. Deploy Dev backend, user frontend, and admin frontend.

## Verification

Backend:

- Focused membership tests
- Focused B2B billing tests
- Controller grant-membership tests
- `go test ./...`
- `task lint`

User frontend:

- `npm run lint`
- `npm run type-check`

Admin frontend:

- `npm run lint`
- `npm run type-check`

Dev:

- backend health check after deploy
- user frontend deployment script completion
- admin frontend deployment script completion
