# scrubber — Streaming Context Scrubber

Token-level streaming filter that strips **internal-injected tags** from LLM
streaming output before it reaches the user. Part of 有数 Agent Mode V1.5
板块 2「上下文管理 V2」(D5 决策).

**Requires Go ≥ 1.24** (uses `b.Loop()` benchmark API).

## Why this exists

Agent Mode injects tags into the LLM context that the user must NEVER see:

| Tag | Source | Purpose |
|---|---|---|
| `<memory data-internal="true">...</memory>` | 板块 3 task-07 dialectic | User memory facts |
| `<reference-only data-internal="true">...</reference-only>` | 板块 2 task 2.4 autocompact | Summarized history (12-section) |
| `<context data-internal="true">...</context>` | future memory injection | Contextual facts |
| `<personal_context data-internal="true">...</personal_context>` | future | Same |
| `<system-reminder>...</system-reminder>` | runtime | Internal reminders (always-strip) |
| `<persisted-output ref="UUID" .../>` | task 2.2 L0 write-to-disk | Big tool result reference (self-closing, always-strip) |
| `[Personal Memory: ...]` | legacy inline marker | Always-strip |
| `[Context: ...]` | legacy inline marker | Always-strip |
| `[CONTEXT COMPACTION — REFERENCE ONLY] ... \n\n` | early autocompact fallback | Always-strip (legacy data) |
| `[REFERENCE ONLY] ... \n\n` | early autocompact fallback | Always-strip (legacy data) |

If the LLM echoes any of these tags back (which it sometimes does after the
12-section summary or after seeing the system-reminder block), they would
leak to the end user. Scrubber catches them at the stream emit boundary.

## Cross-board injection contract (硬约束)

All internal-injected tags **must carry `data-internal="true"`** (or
`data-internal='true'`):

```go
// 板块 3 task-07 memory injection
systemPrompt += fmt.Sprintf(`<memory data-internal="true" id="%s">%s</memory>`, uuid, content)

// 板块 2 task 2.4 autocompact summary
summary := `<reference-only data-internal="true">` + body12Sections + `</reference-only>`
```

If the user types `<memory>literal text</memory>` (no `data-internal`)
inside their own input, **scrubber lets it through** — we only strip
internally-injected variants. This avoids hurting legitimate user input
that happens to look like a tag.

`<system-reminder>` and `<persisted-output>` are exceptions: they are
*always* stripped (no `data-internal` required), because the user has no
legitimate reason to write them.

## API

```go
import "numind-server/internal/numind/biz/compactv2/scrubber"

s := scrubber.NewStreamScrubber()
for chunk := range llmStream {
    clean := s.Push(chunk.Content)  // returns user-safe text (may be "")
    if clean != "" {
        emit(clean)
    }
}
if tail := s.Flush(); tail != "" {
    emit(tail)
}
```

Reuse a scrubber across runs via `s.Reset()`.

## State machine

```
                    OUTSIDE
                  /        \
            seen "<"      seen "["
                |             |
            MAYBE_TAG      MAYBE_TAG
            /     \         /     \
   matched      not       matched   not
   open tag    tag        inline   inline
       |        |         block   prefix
       v        |          |        |
  INSIDE_TAG    emit       emit    emit "[",
       |         "<",     scrubbed  back to
   seen "</tag>" back to   inline   OUTSIDE
   or self-close OUTSIDE   block,
       |                    back to
       v                    OUTSIDE
    OUTSIDE
```

Buffer accumulates the undecided suffix; `Push` returns only decided text.

## Overflow protection

`MaxBufferSize = 8 KB`. If an unclosed tag's body grows past this (malicious
or buggy LLM output), the scrubber:

1. Logs a `warn`-level event.
2. Flushes the entire buffer as plain text.
3. Resets state to OUTSIDE.

This prevents OOM and is preferred over silently dropping content.

## Performance

| Scenario | Target | Why |
|---|---|---|
| Pure text 1 KB (fast path) | < 5 µs | No state-machine work; direct flush |
| Single tag 1 KB | < 50 µs | One regex match + one state transition |
| Cross-10-chunk tag | < 200 µs | Worst case, repeated MAYBE_TAG → INSIDE_TAG transitions |

Run `go test -bench=. ./internal/numind/biz/compactv2/scrubber/...` to verify.

## Integration

The runner wires scrubber into `streamToFrontend` (the narration / final answer
pipeline). Only user-facing stream gets scrubbed; tool call args do NOT go
through scrubber (would break tool call semantics).

The integration is done **after task 2.4** lands the `<reference-only>` XML
wrapping — the scrubber package itself is independent and can be built /
tested standalone.

## SoT consistency

`ScrubTagNames`, `alwaysScrubTags`, and `requiresDataInternalTags` are
checked at package `init()` to ensure every tag appears in exactly one of
the two maps. If you add a tag to `ScrubTagNames`, you MUST also add it to
either `alwaysScrubTags` or `requiresDataInternalTags` — otherwise the
package will panic at startup.

## Files

- `scrubber.go` — `StreamScrubber` struct, Push/Flush/Reset.
- `patterns.go` — `ScrubTagNames`, regex patterns, SoT consistency check.
- `scrubber_test.go` — 18 spec cases (table-driven).
- `scrubber_bench_test.go` — 3 benchmark scenarios.
