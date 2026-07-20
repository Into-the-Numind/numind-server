# Feishu minimal unknown-result fence

## Problem

Dev run 255 proves that an ambiguous Docs write currently freezes the whole Agent run. After the first verification read fails, alternate Wiki reads and an unrelated Base create are rejected before reaching Feishu. The Agent then reports a partial result even though the remaining user request was still executable.

## Required outcome

- Fence only the exact Feishu write command whose outcome is `unknown_result`.
- Never let that fence block reads or a different Docs/Base/Wiki/Drive command.
- Allow multiple sequential read-verification strategies; an ordinary failed read must not close the run.
- Preserve the exact-command fence across Agent continuation without persisting model arguments or plaintext command content.
- Keep identity, account binding, official authorization, scope checks, catalog allowlisting, shell isolation, request integrity, idempotency, and single-command execution unchanged.
- Tell the Agent to verify ambiguous writes when practical and never claim an unverified write succeeded.

## Acceptance

The Run 255 regression must pass: unknown Docs update -> failed Docs fetch -> successful Wiki node read -> successful unrelated Base create. Repeating the exact ambiguous write remains blocked.
