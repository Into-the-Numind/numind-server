# S5 Verification - Official Template Institution Scope

## Summary

Backend implementation and data migration were verified locally and against a temporary Dev MySQL database.

## Checks

- `go test ./internal/numind/controller/v1/agent -run TestSkillArtifact_ImportTemplate_HappyPath -count=1` passed.
- `go test ./internal/numind/biz/skill/artifact -count=1` passed.
- `go test ./...` passed.
- `PATH="$(go env GOPATH)/bin:$PATH" GOPROXY=https://goproxy.cn,direct task lint` passed.
- `git diff --check` passed.

## Migration Dry Run

The forward migration was applied to a temporary database copied from Dev table schemas and representative data:

- `skill_template` count changed from `10` to `0`.
- System official skill `官方示例技能` stayed `visibility='official'`.
- Tenant imported skill `学员爆款分析师` changed from `visibility='official'` to `visibility='institution'`.

The rollback migration was applied to a separate temporary database and restored `10` template rows.

## Result

PASS. The imported-template path now creates institution-scoped skills, and the migration removes official templates without demoting system-owned official skills.
