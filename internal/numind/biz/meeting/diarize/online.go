package diarize

// online.go — per-session online incremental speaker clustering
// (DIARIZATION_SPEC.md §3 step (3), §5 D3, §5 D9).
//
// This file is PURE logic: no DB, no network, no goroutines. It owns the in-memory
// per-session centroid bookkeeping and the double-threshold hysteresis assignment.
// The worker (worker.go) feeds it embeddings (from voiceprint.Embed) and persists the
// resulting online_speaker_id. Keeping clustering pure makes it deterministically
// unit-testable (DIARIZATION_SPEC.md §7 T7: "3 说话人序列分簇").
//
// ALGORITHM (DIARIZATION_SPEC.md §5 D3 + D9):
//   - Per-session centroids accumulate a rawSum of (duration-weighted) member
//     embeddings; the centroid used for matching is rawSum L2-normalized ON READ
//     (never stored pre-normalized — averaging unit vectors then re-normalizing is
//     the standard online-centroid trick and keeps the running sum exact).
//   - Match score = cosine similarity between the incoming (normalized) embedding and
//     each centroid (normalized). Cosine of two unit vectors == their dot product.
//   - Double-threshold hysteresis:
//       best >= TAU_MATCH (0.55)            → confident match; assign + UPDATE centroid.
//       best <  TAU_NEW   (0.45)            → genuinely new speaker; open a new cluster
//                                             (subject to MAX_SPEAKERS) + seed centroid.
//       TAU_NEW <= best < TAU_MATCH (gray)  → tentatively attach to best cluster but mark
//                                             PROVISIONAL and DO NOT update the centroid
//                                             (gray-zone audio must not poison a centroid).
//   - STICKY_MARGIN (0.04): if the previous segment's speaker is within STICKY_MARGIN of
//     the best non-previous cluster, keep the previous speaker (reduces flapping between
//     two acoustically close speakers — single-mic reality, §8).
//   - Only NON-provisional assignments update the centroid (§5 D3).
//   - MIN_DUR_MS (700) / MIN_RMS (-45 dBFS): segments shorter/quieter than these are
//     "weak" — they still get a best-effort label (so the UI shows something) but are
//     forced PROVISIONAL and never update a centroid (short/quiet Chinese segments
//     degrade embedding quality, §8). A weak segment also never opens a NEW cluster
//     (would create spurious speakers from noise).
//   - MAX_SPEAKERS (8): once 8 clusters exist, a would-be-new embedding is instead
//     attached PROVISIONAL to its best existing cluster (single-mic meetings rarely
//     exceed this; the cap bounds memory + spurious-cluster blowup).
//
// All parameters are D9 INITIAL VALUES, to be re-calibrated on real meetings in dev
// acceptance (the spike's synthetic purity 1.0 is not production accuracy, §2).

import "math"

// Clustering parameters (DIARIZATION_SPEC.md §5 D9 initial values).
const (
	// tauMatch is the high threshold: best cosine >= this ⇒ confident match, centroid updates.
	tauMatch = 0.55
	// tauNew is the low threshold: best cosine < this ⇒ a genuinely new speaker.
	tauNew = 0.45
	// stickyMargin keeps the previous speaker if it's within this of the best other cluster.
	stickyMargin = 0.04
	// minDurMs: segments shorter than this are weak ⇒ provisional, no centroid update, no new cluster.
	minDurMs = 700
	// minRMSdBFS: segments quieter than this (RMS in dBFS) are weak ⇒ same treatment as too-short.
	minRMSdBFS = -45.0
	// maxSpeakers caps the number of online clusters per session.
	maxSpeakers = 8
)

// centroid is one online speaker cluster's running state.
//
// rawSum is the (duration-weighted) sum of all NON-provisional member embeddings; the
// match centroid is rawSum L2-normalized on read (normalizedCopy). count/weight are
// diagnostic. Storing rawSum (not a pre-normalized mean) keeps the online update exact
// and O(dim) per accepted segment.
type centroid struct {
	id     int
	rawSum []float32 // length EmbeddingDim; sum of weighted member embeddings (non-provisional only)
	weight float64   // total weight accumulated (sum of per-segment weights)
	count  int       // number of non-provisional segments folded in
}

// EmbeddingDim is the fixed CAM++ embedding dimensionality (mirrors voiceprint.EmbeddingDim).
// Duplicated here to keep this pure-logic file free of the voiceprint import; the worker
// guarantees it only ever passes EmbeddingDim-long slices (voiceprint validates length).
const EmbeddingDim = 192

