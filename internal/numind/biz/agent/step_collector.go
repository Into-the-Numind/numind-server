package agent

import (
	"context"
	"sync"
	"time"
)

// stepCollector accumulates, in order, each ReAct step's assistant output
// (content + reasoning) during a STREAMING run, so the finalizer can persist the
// full transcript (Option C verbatim replay): user → assistant(step1) →
// tool_group(step1) → assistant(step2) → … → final answer.
//
// Why a collector (mirrors imageCollector / narration.EventCollector): Eino's
// react.Agent graph output is only the FINAL *schema.Message; the per-step
// transcript lives in unexported graph-local state with no accessor. So we tap
// the stream at the FinishReason boundary in streamScanToolCallChecker, where
// each step's accumulated text/reasoning is intact, and record it here.
//
// The captured `ts` (server wall-clock at the step's FinishReason) is the
// interleave key: a step's tool calls fire AFTER its assistant emit and BEFORE
// the next step's, so chronological order — not the fragile Eino StepIdx (which
// increments between the assistant emit and the tools node) — yields the correct
// assistant↔tool ordering at finalize.
//
// Non-stream Run() never populates this (Generate returns only the final
// message); an empty collector makes the finalizer fall back to the collapsed
// [user, tool_group, assistant] shape.
type stepEntry struct {
	Content   string
	Reasoning string
	TS        time.Time
}

type stepCollector struct {
	mu      sync.Mutex
	entries []stepEntry
	dropped int
}

// maxSteps caps entries for a pathological runaway loop. Eino MaxStep is 360
// (runner_runstream.go); this cap sits above it to absorb any future bump.
const maxSteps = 400

// maxStepReasoningRunes soft-caps a single step's reasoning so the persisted
// messages JSON can't blow up on a verbose thinking model. Content (the actual
// answer text) is left uncapped, matching the pre-Option-C final-answer contract.
const maxStepReasoningRunes = 16000

type stepCollectorCtxKey struct{}

func withStepCollector(ctx context.Context) context.Context {
	return context.WithValue(ctx, stepCollectorCtxKey{}, &stepCollector{})
}

// stepCollectorFrom returns the collector on ctx, or nil. All methods are
// nil-safe so callers need not check.
func stepCollectorFrom(ctx context.Context) *stepCollector {
	c, _ := ctx.Value(stepCollectorCtxKey{}).(*stepCollector)
	return c
}

// add records one step's assistant output. Steps whose content AND reasoning are
// both empty are dropped (nothing renderable). Safe on a nil receiver and for
// concurrent use.
func (c *stepCollector) add(content, reasoning string, ts time.Time) {
	if c == nil {
		return
	}
	if content == "" && reasoning == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxSteps {
		c.dropped++
		return
	}
	c.entries = append(c.entries, stepEntry{
		Content:   content,
		Reasoning: truncateRunesWithMarker(reasoning, maxStepReasoningRunes),
		TS:        ts,
	})
}

// list returns a snapshot copy of the recorded steps in order. Safe on nil.
func (c *stepCollector) list() []stepEntry {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) == 0 {
		return nil
	}
	out := make([]stepEntry, len(c.entries))
	copy(out, c.entries)
	return out
}

// Dropped returns the number of steps dropped after hitting maxSteps (lock-safe).
func (c *stepCollector) Dropped() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

// truncateRunesWithMarker trims s to at most n runes, appending an ellipsis
// marker when truncated. n <= 0 disables truncation.
func truncateRunesWithMarker(s string, n int) string {
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…（思考过程较长，已截断）"
}
