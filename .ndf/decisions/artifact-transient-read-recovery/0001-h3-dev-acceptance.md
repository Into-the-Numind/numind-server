# H3 Dev acceptance — artifact transient read recovery

Date: 2026-07-20

## Outcome

- RED commit `5bdc5e14` reproduces Dev run 263: a persisted artifact could be read moments later, but its immediate visibility miss terminated the Agent run.
- Fix commit `ac441c23` converts only `gorm.ErrRecordNotFound` into a normal `note=not_found` result with one-retry guidance. Cross-user reads remain `not_accessible`; malformed input and real store failures remain errors.
- Focused tests, focused race, full `go test ./... -count=1`, `task lint`, YAML/diff hygiene, and self-review passed.
- Merge commit: `07dd9247`.
- Dev image: `develop-07dd9247`; registry digest `sha256:0d4fe854d817dfcd584fe3cdde27097e34c680128ca04ebe155aa39a870d4460`; runtime image `sha256:8246c586df9409f34d0cbc2b773345a2df7a1787659fd1929750e76461622c1a`.
- Public `/healthz` returned `code=0`, and the container reported `healthy`.
- Real Agent 2 run 266 fully read the uploaded v2 source and the existing Feishu profile, updated only the existing managed document, re-read it, and completed successfully.

Prod was not tagged or deployed.
