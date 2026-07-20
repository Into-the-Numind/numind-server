# Feishu minimal unknown-result fence implementation plan

1. Commit a failing Run 255 regression before production code.
2. Add a closed optional write-fence fingerprint to Feishu terminal results.
3. Replace the global terminal stop with exact-command unknown fences and keep reads/different commands live.
4. Persist and restore validated unknown fingerprints across continuation.
5. Update Agent policy and affected tests, then run focused Go tests and `task lint`.
6. Merge/push with `ndf-done`, deploy the backend to Dev, and verify the exact container and health endpoint.
