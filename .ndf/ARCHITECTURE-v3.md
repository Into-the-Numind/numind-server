# NDF v3 Architecture: per-worktree state

*Effective from 2026-05-22. Replaces the v2 central `state.json` design.*

## Why v3

NDF v2 used a single central state file at `numind-server/.ndf/state.json`. Two
production incidents in May 2026 traced back to that design:

- **Incident A (2026-05-21 morning).** Session A ran `ndf-micro foo`; worktree
  was set up. Session B, working on a different feature, ran `ndf-start ... --force`
  which silently overwrote `state.json` with B's feature ID. Session A kept editing,
  later ran `ndf-done`, which read the stale-from-A's-perspective `state.json` and
  almost merged the wrong branch.

- **Incident B (same day).** Session A then tried `ndf-start --force` to reclaim
  its slot. `ndf-start.sh` saw the existing worktree path on disk and ran
  `rm -rf` against it — wiping approximately three uncommitted `.vue` files. Git
  knew nothing because the work was never staged.

Both incidents share the same root: state living in **one shared mutable place**
means any session can step on another's work, and `ndf-start --force` had no
notion of "is there unsaved work in that directory?"

## What v3 changes

State moves into each worktree itself:

```
/private/tmp/wt-<slug>-<repo>/
  ├── .ndf-active             ← single JSON file describing this worktree's feature
  └── (all the code)
```

There is **no central state.json**. The 5 v2 status tables in
`numind-server/.ndf/` reduce to:

```
numind-server/.ndf/
  ├── manifest.yaml           ← UNCHANGED — feature ledger
  ├── decisions/<id>/         ← UNCHANGED — ADRs
  ├── archived/<yyyy-mm>.yaml ← UNCHANGED — completed > 7 days
  └── (no state.json — archived at v2-archive-<date>.json after migration)
```

### `.ndf-active` schema

Same shape as v2's `state.json.active` block, with a top-level `version: "ndf-v3"`:

```json
{
  "version": "ndf-v3",
  "id": "<feature-slug>",
  "track": "micro|hotfix|standard",
  "stage": "M1|H1|H2|H3|S0|...|S7",
  "created_at": "2026-05-22T00:03:01+0800",
  "repos": ["numind-server", "numind-web-v3"],
  "worktrees": {
    "numind-server":  "/private/tmp/wt-foo-numind-server",
    "numind-web-v3":  "/private/tmp/wt-foo-numind-web-v3"
  },
  "branches": {
    "numind-server": "feature/foo",
    "numind-web-v3": "feature/foo"
  },
  "review_policy": "none|single|dual-parallel",
  "blockers": []
}
```

For **cross-repo features** the *same* JSON content is written into every
worktree's `.ndf-active`. The duplication is intentional — any worktree of the
feature is enough to resume.

## Property changes vs v2

| Property | v2 | v3 |
|---|---|---|
| State location | 1 central `state.json` | N per-worktree `.ndf-active` |
| Concurrent `ndf-start` | mutex via central file (force flag overrides) | independent, no contention |
| Active feature limit | 1 (strict) | unlimited (one per worktree) |
| `ndf-start` discovery | reads `state.json` to see if locked | checks if target worktree path exists |
| Worktree pre-exists | `--force` rm -rfs it | hard error, no override |
| `ndf-done` discovery | reads `state.json` | walks up from cwd to `.ndf-active` |
| `ndf-done` outside worktree | uses central state | hard error |
| `ndf-status` source | dumps `state.json` | scans worktrees on disk + git worktree list |

## Migration: v2 → v3

`scripts/ndf/ndf-migrate-v3.sh`:

1. Reads `numind-server/.ndf/state.json`.
2. If `.active` is non-null, writes a v3 `.ndf-active` into each worktree path
   listed under `.active.worktrees`.
3. Copies `state.json` → `state.json.v2-archive-<YYYYMMDD>.json`.
4. Removes the original `state.json`.

Idempotent: re-running skips worktrees that already have a `.ndf-active`.
Dry-run available (`--dry-run`).

After migration, `manifest.yaml`, `decisions/`, and `archived/` are untouched.

## Why the worktree path collision check is hard-fail

`ndf-start.sh` v3 refuses to proceed if `/private/tmp/wt-<slug>-<repo>/` already
exists. There is no `--force` override.

Rationale: a stale worktree on disk has exactly two explanations.
1. **You** abandoned it without `ndf-done`. Your uncommitted work is in there.
2. **Some other session** is currently using it.

In both cases, silently `rm -rf`'ing is the wrong move. The user must
investigate (`cd` in, `git status`) and decide. If they want to discard, the
script suggests the explicit `git worktree remove --force && git branch -D`.

## bash 3.2 compatibility

macOS ships bash 3.2.57. The v3 scripts deliberately avoid:

- Associative arrays (`declare -A`)
- `mapfile` / `readarray`
- `${var,,}` lowercase substitution

We use `while read | done < <(…)` for array fill, `tr` for case conversion, and
a poor-man-set via newline-delimited string for deduping.

## Compatibility checklist

- [x] Existing worktrees keep working: `ndf-migrate-v3.sh` retrofits them with
      `.ndf-active`.
- [x] `manifest.yaml` schema unchanged.
- [x] `decisions/`, `archived/` layout unchanged.
- [x] `pre-push` hook unchanged (still blocks `feature/`, `fix/`, `micro/` push).
- [x] `ndf-check-disjoint.sh` unchanged (not state-related).
- [x] `.claude/scripts/ndf-check.sh` rewritten: it now walks up from
      `file_path` to find `.ndf-active`; main checkout still blocks code-file
      edits.

## Future cleanups (followups)

- `templates/ndf/` — schema templates were written assuming v2 state. Update
  references where they imply "central state.json" (mostly comments).
- `numind-server/scripts/ndf/ndf-migrate-manifest.py` — was the v1→v2
  migrator. No changes needed; it does not touch state.json.
