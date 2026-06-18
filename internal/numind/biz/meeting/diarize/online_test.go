package diarize

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// speakerEmbedding builds a deterministic, well-separated 192-d unit-ish embedding for
// speaker `id` (1,2,3,...), with a per-segment `jitter` to mimic intra-speaker variation.
//
// Construction: each speaker concentrates its energy in a disjoint third of the dimension
// space (so distinct speakers are near-orthogonal → low cosine), then adds a small amount
// of jitter-driven wobble. The result is NOT normalized here on purpose — Assign normalizes
// internally, and feeding raw vectors exercises that path.
func speakerEmbedding(id int, jitter float64) []float32 {
	v := make([]float32, EmbeddingDim)
	third := EmbeddingDim / 3
	lo := (id - 1) * third
	hi := lo + third
	if id == 3 {
		hi = EmbeddingDim // last speaker takes the remainder
	}
	for i := lo; i < hi; i++ {
		// strong in-band energy + a deterministic ripple so same-speaker segments differ
		// slightly but stay highly correlated.
		v[i] = float32(1.0 + 0.05*math.Sin(float64(i)+jitter))
	}
	// tiny cross-band leakage (same for all so it doesn't help separation) to avoid exact
	// orthogonality / zero overlaps.
	for i := 0; i < EmbeddingDim; i++ {
		v[i] += float32(0.001)
	}
	return v
}

// TestClusterer_ThreeSpeakers_SeparatesClusters feeds an interleaved sequence of three
// distinct speakers and asserts: (a) same speaker → same cluster id across segments;
// (b) different speakers → different cluster ids; (c) exactly 3 clusters opened.
// This is the load-bearing §7 T7 assertion ("3 说话人序列分簇").
func TestClusterer_ThreeSpeakers_SeparatesClusters(t *testing.T) {
	c := NewClusterer()

	// Long, loud segments so none are forced provisional (durationMs >= MIN_DUR_MS,
	// rms computed elsewhere is irrelevant here — Assign takes rms directly; pass 0 dBFS).
	const dur = int64(2000)
	const loud = 0.0 // 0 dBFS, well above MIN_RMS

	type step struct{ speaker int }
	// Interleave so sticky/centroid logic is exercised across speaker switches.
	seq := []step{{1}, {2}, {3}, {1}, {2}, {3}, {1}, {1}, {2}, {3}}

	assignedFor := map[int]int{} // speaker -> first assigned cluster id
	for i, s := range seq {
		emb := speakerEmbedding(s.speaker, float64(i)) // jitter varies per segment
		a, ok := c.Assign(emb, dur, loud)
		require.True(t, ok, "step %d (speaker %d) should produce an assignment", i, s.speaker)
		require.GreaterOrEqual(t, a.SpeakerID, 1, "speaker id must be >=1")

		if prev, seen := assignedFor[s.speaker]; seen {
			assert.Equal(t, prev, a.SpeakerID,
				"step %d: speaker %d should map to the same cluster (%d) it got before", i, s.speaker, prev)
		} else {
			assignedFor[s.speaker] = a.SpeakerID
		}
	}

	// Three distinct speakers ⇒ three distinct cluster ids.
	require.Len(t, assignedFor, 3, "should have tracked exactly 3 speakers")
	ids := map[int]bool{}
	for _, id := range assignedFor {
		ids[id] = true
	}
	assert.Len(t, ids, 3, "three speakers must occupy three distinct cluster ids")
	assert.Equal(t, 3, c.SpeakerCount(), "exactly 3 online clusters should have been opened")
}

