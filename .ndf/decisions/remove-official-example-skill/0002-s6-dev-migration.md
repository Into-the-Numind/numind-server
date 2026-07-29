# S6 Dev Migration - Remove Official Example Skill

## Summary

The placeholder official example Skill was removed from Dev.

## Git

- Merge commit: `8a12555c`

## Dev Migration

Applied `migrations/20260729_143500_remove_official_example_skill.sql` to `numind-dev`.

Post-migration DB checks:

- active `官方示例技能` count: `0`
- active Skill visibility counts: `institution=8`
- no active `official` Skill rows remain

## API Smoke

Using the E2E parent account:

- `GET /v1/skills?page=1&page_size=50` returned `total=7`
- returned names did not include `官方示例技能`
- returned rows contained no `visibility='official'`

## Result

PASS. The Skill page should no longer show the placeholder official Skill after refresh.
