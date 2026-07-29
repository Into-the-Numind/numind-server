# Remove Official Example Skill Requirement

## Background

After official templates were removed, the Skill list still showed `官方示例技能` because it is a separate system-seeded `skill` row, not a `skill_template` row.

## Problem

Parent accounts should not see the placeholder official example Skill in the configuration center.

## Scope

In scope:

- Delete the system-seeded `官方示例技能` Skill row.
- Keep tenant-owned institution Skills unchanged.
- Add a migration regression test so this placeholder is not accidentally preserved.
- Apply the migration to Dev.

Out of scope:

- Removing the `official` visibility enum.
- Removing future admin-owned official Skill capabilities.

## Acceptance Criteria

- `官方示例技能` no longer appears in `/v1/skills`.
- Dev database has no active `visibility='official'` Skill rows for this placeholder.
- Existing institution Skills remain visible to their owning parent account.
- Focused migration test and backend lint pass.
