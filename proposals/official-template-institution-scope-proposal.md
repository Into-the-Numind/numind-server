# Official Template Institution Scope Proposal

## Summary

Remove all platform official templates and make template import produce institution-owned skills instead of globally visible official skills.

## Product Rules

- The official template library no longer offers any built-in templates.
- A concrete skill created from a template belongs to the importing parent account.
- Imported skills are regular institution assets and can be edited/deleted by that parent account.
- System-level official skills remain possible for future platform-owned content, but only rows with `parent_user_id=0` should be global.

## Backend Design

Add a data migration:

- `DELETE FROM skill_template` to clear official templates.
- Convert tenant-owned imported skills from `visibility='official'` to `visibility='institution'` when `parent_user_id <> 0`.

Change import behavior:

- Keep `source_type='imported_from_template'`.
- Keep `source_template_id` as historical provenance.
- Preserve legacy `origin_type='official'` for imported-from-template rows.
- Set `visibility='institution'`.
- Set `parent_user_id` and `owner_user_id` to the importing parent account ID.

## Compatibility

- Empty template library responses are valid and require no API contract change.
- Existing imported skills remain in place; only visibility is corrected.
- Existing system-level official skills are untouched.

## Rollout

The backend migration is safe to run repeatedly. The application behavior is changed in code and guarded by tests. Dev deployment can be done after merge if requested.
