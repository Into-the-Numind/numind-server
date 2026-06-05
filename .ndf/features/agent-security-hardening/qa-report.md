# QA Report — agent-security-hardening (S5)

> Feature: `agent-security-hardening`. Spec: `docs/superpowers/specs/2026-06-05-agent-security-hardening-design.md`.
> Branch: `feature/agent-security-hardening`. Date: 2026-06-05.
> Strategy: T6 (Go persistent regression primary + prod-shape build + dev black-box at S6). Rule 10 (security high-risk → persistent Go regression).

## S5 Gate Results

| Check | Result |
|-------|--------|
| `task lint` (go vet + golangci-lint, whole repo) | **PASS** (exit 0) |
| `go test ./...` (whole repo) | **PASS** except pre-existing `cmd/numind` + `internal/numind/biz/memory` failures — this branch touches **neither** package (verified via `git diff --name-only`); documented pre-existing on clean develop (b2b2c S5-D9). |
| `go test -race` on ALL changed packages (agent, bashvalidator, compliancegate, permission, permission/validators) | **PASS** (race-clean) |
| Prod-shape binary build `go build ./cmd/numind` | **PASS** (78MB) — confirms enforce gate + SoftDenyController + 6 semantic validators wire into the real NON-test binary (the BLK-1 lesson: test.v ≠ prod). |

## Persistent regression coverage (the protection that stays)

- **BLK-1 (T1)**: `permission/gate_test.go::TestGate_Check_EnforceFlagGovernsPipeline` — default gate enforces (Deny honored) WITHOUT relying on test.v; `WithEnforce(false)` force-allows. Rule 11 repro chain preserved (`test(qa): reproduce…` → `fix:`).
- **Soft interception (T2/T3)**: `soft_deny_test.go` (10 cases: 3-threshold trip, R2-B lifetime bypass, OnSuccess reset, nil-pending, ctx) + `soft_deny_adapter_test.go` (9 cases: registry-hygiene LastAction==Continue, trip→PermissionDeny, OnSuccess wired, disabled/no-controller fallback, hard-stops unaffected, compliance-source reason, escalation). All `-race` clean.
- **SSRF (T4)**: `ssrf_reuse_test.go` — web_fetch SSRF soft-result (incl IPv6 [::1]); run_python downloadInputFile blocks internal (incl IPv6) + allows public; TOCTOU closed via newSafeHTTPClient (dial-time IP recheck).
- **Platform bans (T5)**: `bashvalidator/semantic_validators_test.go` — 6 validators, **double-sided** (dangerous→Deny + normal→Allow) incl. the adversarial-review FN/FP fixes: rm catches `/home`/`/usr`/`/etc`/`//` but not `/tmp`; SSRFLiteral host-anchored (no `…/localhost` path FP); CredentialFile `.ssh` key-files only (no `~/.ssh/config` FP), `.env` verb-gated (no `echo "…env…"` FP).

## 不误伤 evidence (bidirectional, the product's core concern)

Every new rule has Allow cases proving normal usage passes: `rm -rf /tmp/build`, `rm -rf $HOME/cache`, public curl (`8.8.8.8`, `172.15.x`), `curl …/localhost` (path), `cat .env.example`/`.envrc`, `grep ~/.ssh/config`, `ls ~/.ssh/`, `dd if=a of=/tmp/b`, `curl -o f url` (no shell), `echo "rm -rf /"`.

## Open items carried to S6 (dev black-box) + follow-ups

- **O1 (user-decided: measure at S5/dev)**: enforce=true activates the EXISTING 8 Phase-0 checkers (`CommandSubstitution` blocks all `$()`, `BraceExpansion` blocks `{a,b}`). S6 dev: run the normal-bash sample set (`echo "$(date)"`, `ls {a,b}`, `for {1..3}`, `RESULT=$(cat f)`, `tar czf o {a,b}`); if blocked → bring data to user for the §2.2 accept/relax decision. Soft interception buffers the impact (LLM gets a rewrite hint, run continues).
- **Dev black-box (S6)**: deploy dev → confirm healthz + grep dev logs for `agent permission gate wired enforce=true` (prod-shape BLK-1 confirmation in the real dev binary) + best-effort seed-agent security smoke (dev agent链路 has known 422/model_error env limits — honest caveat).
- **Documented gaps (sandbox-mitigated, bash `--network=none`)**: SSRFLiteral misses hex/decimal-encoded IPs (`0x7f000001`); `rm -rf $VAR` (var-expansion) not text-detectable. Both backstopped by sandbox ephemerality + network isolation.

## Verdict
S5 backend verification **PASS**. Persistent Go regression comprehensive + race-clean + prod-shape build green. Ready for S6 (ndf-done merge → dev deploy + black-box).
