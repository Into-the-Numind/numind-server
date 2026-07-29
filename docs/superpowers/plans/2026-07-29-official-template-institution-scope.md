# Official Template Institution Scope Implementation Plan

## Track

Standard, backend only:

- `numind-server`

## Tasks

1. Add NDF requirement, proposal, design, plan, and manifest entry.
2. Add migration and rollback for official template removal and imported-skill repair.
3. Change `ImportTemplate` to create institution-scoped skills.
4. Update comments that still describe import-template as an official creator.
5. Expand import-template regression tests for institution visibility and cross-tenant isolation.
6. Run focused tests for Skill artifact behavior.
7. Run backend lint and broader tests as practical.
8. Merge with `ndf-done`.

## Verification

Focused:

- `go test ./internal/numind/controller/v1/agent -run TestSkillArtifact_ImportTemplate_HappyPath`
- `go test ./internal/numind/biz/skill/artifact`

Backend gate:

- `go test ./...`
- `task lint`

Data:

- Manual read of migration confirms `skill_template` rows are deleted.
- Manual read of migration confirms only tenant-owned imported `official` rows are repaired.