// Assignment is the result of clustering one segment's embedding.
type Assignment struct {
	// SpeakerID is the assigned online cluster id (stable within a session). Always >= 1
	// for a successful Assign; the worker maps this to meeting_segment.online_speaker_id.
	SpeakerID int
	// Provisional is true when the assignment is gray-zone / weak (low confidence): the
	// centroid was NOT updated and the UI should weaken the label (DIARIZATION_SPEC.md §6).
	Provisional bool
	// Confidence is the best cosine similarity against the chosen cluster's centroid
	// (1.0 for a brand-new cluster, which has no prior to compare against). Persisted to
	// meeting_segment.speaker_confidence for the UI to weaken low-confidence labels.
	Confidence float32
}

// Clusterer is the per-session online incremental clusterer. NOT safe for concurrent
// use — the worker drives it from a single goroutine (one per session), so no locking
// is needed (and adding a mutex would be dead weight). One Clusterer per meeting session.
type Clusterer struct {
	centroids []*centroid
	nextID    int
	// lastSpeakerID is the previous NON-provisional assignment's speaker (for STICKY_MARGIN);
	// 0 means "no prior confident speaker yet".
	lastSpeakerID int
}

// NewClusterer builds an empty per-session clusterer (speaker ids start at 1).
func NewClusterer() *Clusterer {
	return &Clusterer{nextID: 1}
}

// Assign clusters one segment's embedding and returns its online speaker assignment.
//
// embedding MUST be length EmbeddingDim (the worker only calls Assign with a voiceprint
// embedding that already passed length validation). durationMs is the segment's audio
// length; rmsDBFS is its loudness in dBFS (use a very-negative value, e.g. math.Inf(-1),
// when unknown — it will be treated as below the floor ⇒ weak). The returned Assignment
// is never the zero value for a valid embedding: a fresh session always opens cluster 1.
//
// ok==false only for a malformed (wrong-length / all-zero) embedding — the worker then
// leaves online_speaker_id NULL (soft skip), mirroring the Valid==false path.
func (c *Clusterer) Assign(embedding []float32, durationMs int64, rmsDBFS float64) (Assignment, bool) {
	if len(embedding) != EmbeddingDim {
		return Assignment{}, false
	}
	norm := normalize(embedding)
	if norm == nil {
		// All-zero / non-finite embedding ⇒ unusable; soft skip.
		return Assignment{}, false
	}

	// A segment is "weak" if too short OR too quiet: it gets a best-effort label but is
	// forced provisional, never updates a centroid, and never opens a new cluster.
	weak := durationMs < minDurMs || rmsDBFS < minRMSdBFS

	bestID, bestSim := c.bestMatch(norm)

	// STICKY_MARGIN: if we have a previous confident speaker and it is within stickyMargin
	// of the best (different) cluster, prefer the previous speaker to reduce flapping.
	if c.lastSpeakerID != 0 && bestID != c.lastSpeakerID {
		if prevSim, ok := c.simTo(c.lastSpeakerID, norm); ok && bestSim-prevSim <= stickyMargin {
			bestID, bestSim = c.lastSpeakerID, prevSim
		}
	}

	switch {
	case bestID != 0 && bestSim >= tauMatch:
		// Confident match. Update centroid only if the segment is strong (non-weak).
		provisional := weak
		if !weak {
			c.update(bestID, norm, durationMs)
			c.lastSpeakerID = bestID
		}
		return Assignment{SpeakerID: bestID, Provisional: provisional, Confidence: float32(bestSim)}, true

	case bestID != 0 && bestSim >= tauNew:
		// Gray zone [TAU_NEW, TAU_MATCH): attach to best cluster but PROVISIONAL, no centroid
		// update (§5 D3). Weak segments also land here when their best is in-range.
		return Assignment{SpeakerID: bestID, Provisional: true, Confidence: float32(bestSim)}, true

	default:
		// best < TAU_NEW (or no clusters yet): would be a new speaker.
		if weak || len(c.centroids) >= maxSpeakers {
			// Weak audio must not spawn a speaker; at MAX_SPEAKERS we attach to the best
			// existing cluster instead of opening a 9th. If there is literally no cluster
			// yet AND the segment is weak, we still must seed cluster 1 so the very first
			// (possibly short) utterance gets a label — otherwise a quiet opener is dropped.
			if bestID != 0 {
				return Assignment{SpeakerID: bestID, Provisional: true, Confidence: float32(bestSim)}, true
			}
			// No cluster at all: open the first one even for weak audio, but keep it
			// provisional and do not advance lastSpeakerID.
			id := c.open(norm, durationMs, false /* don't fold weak audio into rawSum */)
			return Assignment{SpeakerID: id, Provisional: true, Confidence: 1.0}, true
		}
		// Strong, room available ⇒ genuinely new speaker.
		id := c.open(norm, durationMs, true)
		c.lastSpeakerID = id
		return Assignment{SpeakerID: id, Provisional: false, Confidence: 1.0}, true
	}
}

