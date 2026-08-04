# xhs-video-info-v2-fallback H1/H2 Verification

Date: 2026-08-04T15:37:05+0800

## Diagnosis

- Production collector zip and local zip matched before the fix, so the failure was not caused by a package rollback.
- Current Xiaohongshu video detail data exposes direct stream URLs under `video_info_v2.media.stream.h264/h265[*].master_url`.
- The existing collector only read the older `note.video.media.stream...` family, so video notes could still capture metadata and links while missing `video_url`.

## Commit Chain

- `e60d362 test(repro): reproduce xhs video_info_v2 fallback failure`
- `bdb6507 fix(xhs): capture video_info_v2 stream urls`

## Verification

- `npx vitest run --config extension/vitest.config.ts`: 37 passed
- `npm run check:xhs-extension`: passed
- `npm run lint`: passed with 0 errors and 7 existing warnings
- `npm run type-check`: passed

Packaged collector zip SHA256 after fix: `e382bb6f5ad69df2d53722e6f014233ee4704f36a6630d630ed1357e7d1d8525`

## Merge

- `ndf-done` merged `fix/xhs-video-info-v2-fallback` into `numind-web-v3` develop at `4c2ae56`.
- Worktree `/private/tmp/wt-xhs-video-info-v2-fallback-numind-web-v3` was removed and the local fix branch was deleted.

## Dev Deployment

- Deployed `numind-web-v3` develop `4c2ae56` to Dev as image `develop-4c2ae56`.
- Registry digest: `sha256:d02310d65db2e9b93b8280ab50e851eb97d492ba515cd50c30ef9f94cde3aeca`.
- Public Dev health check returned `healthy`.
- Downloaded Dev collector zip SHA256: `e382bb6f5ad69df2d53722e6f014233ee4704f36a6630d630ed1357e7d1d8525`.
