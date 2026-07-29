# Remove Official Example Skill Proposal

## Summary

Remove the placeholder system official Skill seeded by the 3-tier visibility migration.

## Design

Add a forward migration that deletes from `skill` where:

- `visibility='official'`
- `parent_user_id=0`
- `owner_user_id=0`
- `name='官方示例技能'`
- `source_type='custom'`

The predicate is intentionally narrow so tenant skills and marketplace/imported skills are not affected.

## Rollback

The rollback re-inserts the same placeholder row only if it does not already exist.

## Verification

- Migration content regression test.
- Migration dry run on a temporary Dev MySQL database.
- Apply migration to Dev and verify `/v1/skills` no longer contains `官方示例技能`.
