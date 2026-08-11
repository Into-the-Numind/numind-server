# xhs-video-collector-runtime H1/H2 Verification

Date: 2026-08-11T11:46:24+0800

## Diagnosis

- The supplied Xiaohongshu note `6a7993e60000000028032339` is a video note whose runtime state stores direct MP4 URLs in `note.video.media.stream.EF4` / `EF5`.
- The collector only checked named codec groups (`h264`, `h265`, `h266`, `av1`). It therefore missed the MP4 and later collection fallbacks could use the cover image instead, leaving no stored video or transcription input.
- The repair keeps named-codec priority and then examines every other stream group for `master_url` / backup MP4 URLs. The same logic is applied in both `lib/parse.js` and the MAIN-world page-state bridge.
- Every route that can populate `video_url` now uses the video-resource predicate: dynamic-state streams, HTML-text fallback, and the final payload assignment. JPG/WebP cover URLs are therefore rejected even when runtime state is unavailable.

## Commit Chain

- `8e2c490 test(qa): reproduce xhs EF stream video capture failure`
- `19610c1 fix(xhs): capture video urls from dynamic stream groups`
- `4f0bede fix(xhs): reject cover urls in video streams`
- `5112ecc fix(xhs): reject cover urls from all fallbacks`

## Verification

- The new customer repro test failed before the repair: expected the EF4 MP4 URL but received an empty video URL.
- `npm run test:unit`: 104 application test files (1,190 passed) plus the extension suite (40 passed); the extension suite is now part of the default command.
- The extension coverage includes EF4 cover-plus-backup-MP4, EF5, and HTML-only cover fallback; the final `payload.video_url` is asserted to be either the MP4 or empty, never an image.
- `npm run check:xhs-extension`: passed.
- `npm run lint`: passed with 0 errors and 7 pre-existing warnings outside this hotfix.
- `npm run type-check`: passed.
- Regenerated package `public/downloads/xhs-collector-extension.zip` SHA-256: `7045c661c6078b1ec41de8242d0010d13f36346a9bb9edd1188a02fe46ac8d41`.

## Scope

- No `src/` UI file changed, so browser UI QA is not applicable. The extension parser test exercises the exact stream shape from the supplied production note.

## H3 Production Release

- `ndf-done` merged the hotfix into `numind-web-v3` develop at `c58c964` and removed the worktree/feature branch.
- Tagged and deployed `v1.0.44`; production image: `ccr.ccs.tencentyun.com/youshunumind/numind-web-v3:v1.0.44-c58c964`.
- Registry digest: `sha256:dd7ed71c4d7b5df1e36e7cb5a85dff6a7ddedef00b95b293aa5dff4632829dc7`.
- Public `https://youshu.asia/health` returned `healthy`.
- Downloaded production collector ZIP SHA-256: `7045c661c6078b1ec41de8242d0010d13f36346a9bb9edd1188a02fe46ac8d41`, matching the released package. Its manifest is version `1.0.1` and contains the dynamic-stream and image-rejection parser code.

## Non-XHS Baseline Test

- `tests/unit/integration/SettingsView.spec.ts` has one failing assertion when run in isolation (`credits` expected, `free` received). The same failure reproduces on the pre-hotfix `develop` baseline and no relevant file changed in this repair, so it is tracked separately rather than modifying unrelated membership UI behavior during this incident response.
