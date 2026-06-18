package diarize

// offline.go — post-meeting offline speaker refinement
// (DIARIZATION_SPEC.md §3 step (4), §4 P0-2, §5 D8, §7 T8).
//
// MAIN PATH (P0-2 / D8, the ONLY production path): pull this meeting's already-stored
// per-segment 192-d embeddings (meeting_segment_embedding, attached online during the meeting)
// and re-cluster them GLOBALLY with agglomerative hierarchical clustering (average linkage,
// cosine distance, adaptive threshold 0.55). Because the input is the embeddings themselves —
// one per segment, already aligned to meeting_segment rows by segment_id — this path is
// COMPLETELY INDEPENDENT of the recording timeline. It therefore sidesteps the §4 P0-2
// mis-alignment bug entirely (dashscope begin_time resets to 0 on every reconnect/resume, so
// aligning a continuous full.webm by absolute time is guaranteed to skew). It also needs no
// recording at all, so it can fire right after EndSession (the embeddings were collected
// during the meeting), not only after the recording upload.
//
// FALLBACK (bounded, best-effort): when the meeting has NO stored embeddings (online
// diarization was off / soft-degraded the whole meeting) AND a full recording exists, POST
// the recording to voiceprint /diarize, which does server-side VAD + sliding-window embedding
// + AHC and returns per-segment cluster ids. The fallback ALSO does not trust start_ms — it
// hands /diarize the segment list and lets the SERVICE align via its own re-segmentation
// (the Go side only maps the returned segment_id → cluster_id). The fallback is wholly
// optional; if it errors the refinement is marked failed and the meeting is unaffected.
//
// OUTPUT: appearance-ordered, stable speaker numbering written to meeting_speaker
// (display_label "发言人N" + color_index) + each segment's final_speaker_id, then the
// session's diarization_status (refining → done | failed) and speaker_count.
//
// IDEMPOTENCY (重试不漂移 — §8): cluster ids are assigned in order of each cluster's EARLIEST
// member segment (by seq). Re-running on the same embeddings yields the same ordering, the
// same display labels, and the same final_speaker_id mapping. ReplaceMeetingSpeakers is a
// delete-then-insert so a retry leaves no stale rows.
//
// SOFT DEGRADE (§4 P1): refinement NEVER kills anything — it runs in its own goroutine with a
// recover (spawned by the caller). Within RefineSpeakers, any failure (load error, no usable
// data, persist error, fallback error) ends with diarization_status=failed and a nil error
// return path for the happy case; the meeting / transcription / summary are untouched.

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/voiceprint"
)

// ahcCosineThreshold is the average-linkage agglomerative-clustering MERGE threshold on cosine
// DISTANCE (1 - cosine similarity). Two clusters are merged while the closest remaining pair's
// average-linkage cosine distance is below this. 0.55 mirrors the online tauMatch boundary
// (§5 D9 "AHC 余弦距离阈值 0.55") and is a D9 initial value to be re-calibrated on real
// meetings in dev acceptance (§2 — synthetic purity is not production accuracy).
const ahcCosineThreshold = 0.55

// NOTE: a hard speaker-count cap (online maxSpeakers=8 parity) is intentionally NOT enforced
// offline yet — AHC at the 0.55 cosine threshold rarely over-splits on single-mic audio, and the
// threshold is the primary control, calibrated on real meetings in dev acceptance (§ risk). If
// over-splitting shows up there, add a post-merge cap (merge closest pairs until count ≤ 8) here.

// SpeakerColorCount is the size of the frontend speaker color palette; color_index is assigned
// round-robin over it (DIARIZATION_SPEC.md §6 / T10 取色板). Kept here (not in the frontend) so
// the persisted color_index is stable and server-authoritative.
const SpeakerColorCount = 8

// OfflineStore is the subset of store.IDiarizeOfflineStore the refiner needs. Declared as a
// local interface so this leaf package does not import the store package (avoids an import
// cycle and keeps it fakeable in unit tests). store.IDiarizeOfflineStore satisfies it.
type OfflineStore interface {
	LoadSegmentEmbeddings(ctx context.Context, meetingID uint64) ([]store.LoadedEmbedding, error)
	ListSegmentsForFallback(ctx context.Context, meetingID uint64) ([]store.FallbackSegment, error)
	ReplaceMeetingSpeakers(ctx context.Context, meetingID uint64, speakers []model.MeetingSpeaker) error
	SetSegmentFinalSpeakers(ctx context.Context, assignments []store.FinalSpeakerAssignment) error
	SetDiarizationStatus(ctx context.Context, meetingID uint64, status string, speakerCount *int) error
}

