# Feishu Keychain App Evidence Design

## Root cause

Pinned lark-cli 1.0.68 calls its secure-storage adapter after app creation and serializes `appSecret` as:

```json
{"source":"keychain","id":"appsecret:cli_created"}
```

Numind's controlled completion probe reuses a legacy struct whose `appSecret` field is `string`. JSON decoding therefore fails after the real CLI has already created the application. The worker marks the session failed before sealing its HOME, and `resume` later reports that terminal failure.

## Design

Add a private controlled-evidence decoder near `AppIDFromHome`:

1. Read `.lark-cli/config.json` with the existing file-size limit.
2. Strictly decode the top-level config and first application evidence.
3. Require a non-empty `appId`.
4. Decode `appSecret` as either:
   - a non-empty JSON string, retained only for backward compatibility; or
   - an object with exactly `source=keychain` and `id=appsecret:<appId>`.
5. Return only `appId`; never surface the secret or reference.

The official keychain object is strict: duplicate, case-variant, unknown, empty, wrong-source, and mismatched-ID fields are rejected. Existing lifecycle and legacy plaintext-secret parsing remain unchanged.

## Persistence invariant

`EncryptedCLIHomeVault` already archives the complete controlled HOME recursively. A regression test will prove that the config plus `.local/share/lark-cli/master.key` and encrypted app-secret file survive seal/open byte-for-byte. This makes the expected secure-storage boundary executable rather than implicit.

## Failure behavior

Any invalid evidence fails before account finalization. The session remains terminally failed and no unverified app ID is committed. No user-visible secret or provider payload is added to API responses or logs.
