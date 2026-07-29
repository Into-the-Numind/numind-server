# S7 Dev Deployment - Weekly Membership

Date: 2026-07-29

## Outcome

Weekly membership is merged to `develop`, pushed to origin, deployed to Dev, and health-checked across all three repositories.

## Deployment

- `numind-server`: deployed to Dev from `9ddd82b4`
- `numind-web-v3`: deployed to Dev from `6f0e4e8`
- `numind-admin-web`: deployed to Dev from `2a0a03b`

## Health Checks

- Backend `http://49.233.219.254:9091/healthz`: OK
- User frontend `http://49.233.219.254:9200/health`: HTTP 200
- Admin frontend `http://49.233.219.254:9100/health`: HTTP 200

## Notes

GitHub push temporarily failed for the frontends during `ndf-done` because `github.com:443` timed out. After Dev deployment, both frontend `develop` branches were pushed successfully and the leftover weekly-membership worktrees were removed.
