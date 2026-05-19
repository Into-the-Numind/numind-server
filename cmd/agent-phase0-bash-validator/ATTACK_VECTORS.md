# ATTACK_VECTORS.md — V3 Bash Validator Test Matrix

This document defines the 20 canonical attack vectors for the 8 P0 Bash validators.
It serves as both documentation and the ground truth for `TestAttackMatrix` in `validator_test.go`.

## Validator ID Reference

| ID | File | Detection Target |
|----|------|-----------------|
| `ControlChar` | `control_char.go` | ASCII 0x00–0x1F (excl. `\t \n \r`) + DEL (0x7F) |
| `Unicode` | `unicode.go` | U+202E RTL / U+00A0 NBSP / U+200B,200C,200D,FEFF zero-width |
| `CR` | `carriage_return.go` | Lone `\r` not followed by `\n` |
| `CommandSubstitution` | `command_substitution.go` | `$(...)` / backtick / `<(...)` / `>(...)` |
| `IFS` | `ifs.go` | `$IFS` / `${IFS}` / ANSI-C quoting `$'...'` |
| `ProcEnviron` | `proc_environ.go` | `/proc/*/environ\|cmdline\|maps\|status\|fd` |
| `BackslashOperator` | `backslash_operator.go` | `\x` / `\u` / `\0` escapes inside `echo -e` or `printf` |
| `BraceExpansion` | `brace_expansion.go` | `{a,b,c}` list / `{a..z}` or `{1..N}` range / nested depth ≥ 2 |

---

## Attack Vector Matrix (20 cases)

| # | Attack Vector | Expected Validator | Expected Decision | Notes |
|---|---|---|---|---|
| 1 | `rm -rf /` | (none — not V3 scope) | **Allow** | V3 does not check command semantics; allowlist/blocklist is a separate layer |
| 2 | `cmd\x00rm -rf /` | `ControlChar` | **Deny** | Null byte (0x00) can split or hide commands in some parsers |
| 3 | `ls<RTL>.exe` (contains U+202E) | `Unicode` | **Deny** | RTL Override makes filename appear as innocent text to human reviewers |
| 4 | `echo hi\rrm /` | `CR` | **Deny** | Bare `\r` causes bash to treat second part as a new command |
| 5 | `echo $(id)` | `CommandSubstitution` | **Deny** | `$(...)` executes `id` and substitutes output |
| 6 | `` echo `id` `` | `CommandSubstitution` | **Deny** | Backtick form of command substitution |
| 7 | `cat <(id)` | `CommandSubstitution` | **Deny** | Process substitution — runs `id`, feeds output as a file |
| 8 | `cat${IFS}/etc/passwd` | `IFS` | **Deny** | IFS-based word splitting bypasses naive space-detection filters |
| 9 | `$'\trm'` | `IFS` | **Deny** | ANSI-C quoting injects a tab character to confuse parsers |
| 10 | `cat /proc/self/environ` | `ProcEnviron` | **Deny** | Reads the current process's environment variables (may contain API keys) |
| 11 | `cat /proc/1/cmdline` | `ProcEnviron` | **Deny** | Reads init process command line — can reveal secrets passed as args |
| 12 | `echo -e '\x72\x6d'` | `BackslashOperator` | **Deny** | `\x72\x6d` decodes to `rm` — obfuscates the real command |
| 13 | `printf '\x72\x6d /'` | `BackslashOperator` | **Deny** | Same `\x` encoding via `printf` |
| 14 | `rm /{a..z}/{1..1000}` | `BraceExpansion` | **Deny** | Range expansion generates 26,000 argument paths |
| 15 | `echo {a,{b,{c,d}}}` | `BraceExpansion` | **Deny** | Nested brace expansion; exponential argument count |
| 16 | `python -c print("hello")` | (none) | **Allow** | Normal Python one-liner; no dangerous patterns |
| 17 | `ls -la /home` | (none) | **Allow** | Standard directory listing |
| 18 | `echo $HOME` | (none) | **Allow** | Plain variable expansion — V3 does not block `$VAR` |
| 19 | `cat file.txt \| grep foo` | (none) | **Allow** | Pipe is not blocked by V3 validators |
| 20 | `head -n 5 /etc/hostname` | (none) | **Allow** | Reads a safe, non-sensitive file |

**Summary**: 6 Allow + 14 Deny = 20 total.

---

## False Positive Acknowledgements

V3 is a v1 prototype designed for zero false negatives on the 20 canonical vectors.
Known acceptable false positives:

| Pattern | Validator | Why Triggered | Mitigation in v2 (#6) |
|---------|-----------|---------------|----------------------|
| `echo ${HOME}` | None — `${VAR}` not `${IFS}` | Correctly allowed | N/A |
| `printf '%s\n' hello` | `BackslashOperator` may trigger | `\n` is `\` + `n` after `printf` | Context-aware parser in #6 |
| `echo {{tag}}` (template syntax) | `BraceExpansion` | Depth-2 brace nesting | Allowlist template patterns in #6 |

---

## Spec Reference

Spec: `docs/superpowers/specs/2026-05-20-agent-mode-phase0-verification-design.md` §4.4
Feature: `agent-mode-phase0-verification` (Standard track)