// DiarizeClient is the subset of voiceprint.Client the fallback needs (just Diarize). Defined
// as an interface so tests can inject a fake without an httptest server. *voiceprint.Client
// satisfies it. nil ⇒ no fallback available (refiner skips the fallback branch).
type DiarizeClient interface {
	Diarize(ctx context.Context, audioURL string, sessionID string, segments []voiceprint.Segment) (*voiceprint.DiarizeResult, error)
}

// Refiner runs offline speaker refinement for one meeting. Construct with NewRefiner.
type Refiner struct {
	store        OfflineStore
	diarize      DiarizeClient // may be nil ⇒ fallback unavailable
	recordingURL string        // full.webm COS url; "" ⇒ fallback unavailable
}

// NewRefiner builds an offline refiner. diarize/recordingURL are only consulted on the
// fallback branch (no stored embeddings); pass a nil diarize and/or empty recordingURL when
// the fallback is unavailable — the refiner then just marks failed if there is nothing to
// cluster.
func NewRefiner(st OfflineStore, diarize DiarizeClient, recordingURL string) *Refiner {
	return &Refiner{store: st, diarize: diarize, recordingURL: recordingURL}
}

// RefineSpeakers runs the full offline refinement for meetingID:
//
//  1. mark diarization_status = "refining"
//  2. load stored embeddings (main path). If present → AHC re-cluster.
//     else if a recording + diarize client exist → /diarize fallback.
//     else → nothing to do → failed.
//  3. assign appearance-ordered stable speaker numbers → meeting_speaker rows
//  4. write each segment's final_speaker_id
//  5. mark diarization_status = "done" + speaker_count
//
// Returns nil on success. On any failure it best-effort marks status=failed and returns the
// underlying error (the caller logs it; the meeting is unaffected — soft degrade). It never
// panics (the caller also wraps it in a recover for defence in depth).
func (r *Refiner) RefineSpeakers(ctx context.Context, meetingID uint64) error {
	// (1) refining.
	if err := r.store.SetDiarizationStatus(ctx, meetingID, model.MeetingDiarizationStatusRefining, nil); err != nil {
		// Could not even mark refining — surface the error, but do not try to mark failed
		// (the same write would fail). Soft: caller logs.
		return fmt.Errorf("RefineSpeakers: mark refining: %w", err)
	}

	assignments, speakerCount, err := r.computeAssignments(ctx, meetingID)
	if err != nil {
		r.markFailed(ctx, meetingID)
		return fmt.Errorf("RefineSpeakers: compute: %w", err)
	}
	if len(assignments) == 0 {
		// Nothing usable to attribute (no embeddings, no fallback, or fallback returned
		// nothing). Mark failed (not done): the UI keeps the online A/B/C labels.
		r.markFailed(ctx, meetingID)
		return fmt.Errorf("RefineSpeakers: no usable speaker data for meeting %d", meetingID)
	}

	// (3) appearance-ordered speaker rows. computeAssignments already numbered clusters in
	// appearance order, so the distinct cluster ids are exactly 1..speakerCount.
	speakers := buildMeetingSpeakers(meetingID, speakerCount)
	if err := r.store.ReplaceMeetingSpeakers(ctx, meetingID, speakers); err != nil {
		r.markFailed(ctx, meetingID)
		return fmt.Errorf("RefineSpeakers: replace speakers: %w", err)
	}

	// (4) per-segment final_speaker_id.
	if err := r.store.SetSegmentFinalSpeakers(ctx, assignments); err != nil {
		r.markFailed(ctx, meetingID)
		return fmt.Errorf("RefineSpeakers: set final speakers: %w", err)
	}

	// (5) done + speaker_count.
	sc := speakerCount
	if err := r.store.SetDiarizationStatus(ctx, meetingID, model.MeetingDiarizationStatusDone, &sc); err != nil {
		return fmt.Errorf("RefineSpeakers: mark done: %w", err)
	}
	log.Infow("meeting diarize offline: refinement done",
		"meeting_id", meetingID, "speaker_count", speakerCount, "segments", len(assignments))
	return nil
}

