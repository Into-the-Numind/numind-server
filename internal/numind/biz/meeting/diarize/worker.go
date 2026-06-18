package diarize

// worker.go — the per-session online-diarization worker goroutine
// (DIARIZATION_SPEC.md §3 step (2)/(3), §7 T7 part (b)).
//
// One Worker runs per realtime ASR session. It ranges over the T6 SessionBuffer's Pending()
// channel of ready PCM slices and, for each:
//
//	(1) voiceprint.Embed(pcm) — SOFT DEGRADE: when EmbedResult.Valid==false (timeout, 5xx,
//	    not-configured, too-short/silent) the segment's online_speaker_id is left NULL and
//	    the meeting carries on. NEVER kills the relay/transcription (§4 P1 invariant).
//	(2) Clusterer.Assign(embedding, durationMs, rms) — online incremental clustering →
//	    online_speaker_id (+ provisional flag + confidence).
//	(3) targeted UPDATE meeting_segment.online_speaker_id/online_provisional/speaker_confidence
//	    (via the T7-owned diarize store) + persist the 192-d embedding to
//	    meeting_segment_embedding (offline AHC main-path prerequisite, P0-2).
//	(4) push a ws "speaker" segment-update event to the frontend (new event type, reusing the
//	    relay's existing single-writer ws send — the Worker calls back through OnSpeaker).
//
// The worker is decoupled from store/biz by small interfaces (SpeakerStore, EmbedClient) so
// this package stays leaf-level (importable by biz/meeting without cycles) and unit-testable
// with fakes. Everything here degrades softly: a DB error, a marshal error, or a closed ws
// is logged and skipped; the loop continues. The loop exits when Pending() is closed (the
// relay's SessionBuffer.Close()), making teardown deterministic and leak-free.

import (
	"context"
	"encoding/binary"
	"math"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/voiceprint"
)

// EmbedClient is the subset of voiceprint.Client the worker needs (just Embed). Defined as
// an interface so tests can inject a fake (incl. the soft-degrade Valid==false path) without
// an httptest server. *voiceprint.Client satisfies it.
type EmbedClient interface {
	Embed(ctx context.Context, pcm []byte, sessionID string, segmentID int64) (*voiceprint.EmbedResult, error)
}

// SpeakerStore is the subset of the diarize store the worker needs. Matches
// store.IDiarizeStore method-for-method; kept as a local interface so this package does not
// import the store package (leaf-level decoupling + fakeable in tests).
type SpeakerStore interface {
	UpdateSegmentOnlineSpeaker(ctx context.Context, segmentID uint64, speakerID *int, provisional bool, confidence *float32) error
	UpsertSegmentEmbedding(ctx context.Context, meetingID, segmentID uint64, embedding []byte) error
}

// SpeakerUpdate is the payload pushed to the frontend when a segment's online speaker is
// assigned (DIARIZATION_SPEC.md §6 display rule). The relay turns this into a ws frame
// {"type":"speaker", ...} via its existing single-writer send (OnSpeaker callback).
type SpeakerUpdate struct {
	SegmentID   uint64  `json:"segment_id"`
	SpeakerID   int     `json:"online_speaker_id"`
	Provisional bool    `json:"online_provisional"`
	Confidence  float32 `json:"speaker_confidence"`
}

// Worker drives online diarization for one session. Construct with NewWorker, then Run in a
// goroutine; it returns when the SessionBuffer's Pending() channel closes.
type Worker struct {
	meetingID uint64
	sessionID string // string form of meetingID for voiceprint trace correlation
	embed     EmbedClient
	store     SpeakerStore
	clusterer *Clusterer
	// onSpeaker pushes an assignment to the frontend; nil ⇒ no push (still persists). The
	// relay supplies a closure over its single-writer ws send.
	onSpeaker func(SpeakerUpdate)
}

// NewWorker builds a per-session online-diarization worker.
//
// embed / store may be nil-ish only in the sense that the CALLER must not start a Worker
// when diarization is disabled or the voiceprint client is unconfigured (the relay gates
// this — see realtime.go). onSpeaker may be nil (persist-only, no ws push).
func NewWorker(meetingID uint64, sessionID string, embed EmbedClient, store SpeakerStore, onSpeaker func(SpeakerUpdate)) *Worker {
	return &Worker{
		meetingID: meetingID,
		sessionID: sessionID,
		embed:     embed,
		store:     store,
		clusterer: NewClusterer(),
		onSpeaker: onSpeaker,
	}
}

// Run consumes ready slices until pending is closed. Blocking; intended to run in its own
// goroutine. Each slice is processed by processSlice, which never panics out (a recover
// guards each iteration so one bad slice can't take down the worker or the process —
// soft-degrade invariant). Returns when the channel drains and closes.
func (w *Worker) Run(pending <-chan SegmentSlice) {
	for slice := range pending {
		w.safeProcess(slice)
	}
}

