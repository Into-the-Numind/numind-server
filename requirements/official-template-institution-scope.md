# Official Template Institution Scope Requirement

## Background

The Skill configuration center currently has two related concepts:

- `skill_template`: platform-provided official templates shown in the official template library.
- `skill`: concrete skill assets, with `visibility` controlling who can see each asset.

The current import-template path creates a concrete `skill` with `visibility='official'`. That makes a tenant-created imported skill globally visible to every parent account, which is not the intended ownership model.

## Problem

Official templates should be removed from the product surface. If template import is ever used again, the imported result must be an independent institution-owned skill:

- It must not mutate or replace the source official/template record.
- It must not become globally visible.
- It must carry the importing parent account as `parent_user_id`.
- It must use `visibility='institution'`.

Existing tenant-owned imported skills that were incorrectly marked `official` should be corrected so they stop leaking across institutions.

## Scope

In scope:

- Delete active official template rows from `skill_template`.
- Change `ImportTemplate` so imported skills are institution-scoped.
- Repair existing tenant-owned imported skills with `visibility='official'` to `visibility='institution'`.
- Add regression tests for import visibility and cross-tenant isolation.

Out of scope:

- Removing system-level official skills where `parent_user_id=0`.
- Removing the template API endpoint or UI button.
- Adding a new admin template-management surface.
- Production deployment unless separately requested.

## Acceptance Criteria

- `skill_template` is empty after the new migration runs.
- Importing a template creates `skill.visibility='institution'`.
- Imported skill has `parent_user_id` equal to the importing parent account ID.
- Imported skill remains editable by its importing parent account.
- Another parent account cannot list or read the imported skill.
- Existing tenant-owned imported official skills are migrated back to institution visibility.
- System official skills with `parent_user_id=0` remain official and globally visible.
- Focused backend tests and lint pass.
