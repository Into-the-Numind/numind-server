# Prod schema reconcile — S5 verification

Date: 2026-07-30  
Reviewed commit: `0a9040b2`

## Result

S5 passed. The schema package is ready for the S6 Dev database rehearsal and
product smoke. No Dev or Prod database write was performed during S5.

## Evidence

- Both independent S4 reviewers returned PASS with P0=0, P1=0, P2=0.
- `go test ./... -count=1` passed.
- `task lint` passed, including `go vet` and `golangci-lint`.
- `go test -race ./migrations -count=1` passed.
- The isolated build-host MySQL 8.4 runner passed exact-schema, interrupted
  partial retry, malformed-schema/index/FK/CHECK negative cases, double apply,
  constraint enforcement, and protected-data invariance.
- Real Prod preflight was SELECT-only and returned no FAIL rows:
  - required base tables: 20/20;
  - historical subscription rows: 102;
  - protected subscription SHA256:
    `a49eab0a594e92838c1c15311b870a3b3b77dc118571811cea629d7f08e413e8`;
  - trial grants: 69;
  - credit cycles: 88;
  - booster balances: 9;
  - membership events: 278;
  - full unique-index and notification FK column contracts passed.
- Reviewed migration SHA256:
  `44e4a6e3afd969f408cccd1997f9997272e2347c15c209581006954a2758aa58`.

## S6 gate

Before deploying the new Dev backend:

1. create and verify a recoverable Dev database backup;
2. run the read-only preflight and retain its full output;
3. apply the reviewed migration twice;
4. run verify after each apply and compare all protected evidence;
5. deploy the exact reviewed backend image;
6. smoke existing balances and membership plus SOP, chatbot, Agent attachments,
   document, notification, Feishu, and Qwen image understanding.

Prod remains read-only until a separate final authorization.