// safeProcess wraps processSlice in a recover so a programming bug on one segment degrades
// to a skipped segment instead of crashing the worker goroutine (which would silently end
// diarization for the rest of the meeting). The relay/transcription is unaffected regardless.
func (w *Worker) safeProcess(slice SegmentSlice) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Errorw("meeting diarize worker: process slice panicked, skipping segment",
				"meeting_id", w.meetingID, "segment_id", slice.SegmentID, "panic", rec)
		}
	}()
	w.processSlice(slice)
}

// processSlice runs the embed → cluster → persist → push pipeline for one segment. Uses
// context.Background(): like the relay's handleFinal, attribution persistence must not be
// cancelled by ws-connection lifecycle. All failures are soft (logged + skipped).
func (w *Worker) processSlice(slice SegmentSlice) {
	ctx := context.Background()

	// (1) Embed — SOFT DEGRADE. Embed itself returns nil error on transport failure
	// (Valid==false). The returned (rare) non-nil error is only a usage fault; treat it
	// identically to Valid==false: leave online_speaker_id NULL, meeting continues.
	res, err := w.embed.Embed(ctx, slice.PCM, w.sessionID, slice.SegmentID)
	if err != nil || res == nil || !res.Valid {
		reason := "embed not valid"
		if err != nil {
			reason = err.Error()
		} else if res != nil {
			reason = res.Reason
		}
		log.Debugw("meeting diarize worker: embed soft-degraded, leaving speaker NULL",
			"meeting_id", w.meetingID, "segment_id", slice.SegmentID, "reason", reason)
		// Persist nothing (online_speaker_id stays NULL by default); no ws push.
		return
	}

	// (2) Cluster. durationMs from the slice window; rms computed from the PCM. A malformed
	// embedding (wrong length / all-zero) ⇒ ok=false ⇒ soft skip (same as Valid==false).
	durationMs := slice.EndMs - slice.BeginMs
	if durationMs < 0 {
		durationMs = 0
	}
	rms := rmsDBFS(slice.PCM)
	assign, ok := w.clusterer.Assign(res.Embedding, durationMs, rms)
	if !ok {
		log.Debugw("meeting diarize worker: embedding unusable for clustering, leaving speaker NULL",
			"meeting_id", w.meetingID, "segment_id", slice.SegmentID)
		return
	}

	// SegmentSlice.SegmentID is int64 (T6 contract); the meeting_segment PK + store methods
	// are uint64. It originates from seg.ID (uint64) cast to int64 in the relay, so the value
	// is always a valid non-negative id — convert at this boundary.
	segmentID := uint64(slice.SegmentID)

	// (3a) Persist the embedding for the offline AHC main path (P0-2). A failure here must
	// not block the online label write — log + continue.
	if blob := packEmbedding(res.Embedding); blob != nil {
		if perr := w.store.UpsertSegmentEmbedding(ctx, w.meetingID, segmentID, blob); perr != nil {
			log.Warnw("meeting diarize worker: persist segment embedding failed",
				"meeting_id", w.meetingID, "segment_id", segmentID, "error", perr)
		}
	}

	// (3b) Targeted UPDATE of the segment's online speaker columns.
	speakerID := assign.SpeakerID
	confidence := assign.Confidence
	if uerr := w.store.UpdateSegmentOnlineSpeaker(ctx, segmentID, &speakerID, assign.Provisional, &confidence); uerr != nil {
		log.Warnw("meeting diarize worker: update online speaker failed",
			"meeting_id", w.meetingID, "segment_id", segmentID, "error", uerr)
		// Still attempt the ws push below: the in-memory assignment is valid even if the
		// DB write transiently failed; the next GetSession reload reconciles from the DB.
	}

	// (4) Push the assignment to the frontend (best-effort; nil onSpeaker ⇒ persist-only).
	if w.onSpeaker != nil {
		w.onSpeaker(SpeakerUpdate{
			SegmentID:   segmentID,
			SpeakerID:   speakerID,
			Provisional: assign.Provisional,
			Confidence:  confidence,
		})
	}
}

// packEmbedding serializes a float32×192 embedding to little-endian bytes for BLOB storage
// (matches the meeting_segment_embedding "float32×192 packed" contract). Returns nil for a
// wrong-length embedding (defensive; voiceprint already validates length).
func packEmbedding(emb []float32) []byte {
	if len(emb) != EmbeddingDim {
		return nil
	}
	out := make([]byte, len(emb)*4)
	for i, f := range emb {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(f))
	}
	return out
}

// rmsDBFS computes the RMS loudness of 16-bit mono s16le PCM in dBFS (0 dBFS == full scale).
// Returns -inf for empty/silent audio (treated as below MIN_RMS ⇒ weak segment). Pure.
func rmsDBFS(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return math.Inf(-1)
	}
	var sumSq float64
	for i := 0; i+1 < len(pcm); i += 2 {
		s := int16(binary.LittleEndian.Uint16(pcm[i : i+2]))
		v := float64(s) / 32768.0
		sumSq += v * v
	}
	rms := math.Sqrt(sumSq / float64(n))
	if rms <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(rms)
}