// markFailed best-effort sets diarization_status=failed (soft收尾). Failures here are only
// logged — the meeting is already fine.
func (r *Refiner) markFailed(ctx context.Context, meetingID uint64) {
	if err := r.store.SetDiarizationStatus(ctx, meetingID, model.MeetingDiarizationStatusFailed, nil); err != nil {
		log.Warnw("meeting diarize offline: mark failed persist failed", "meeting_id", meetingID, "error", err)
	}
}

// computeAssignments returns the per-segment final-cluster assignments (cluster ids already in
// appearance order, 1-based) and the distinct speaker count. It runs the main path (stored
// embeddings → AHC) and, only if there are zero stored embeddings, the bounded /diarize
// fallback. An empty (nil) result + nil error means "nothing to attribute".
func (r *Refiner) computeAssignments(ctx context.Context, meetingID uint64) ([]store.FinalSpeakerAssignment, int, error) {
	embs, err := r.store.LoadSegmentEmbeddings(ctx, meetingID)
	if err != nil {
		return nil, 0, fmt.Errorf("load embeddings: %w", err)
	}

	if len(embs) > 0 {
		assignments, count := r.clusterStoredEmbeddings(embs)
		return assignments, count, nil
	}

	// No stored embeddings → fallback (bounded). Unavailable ⇒ nothing to attribute.
	if r.diarize == nil || r.recordingURL == "" {
		log.Infow("meeting diarize offline: no embeddings and no fallback available",
			"meeting_id", meetingID)
		return nil, 0, nil
	}
	return r.fallbackDiarize(ctx, meetingID)
}

// clusterStoredEmbeddings is the MAIN path: unpack the stored embeddings, run AHC, then number
// clusters in appearance order. Segments whose embedding fails to unpack (defensive — they
// were validated on write) are skipped (no final_speaker_id → they keep their online label).
func (r *Refiner) clusterStoredEmbeddings(embs []store.LoadedEmbedding) ([]store.FinalSpeakerAssignment, int) {
	// Unpack to unit vectors, preserving input order (already seq ASC from the store).
	type item struct {
		segmentID uint64
		seq       int
		vec       []float64 // L2-normalized
	}
	items := make([]item, 0, len(embs))
	for _, e := range embs {
		v := unpackEmbedding(e.Embedding)
		if v == nil {
			continue
		}
		nv := normalize64(v)
		if nv == nil {
			continue
		}
		items = append(items, item{segmentID: e.SegmentID, seq: e.Seq, vec: nv})
	}
	if len(items) == 0 {
		return nil, 0
	}

	vecs := make([][]float64, len(items))
	for i := range items {
		vecs[i] = items[i].vec
	}
	// raw[i] = AHC cluster label (arbitrary, contiguous from 0) for item i.
	raw := agglomerative(vecs, ahcCosineThreshold)

	// Number clusters by the seq of each cluster's EARLIEST member (appearance order). items
	// are already in seq ASC order, so the first time we encounter a raw label is its earliest
	// appearance — a single forward pass yields stable 1-based numbering.
	labelToNum := make(map[int]int)
	next := 1
	for i := range items {
		if _, ok := labelToNum[raw[i]]; !ok {
			labelToNum[raw[i]] = next
			next++
		}
	}
	speakerCount := next - 1

	// Per-segment confidence = cosine similarity of the segment to its cluster centroid
	// (mean of cluster members), exported for the UI to weaken low-confidence finals.
	centroids := clusterCentroids(vecs, raw)

	assignments := make([]store.FinalSpeakerAssignment, 0, len(items))
	for i := range items {
		clusterNum := labelToNum[raw[i]]
		conf := float32(cosine64(items[i].vec, centroids[raw[i]]))
		c := conf
		assignments = append(assignments, store.FinalSpeakerAssignment{
			SegmentID:  items[i].segmentID,
			ClusterID:  clusterNum,
			Confidence: &c,
		})
	}
	return assignments, speakerCount
}

