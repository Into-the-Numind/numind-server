# Official Template Institution Scope Design

## Current State

`skill_template` stores built-in official template cards. `POST /v1/skills/import-template` compiles a selected template into a concrete `skill`.

The current import implementation creates that concrete skill through an internal trusted path with `visibility='official'`. Since `official` is globally visible, a tenant import can be listed by other institutions.

## Target State

Official templates are removed, and template import is institution-scoped:

| Field | Target value |
| --- | --- |
| `skill.visibility` | `institution` |
| `skill.parent_user_id` | importing parent account ID |
| `skill.owner_user_id` | importing parent account ID |
| `skill.source_type` | `imported_from_template` |
| `skill.origin_type` | `official` for legacy provenance |
| `skill.source_template_id` | selected template ID |

The source template remains a source record only. The imported skill is an independent editable asset in the importing institution.

## Data Migration

Create a forward migration that:

- Deletes all rows from `skill_template`.
- Updates active or inactive tenant-owned imported skills that are currently marked `official` back to `institution`.
- Leaves system-owned official skills alone by requiring `parent_user_id <> 0` for the repair update.

## Service Semantics

`ImportTemplate` remains parent-only via the controller guard and service `parentUserID == 0` check.

The method continues to compile `body_md` from `skill_template` content, but calls `createWithVisibility` with `VisibilityInstitution` instead of `VisibilityOfficial`.

## Visibility Contract

After import:

- Importing parent account can list/read/edit the skill.
- Users in other institutions cannot list/read the skill.
- System official skills where `parent_user_id=0` remain globally readable and read-only.

## Tests

Extend the import-template HTTP test to assert:

- created skill visibility is institution
- parent/owner ids are the importing parent
- same parent can list/read it
- another parent cannot list/read it

Run the focused controller test and relevant artifact package tests.
