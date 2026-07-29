# S6 Dev deployment

- Feature: `feishu-write-command-guidance`
- Environment: Dev server
- Source commit: `ec09777778338f4475a952ad8ad2538279f2b712`
- Image: `ccr.ccs.tencentyun.com/youshunumind/numind-server:develop-ec097777`
- Registry digest: `sha256:4fccc0efcfe6db1242bec6c35334c1f192670be0e1aeed86db250edb399e7380`
- Deployment completed: `2026-07-30T00:40:00+0800`

## Evidence

- TCR manifest inspection confirmed the immutable SHA tag and digest.
- The Dev deployment pulled that digest and replaced `numind-server-dev`.
- The deployment script reported the new container healthy.
- An independent public request to `http://49.233.219.254:9091/healthz` returned `{"code":0,"message":"","data":{"status":"ok"}}`.

## Recovery note

The initial full release's build SSH channel remained open after the remote build and both image pushes had completed. Read-only checks confirmed that no remote build child remained and that the immutable image tag was present in TCR. The stale local SSH channel was terminated, then the standard `--deploy-only` path deployed the already-built immutable image. No rebuild or production deployment was performed.

## Acceptance boundary

Infrastructure smoke checks are complete. Human acceptance still needs one Agent 1 run on Dev against a suitable Base to verify the full read-guidance, Agent-authored inline JSON, and batch-write behavior. The eight results from the historical failed run were not automatically replayed.