// bestMatch returns the cluster id with the highest cosine similarity to the (already
// normalized) embedding, and that similarity. Returns (0, -1) when no clusters exist.
func (c *Clusterer) bestMatch(norm []float32) (int, float64) {
	bestID, bestSim := 0, -1.0
	for _, ct := range c.centroids {
		sim := cosineToCentroid(ct, norm)
		if sim > bestSim {
			bestID, bestSim = ct.id, sim
		}
	}
	return bestID, bestSim
}

// simTo returns the cosine similarity of norm to a specific cluster id (ok=false if absent).
func (c *Clusterer) simTo(id int, norm []float32) (float64, bool) {
	for _, ct := range c.centroids {
		if ct.id == id {
			return cosineToCentroid(ct, norm), true
		}
	}
	return 0, false
}

// open creates a new cluster seeded with norm and returns its id. When fold is true the
// embedding is folded into rawSum (a confident new speaker); when false the cluster is
// created empty-weighted (a provisional first-utterance seed) so a later strong segment
// defines its centroid. Either way bestMatch can still compare against a zero-weight
// cluster via the seeded rawSum (we always seed rawSum so the centroid is never all-zero).
func (c *Clusterer) open(norm []float32, durationMs int64, fold bool) int {
	id := c.nextID
	c.nextID++
	ct := &centroid{id: id, rawSum: make([]float32, EmbeddingDim)}
	// Always seed rawSum with the embedding so the new cluster has a usable centroid for
	// subsequent matching; weight/count reflect whether it was a confident fold.
	w := weightForDuration(durationMs)
	for i := range norm {
		ct.rawSum[i] = float32(float64(norm[i]) * w)
	}
	ct.weight = w
	if fold {
		ct.count = 1
	}
	c.centroids = append(c.centroids, ct)
	return id
}

// update folds a (non-provisional, strong) embedding into an existing cluster's rawSum.
func (c *Clusterer) update(id int, norm []float32, durationMs int64) {
	for _, ct := range c.centroids {
		if ct.id == id {
			w := weightForDuration(durationMs)
			for i := range norm {
				ct.rawSum[i] += float32(float64(norm[i]) * w)
			}
			ct.weight += w
			ct.count++
			return
		}
	}
}

// SpeakerCount reports the number of online clusters opened so far (diagnostic / for the
// worker to surface as meeting_session.speaker_count if desired). Provisional-only first
// seeds are counted (they are real cluster slots).
func (c *Clusterer) SpeakerCount() int { return len(c.centroids) }

// ---------------------------------------------------------------------------
// vector math (pure)
// ---------------------------------------------------------------------------

// weightForDuration maps a segment duration to a centroid-update weight. Longer segments
// carry more weight (their embeddings are more reliable, §8 MIN_DUR_MS rationale). Clamped
// to [0.1, 3.0] so a single very long segment cannot dominate a centroid. Duration-weighted
// accumulation is the §5 D3 "时长加权" requirement.
func weightForDuration(durationMs int64) float64 {
	if durationMs <= 0 {
		return 0.1
	}
	w := float64(durationMs) / 1000.0 // 1.0 per second
	if w < 0.1 {
		w = 0.1
	}
	if w > 3.0 {
		w = 3.0
	}
	return w
}

// cosineToCentroid is the cosine similarity between a normalized embedding and a centroid
// (centroid's rawSum normalized on read). Both operands are unit vectors ⇒ cosine == dot.
func cosineToCentroid(ct *centroid, norm []float32) float64 {
	cn := normalize(ct.rawSum)
	if cn == nil {
		return -1.0
	}
	var dot float64
	for i := range norm {
		dot += float64(norm[i]) * float64(cn[i])
	}
	return dot
}

// normalize returns a freshly allocated L2-normalized copy of v, or nil when v has zero /
// non-finite magnitude (an unusable embedding). Never mutates v.
func normalize(v []float32) []float32 {
	var sumSq float64
	for _, x := range v {
		fx := float64(x)
		if math.IsNaN(fx) || math.IsInf(fx, 0) {
			return nil
		}
		sumSq += fx * fx
	}
	if sumSq <= 0 {
		return nil
	}
	mag := math.Sqrt(sumSq)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / mag)
	}
	return out
}
