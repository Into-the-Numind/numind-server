# Remove Official Example Skill Design

## Current State

`20260617_180000_skill_3tier_visibility.sql` seeded a placeholder `skill` row:

- `name='官方示例技能'`
- `visibility='official'`
- `parent_user_id=0`
- `owner_user_id=0`

That row is globally visible by design of the `official` visibility predicate, so it appears for every parent account.

## Target State

The placeholder is removed. The Skill list should contain only institution-owned skills for the current parent account, plus any future real official skills intentionally created by admin/system workflows.

## Migration

Create `20260729_143500_remove_official_example_skill.sql` with a narrow delete predicate matching the seeded placeholder only.

## Test

Add a migration regression test requiring the delete migration to exist and include the expected safety predicates.
