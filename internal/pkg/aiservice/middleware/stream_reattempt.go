package middleware

import (
	"context"
	"time"

	"numind-server/internal/pkg/aiservice"
)

// reattemptFunc obtains the next streaming attempt after the current attempt
// failed with a retryable error BEFORE any content was forwarded. It returns:
//   - ch:   the next attempt's channel (nil when more==false or on a start error)
//   - err:  a synchronous start error from establishing the next attempt (rare).
//     When non-nil the wrapper stops and forwards the held error terminal chunk.
//   - more: false when no further attempts are available (retry budget exhausted
//     or fallback candidates exhausted) — the wrapper then forwards the held
//     error terminal chunk and stops.
//
// Implementations own their own attempt bookkeeping: the Retry path allows a
// single same-route reattempt; the Fallback path cascades through same-model
// alternate-provider routes, internally skipping any candidate that fails to
// start so it never surfaces a start error to the wrapper.
type reattemptFunc func(ctx context.Context) (ch <-chan aiservice.ChatChunk, err error, more bool)

// wrapStreamWithReattempt consumes firstCh and returns a single spliced channel
// to the downstream consumer. If an attempt fails with a retryable error
// terminal chunk BEFORE any content (or reasoning) chunk has been forwarded, it
// drains that attempt and calls reattempt() for a fresh attempt, splicing the
// new attempt's chunks onto the SAME output channel — transparently to the
// downstream consumer (which still sees one <-chan ChatChunk with one terminal).
//
// Invariants (ADR 0001 MUST-HANDLE):
//   - P0-1: the failed attempt's error terminal chunk is NEVER forwarded while a
//     reattempt is possible; only the final outcome (success terminal, or the
//     last attempt's error terminal) reaches the consumer — exactly ONE terminal
//     chunk is emitted. This keeps the OUTER Billing / ContextBudget channel
//     wrappers seeing a single terminal → single Reconcile/Refund/UsageRecord.
//   - P0-2: each abandoned attempt channel is fully drained so the adapter's
//     `defer body.Close()` runs (no provider HTTP body / goroutine leak).
//   - P0-3: once any content (Delta) or reasoning (ReasoningDelta) chunk is
//     forwarded, reattempt is permanently disabled (firstContentForwarded).
//   - P1-5: the reattempt decision looks ONLY at (IsFinal && Err retryable &&
//     !firstContentForwarded); it ignores chunk.Usage (an idle terminal chunk
//     may carry a stale lastUsage).
//
// backoff, when non-nil, is awaited (respecting ctx cancellation) before each
// reattempt; pass nil for no delay (the Fallback path).
func wrapStreamWithReattempt(
	ctx context.Context,
	firstCh <-chan aiservice.ChatChunk,
	reattempt reattemptFunc,
	backoff func() time.Duration,
) <-chan aiservice.ChatChunk {
	out := make(chan aiservice.ChatChunk)

	go func() {
		defer close(out)

		ch := firstCh
		firstContentForwarded := false

		for {
			var pendingErr *aiservice.ChatChunk

			for chunk := range ch {
				// Reattempt candidate: a retryable error terminal chunk seen
				// before any content was forwarded. Hold it; do not forward yet.
				// (P1-5: decide on chunk.Err only, never on chunk.Usage.)
				if chunk.IsFinal && chunk.Err != nil && !firstContentForwarded && retryableError(chunk.Err) {
					c := chunk
					pendingErr = &c
					break
				}

				if chunk.Delta != "" || chunk.ReasoningDelta != "" {
					firstContentForwarded = true // P0-3
				}

				select {
				case out <- chunk:
				case <-ctx.Done():
					drainChunks(ch)
					return
				}

				if chunk.IsFinal {
					// Normal end, or a non-retryable / post-content error
					// terminal: forwarded as-is. Done.
					drainChunks(ch)
					return
				}
			}

			if pendingErr == nil {
				// Channel closed without a terminal chunk (defensive).
				return
			}

			// Holding a pre-content retryable error. Drain the failed attempt's
			// channel before reattempting (P0-2).
			drainChunks(ch)

			if backoff != nil {
				select {
				case <-ctx.Done():
					// Consumer is gone; let the outer ContextBudget ctx.Done
					// path refund. Do not forward a terminal.
					return
				case <-time.After(backoff()):
				}
			}

			nextCh, startErr, more := reattempt(ctx)
			if !more || startErr != nil || nextCh == nil {
				// No further attempts (or reattempt failed to start): forward the
				// held error terminal chunk and stop (P0-1: the single terminal
				// the consumer sees).
				sendChunk(ctx, out, *pendingErr)
				return
			}
			ch = nextCh
		}
	}()

	return out
}

// drainChunks consumes the remainder of ch so the producing goroutine / HTTP
// body is released. ch is expected to be closed by its producer.
func drainChunks(ch <-chan aiservice.ChatChunk) {
	for range ch { //nolint:revive // intentional drain
	}
}

// sendChunk forwards chunk to out unless ctx is already done.
func sendChunk(ctx context.Context, out chan<- aiservice.ChatChunk, chunk aiservice.ChatChunk) {
	select {
	case out <- chunk:
	case <-ctx.Done():
	}
}
