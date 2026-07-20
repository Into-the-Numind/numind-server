# Proposal: exact-command ambiguity fence

Replace the run-global terminal stop with an exact-command fence. The server normalizes an allowlisted write command and stores only its SHA-256 fingerprint when Feishu returns `unknown_result`. The same normalized write is rejected on replay; reads and all different commands continue through the normal server-owned security pipeline.

The fingerprint is included only in the closed, server-produced terminal result so another worker can restore the narrow fence after authorization or process continuation. It contains no argv, content, user credential, resource URL, or token. Unknown results without a trusted fingerprint cannot create a broad fallback lock.

This deliberately favors task continuity. Feishu account binding, scopes, catalog policy, idempotency, serialization, and API-side version/recovery remain intact; only the overly broad post-result lock is reduced.
