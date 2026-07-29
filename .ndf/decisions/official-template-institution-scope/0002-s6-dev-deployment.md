# S6 Dev Deployment - Official Template Institution Scope

## Summary

The backend change was merged, pushed, migrated on Dev, deployed to Dev, and smoke verified.

## Git

- Merge commit: `fd9ec5e5`
- Image: `ccr.ccs.tencentyun.com/youshunumind/numind-server:develop-fd9ec5e5`

## Dev Migration

Applied `migrations/20260729_141000_remove_official_skill_templates.sql` to `numind-dev`.

Post-migration checks:

- `skill_template` count: `0`
- active `official` skill count: `1`
- `官方示例技能` remains `official`
- tenant imported `学员爆款分析师` rows are `institution`

## Dev Deploy

`bash scripts/cicd/release.sh dev server` completed successfully.

Health checks:

- `GET http://49.233.219.254:9091/healthz` returned `status=ok`
- `numind-server-dev` is running image `develop-fd9ec5e5` and Docker health is `healthy`

## API Smoke

Using the E2E parent account:

- `GET /v1/agent/skill-templates` returned `total=0`
- `GET /v1/skills?page=1&page_size=50` returned imported template skills as `visibility='institution'`
- only `官方示例技能` remained `visibility='official'`

## Result

PASS. Dev is ready for user acceptance. Production was not touched.
