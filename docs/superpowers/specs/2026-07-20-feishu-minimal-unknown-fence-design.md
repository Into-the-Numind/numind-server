# Feishu minimal unknown-result fence design

## State model

Each Agent run retains:

- one in-flight command state, preserving serial execution;
- the existing five-attempt correctable-error counter;
- a set of opaque fingerprints for exact write commands that returned `unknown_result`.

There is no run-global terminal write stop. A non-correctable read or write failure returns the closed structured result and resets the execution slot for the Agent's next decision. An unknown write additionally records its exact normalized-command fingerprint.

## Durable continuity

The fingerprint is a lowercase SHA-256 digest over server-normalized path, argv, and stdin. The closed result schema accepts it only for a business-started `unknown_result`. Error narration retains this internal result, and transcript persistence retains only the validated closed result with empty tool arguments. Resume seeding restores only trusted fingerprints; legacy unknown results without a fingerprint do not broaden the lock.

## Security boundary retained

This change does not weaken authentication, user/Agent binding, Feishu application binding, scope enforcement, the command allowlist, argument validation, no-shell execution, credential encryption, idempotency, or one-command-at-a-time execution. It removes only the run-wide ambiguity fence.