// fallbackDiarize is the bounded best-effort path when no embeddings were stored. It feeds the
// segment list to voiceprint /diarize (server-side align by re-segmentation, NOT by start_ms)
// and maps the returned per-segment cluster ids into appearance-ordered numbering. A /diarize
// error returns the error (RefineSpeakers marks failed).
func (r *Refiner) fallbackDiarize(ctx context.Context, meetingID uint64) ([]store.FinalSpeakerAssignment, int, error) {
	segs, err := r.store.ListSegmentsForFallback(ctx, meetingID)
	if err != nil {
		return nil, 0, fmt.Errorf("list segments for fallback: %w", err)
	}
	if len(segs) == 0 {
		return nil, 0, nil
	}

	// Build the /diarize segment list. We DO pass start/end ms (the service may use them as a
	// hint) but the contract is that the service re-segments and aligns itself; we only trust
	// the returned segment_id → cluster_id mapping (P0-2: never align by our start_ms).
	vpSegs := make([]voiceprint.Segment, 0, len(segs))
	for _, s := range segs {
		vpSegs = append(vpSegs, voiceprint.Segment{
			SegmentID: int64(s.SegmentID),
			StartMs:   int64(s.StartMs),
			EndMs:     int64(s.StartMs) + s.DurationMs,
		})
	}

	sessionIDStr := fmt.Sprintf("%d", meetingID)
	dctx, cancel := context.WithTimeout(ctx, voiceprint.DiarizeTimeout())
	defer cancel()
	res, err := r.diarize.Diarize(dctx, r.recordingURL, sessionIDStr, vpSegs)
	if err != nil {
		return nil, 0, fmt.Errorf("voiceprint diarize: %w", err)
	}
	if res == nil || len(res.Segments) == 0 {
		return nil, 0, nil
	}

	// Map the service's (arbitrary) cluster ids into appearance order. segs is already seq ASC;
	// iterate segs in order and, for each segment that got a cluster assignment, register its
	// raw cluster id the first time we see it → stable 1-based appearance numbering.
	clusterBySeg := make(map[int64]int, len(res.Segments))
	confBySeg := make(map[int64]float32, len(res.Segments))
	for _, ss := range res.Segments {
		clusterBySeg[ss.SegmentID] = ss.ClusterID
		confBySeg[ss.SegmentID] = ss.Confidence
	}

	rawToNum := make(map[int]int)
	next := 1
	assignments := make([]store.FinalSpeakerAssignment, 0, len(segs))
	for _, s := range segs {
		rawCluster, ok := clusterBySeg[int64(s.SegmentID)]
		if !ok {
			continue // service did not attribute this segment → keep its online label
		}
		num, seen := rawToNum[rawCluster]
		if !seen {
			num = next
			rawToNum[rawCluster] = num
			next++
		}
		conf := confBySeg[int64(s.SegmentID)]
		c := conf
		assignments = append(assignments, store.FinalSpeakerAssignment{
			SegmentID:  s.SegmentID,
			ClusterID:  num,
			Confidence: &c,
		})
	}
	return assignments, next - 1, nil
}