// TestClusterer_SameSpeakerRepeated_SingleCluster: many segments from ONE speaker must all
// land in one cluster and (after the first) be confident, non-provisional matches.
func TestClusterer_SameSpeakerRepeated_SingleCluster(t *testing.T) {
	c := NewClusterer()
	const dur = int64(1500)

	first, ok := c.Assign(speakerEmbedding(1, 0), dur, 0.0)
	require.True(t, ok)
	assert.False(t, first.Provisional, "a confident brand-new strong speaker is non-provisional")

	for i := 1; i < 6; i++ {
		a, ok := c.Assign(speakerEmbedding(1, float64(i)), dur, 0.0)
		require.True(t, ok)
		assert.Equal(t, first.SpeakerID, a.SpeakerID, "same speaker should stay in one cluster")
		assert.False(t, a.Provisional, "subsequent strong same-speaker segments should be confident")
		assert.GreaterOrEqual(t, a.Confidence, float32(tauMatch), "confident match cosine >= TAU_MATCH")
	}
	assert.Equal(t, 1, c.SpeakerCount(), "only one cluster for one speaker")
}

// TestClusterer_WeakSegment_ForcedProvisional_NoNewCluster: a too-short / too-quiet segment
// that does NOT match an existing cluster must NOT open a new speaker (it attaches
// provisionally to the best, or seeds the very first cluster provisionally). It must never
// update a centroid.
func TestClusterer_ShortSegment_DoesNotSpawnSecondSpeakerFromWeakNoise(t *testing.T) {
	c := NewClusterer()

	// Establish speaker 1 with a strong segment.
	s1, ok := c.Assign(speakerEmbedding(1, 0), 2000, 0.0)
	require.True(t, ok)
	require.False(t, s1.Provisional)

	// A WEAK (too short) segment from a DIFFERENT speaker: must not open cluster 2.
	weak, ok := c.Assign(speakerEmbedding(2, 7), 300 /* < MIN_DUR_MS */, 0.0)
	require.True(t, ok)
	assert.True(t, weak.Provisional, "weak segment must be provisional")
	assert.Equal(t, 1, c.SpeakerCount(), "weak audio must not spawn a new speaker cluster")

	// A WEAK (too quiet) segment likewise attaches provisionally, no new cluster.
	quiet, ok := c.Assign(speakerEmbedding(3, 11), 2000, -60 /* < MIN_RMS dBFS */)
	require.True(t, ok)
	assert.True(t, quiet.Provisional, "too-quiet segment must be provisional")
	assert.Equal(t, 1, c.SpeakerCount(), "quiet audio must not spawn a new speaker cluster")
}

// TestClusterer_MaxSpeakersCap: once MAX_SPEAKERS clusters exist, a would-be-new strong
// segment attaches provisionally to its best existing cluster rather than opening a 9th.
func TestClusterer_MaxSpeakersCap(t *testing.T) {
	c := NewClusterer()
	// Open MAX_SPEAKERS distinct strong clusters using near-orthogonal one-hot-ish vectors.
	for k := 0; k < maxSpeakers; k++ {
		emb := make([]float32, EmbeddingDim)
		emb[k] = 1.0 // each cluster dominated by a unique dimension ⇒ mutually orthogonal
		a, ok := c.Assign(emb, 2000, 0.0)
		require.True(t, ok)
		assert.False(t, a.Provisional)
	}
	require.Equal(t, maxSpeakers, c.SpeakerCount())

	// A brand-new orthogonal speaker (dimension maxSpeakers) is strong but the cap is hit.
	emb := make([]float32, EmbeddingDim)
	emb[maxSpeakers] = 1.0
	a, ok := c.Assign(emb, 2000, 0.0)
	require.True(t, ok)
	assert.True(t, a.Provisional, "at MAX_SPEAKERS a would-be-new speaker is forced provisional")
	assert.Equal(t, maxSpeakers, c.SpeakerCount(), "must not exceed MAX_SPEAKERS clusters")
}

// TestClusterer_RejectsMalformedEmbedding: wrong length or all-zero ⇒ ok=false (soft skip).
func TestClusterer_RejectsMalformedEmbedding(t *testing.T) {
	c := NewClusterer()

	_, ok := c.Assign(make([]float32, 10), 2000, 0.0)
	assert.False(t, ok, "wrong-length embedding must be rejected")

	_, ok = c.Assign(make([]float32, EmbeddingDim), 2000, 0.0) // all zero
	assert.False(t, ok, "all-zero embedding must be rejected")

	assert.Equal(t, 0, c.SpeakerCount(), "rejected embeddings must not create clusters")
}
