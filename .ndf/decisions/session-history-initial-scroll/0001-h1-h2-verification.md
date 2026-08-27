# Session history initial scroll — H1/H2 verification

## Symptom

The deployed Dev page fetched `offset=100` before any user scroll and left the 150-run fixture anchored at Run 051 instead of the newest Run 150.

## Root cause and fix

The browser initially renders a scroll container at `scrollTop=0`; smooth initial positioning let that transient state enter the older-history threshold. Pagination is now gated until the viewport has first been positioned at the newest edge, and that restoration write is immediate rather than animated.

## Evidence

- Red: the new mocked Playwright regression received snapshot offsets `[0, 100]` while expecting `[0]`.
- Green: the same regression passed once and then five consecutive times; it also verifies the first viewport is at the bottom and the later user upward scroll loads `offset=100` without anchor drift.
- `npm run lint`: exit 0 with seven unrelated pre-existing warnings and zero errors.
- `npm run type-check`: exit 0.
- `npm run test:unit`: 1,190 application tests plus 40 extension tests passed; existing skips/todos unchanged.
- `npm run build`: exit 0; only existing chunk-size warnings.

## H3 Dev verification

- Merged frontend develop commit: `1848f9b`.
- Deployed image: `develop-1848f9b`, registry digest `sha256:842475d8ca7f1fe5cfde9f5d2aa8d78ef87432fc1a4bdb70d30e76ec1771609d`.
- Dev container and external `/health` endpoint are healthy.
- Before any user scroll, the authenticated 150-run fixture makes only the default snapshot request, renders exactly Run 051-150, and sits at `distanceFromBottom=0` with Run 149-150 visible.
- On upward scroll, it requests `offset=100&limit=100`, renders Run 001-150, and retains Run 051 with `0px` anchor drift.
- Browser console errors: none.