// buildMeetingSpeakers makes the appearance-ordered speaker rows for clusters 1..count.
// display_label is "发言人N"; color_index is round-robin over the palette (0-based). created_at
// is left zero — the store/gorm autoCreateTime stamps it on insert.
func buildMeetingSpeakers(meetingID uint64, count int) []model.MeetingSpeaker {
	out := make([]model.MeetingSpeaker, 0, count)
	for n := 1; n <= count; n++ {
		out = append(out, model.MeetingSpeaker{
			MeetingID:    meetingID,
			ClusterID:    n,
			DisplayLabel: fmt.Sprintf("发言人%d", n),
			ColorIndex:   (n - 1) % SpeakerColorCount,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// pure AHC + vector math (no DB, no network — deterministically unit-testable)
// ---------------------------------------------------------------------------

// agglomerative runs average-linkage agglomerative hierarchical clustering on the given unit
// vectors using cosine DISTANCE (1 - cosine similarity), merging the closest pair while that
// distance is below threshold. Returns a per-point cluster label slice (labels are arbitrary
// non-negative ints; callers renumber by appearance order). O(n^3) worst case via the classic
// repeatedly-find-closest-pair scheme — fine for meeting scale (a few hundred segments max).
//
// Empty / single input is handled: 0 points → empty; 1 point → [0].
func agglomerative(vecs [][]float64, threshold float64) []int {
	n := len(vecs)
	if n == 0 {
		return nil
	}
	labels := make([]int, n)
	if n == 1 {
		return labels // single cluster 0
	}

	// Each point starts in its own cluster. members[c] is the list of point indices in cluster c.
	// active tracks live cluster ids. We use cluster ids 0..n-1 initially.
	members := make([][]int, n)
	active := make([]int, 0, n)
	for i := 0; i < n; i++ {
		members[i] = []int{i}
		active = append(active, i)
	}

	// Precompute pairwise cosine similarities between points once (symmetric). distance =
	// 1 - similarity; average-linkage cluster distance = mean over cross-cluster point pairs.
	sim := make([][]float64, n)
	for i := 0; i < n; i++ {
		sim[i] = make([]float64, n)
	}
	for i := 0; i < n; i++ {
		sim[i][i] = 1.0
		for j := i + 1; j < n; j++ {
			s := cosine64(vecs[i], vecs[j])
			sim[i][j] = s
			sim[j][i] = s
		}
	}

	avgLinkDist := func(a, b int) float64 {
		var sum float64
		cnt := 0
		for _, pi := range members[a] {
			for _, pj := range members[b] {
				sum += 1.0 - sim[pi][pj]
				cnt++
			}
		}
		if cnt == 0 {
			return math.Inf(1)
		}
		return sum / float64(cnt)
	}

	for len(active) > 1 {
		// Find the closest pair of active clusters. Deterministic tie-break: lowest (i,j) by
		// position in active (active stays sorted ascending), so retries don't drift.
		bestI, bestJ := -1, -1
		bestDist := math.Inf(1)
		for ii := 0; ii < len(active); ii++ {
			for jj := ii + 1; jj < len(active); jj++ {
				d := avgLinkDist(active[ii], active[jj])
				if d < bestDist {
					bestDist = d
					bestI, bestJ = ii, jj
				}
			}
		}
		if bestI < 0 || bestDist >= threshold {
			break // closest pair is farther than threshold ⇒ stop merging
		}
		// Merge active[bestJ] into active[bestI]; keep the lower cluster id as the survivor so
		// numbering is deterministic.
		a, b := active[bestI], active[bestJ]
		members[a] = append(members[a], members[b]...)
		members[b] = nil
		// remove bestJ from active (bestJ > bestI, so removing it doesn't shift bestI).
		active = append(active[:bestJ], active[bestJ+1:]...)
	}

	// Emit labels: assign each surviving cluster a contiguous label; points get their cluster's label.
	for _, c := range active {
		for _, p := range members[c] {
			labels[p] = c
		}
	}
	return labels
}

// clusterCentroids computes, per AHC label present in labels, the L2-normalized mean of its
// member vectors. Returned as a map label→centroid. Used for per-segment confidence.
func clusterCentroids(vecs [][]float64, labels []int) map[int][]float64 {
	sums := make(map[int][]float64)
	counts := make(map[int]int)
	for i, lab := range labels {
		if sums[lab] == nil {
			sums[lab] = make([]float64, len(vecs[i]))
		}
		for d, x := range vecs[i] {
			sums[lab][d] += x
		}
		counts[lab]++
	}
	out := make(map[int][]float64, len(sums))
	for lab, s := range sums {
		if counts[lab] > 0 {
			for d := range s {
				s[d] /= float64(counts[lab])
			}
		}
		if nv := normalize64(s); nv != nil {
			out[lab] = nv
		} else {
			out[lab] = s
		}
	}
	return out
}

// unpackEmbedding decodes a little-endian float32×192 packed BLOB to a []float64. Returns nil
// for a wrong-length blob (defensive; the writer validates length). Mirrors worker.packEmbedding.
func unpackEmbedding(blob []byte) []float64 {
	if len(blob) != EmbeddingDim*4 {
		return nil
	}
	out := make([]float64, EmbeddingDim)
	for i := 0; i < EmbeddingDim; i++ {
		bits := binary.LittleEndian.Uint32(blob[i*4:])
		out[i] = float64(math.Float32frombits(bits))
	}
	return out
}

// normalize64 returns a freshly allocated L2-normalized copy of v, or nil when v has zero /
// non-finite magnitude. Never mutates v.
func normalize64(v []float64) []float64 {
	var sumSq float64
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
		sumSq += x * x
	}
	if sumSq <= 0 {
		return nil
	}
	mag := math.Sqrt(sumSq)
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x / mag
	}
	return out
}

// cosine64 is the cosine similarity of two equal-length vectors. When both are unit vectors
// (the AHC inputs are normalized) this equals their dot product, but we divide by magnitudes
// defensively so centroids (re-normalized but possibly slightly off) stay correct.
func cosine64(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return -1.0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na <= 0 || nb <= 0 {
		return -1.0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
