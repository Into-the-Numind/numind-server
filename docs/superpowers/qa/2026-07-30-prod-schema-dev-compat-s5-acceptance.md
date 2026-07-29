# Prod schema Dev compatibility — S5 acceptance

Date: 2026-07-30  
Reviewed commit: `05f911c5`  
Migration SHA256: `f0622bde1f472e4e489c002507928cee86c79fff22cf89ee96d57ee44079669f`

## Result

PASS. The compatibility implementation is approved for the already-backed-up
Dev S6 rehearsal. Prod remains read-only.

## Verification evidence

- `go test ./...`: PASS.
- `go test -race ./migrations -count=1`: PASS.
- `task lint`: PASS after using the installed linter from `GOPATH/bin`.
- Isolated MySQL 8.4 exact/partial/negative/double-apply/data-protection runner:
  PASS.
- Dedicated temporary MySQL GORM legacy-NULL store test: PASS.
- Latest Prod SELECT-only preflight: 0 FAIL rows.
- Latest Dev SELECT-only preflight: 0 FAIL rows.
- Dev attachment state: `legacy_complete`, 45 rows, complete projection
  `e31cbb18332287caa421ef868f44ab627738e264f6aeb8f854f70f326df3fb76`.
- Dev Feishu proof state: 0 FKs, no orphans, exact compatible columns, 3 rows,
  projection
  `32767affa9c3e95724b06bbb0ad3825a554b3fe9b5c68c667e00a51bc9da2ebd`.
- Dev backup rechecked: 193,990,940 bytes, gzip valid, SHA256
  `91da31971f4d097c2caed8971129e5b47fd71d21250d57f3d38438ea5db4a18b`.
- Independent specification review: PASS, P0=0, P1=0, P2=0.
- Independent engineering/safety review: PASS, P0=0, P1=0, P2=0.

## S6 handoff

Merge with `ndf-done`, then execute the migration twice on Dev, compare all
protected projections/checksums/row counts, deploy both Dev APIs, and compare
the complete attachment/proof projections again after full service startup.
Do not perform any Prod write or deployment.
