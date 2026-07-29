# T2 Sandbox Lease Journal

Date: 2026-07-30

## Context

The Sandbox broker runs on the existing Prod host but must recover interrupted
container work without writing lifecycle state into the Prod customer database.
The journal is therefore a separate, content-free fact source owned only by the
`numind-sandbox` operating-system user.

## Decisions

1. Store leases in a dedicated SQLite database with WAL,
   `synchronous=FULL`, `busy_timeout=5000`, foreign keys, and one database
   connection.
2. Require a private `0700` parent directory and `0600` database and lock
   files. Reject symlink, non-regular, or group/world-accessible targets.
3. Hold a non-blocking file lock for the lifetime of the journal so a second
   broker cannot serve the same state.
4. Keep `lease_event` append-only and use globally unique request IDs plus
   request fingerprints for idempotency conflicts.
5. Treat the lease ID and absolute timestamps as broker-generated results.
   Creation retries compare authenticated owner identity and requested TTL, so
   a response-loss retry returns the first lease even if the handler generated
   a fresh candidate lease ID.
6. Create every lease unbound. Only `ready -> active` can set the positive
   product run and Sandbox session IDs; later transitions cannot change the
   binding or container identity.
7. Use explicit legal transitions and separate state-specific stale cutoffs
   for expiry, heartbeat, and incomplete external actions.
8. Bound every list query to at most 1,000 rows.
9. Store only identifiers, lifecycle states, timestamps, resource counters,
   and bounded reason codes. Do not store files, prompts, commands, outputs,
   environment values, credentials, or customer records.

## Verification

- Focused acceptance test passes with the race detector.
- Full package race test passes.
- Twenty repeated focused race-test runs pass.
- A subprocess exits without closing SQLite; the parent then reopens the WAL
  database and verifies both the lease and append-only events.
- Concurrent create replay, lock exclusion, unsafe-path rejection, state
  invariants, append-only triggers, and inclusive stale boundaries are covered.
- Repository `task lint` passes.

