package diarize

// offline_test.go — unit tests for the offline speaker-refinement path
// (DIARIZATION_SPEC.md §7 T8: "embedding 集合→稳定编号 + 软降级").
//
// All tests are pure / fake-backed (no DB, no network): the AHC + numbering logic is
// deterministic, and RefineSpeakers is driven through a fake OfflineStore (+ fake
// DiarizeClient for the fallback branch). This gives a permanent regression guard for:
//   - AHC clustering separates distinct speakers and merges same-speaker segments;
//   - appearance-order numbering is stable AND idempotent (a retry does not drift labels);
//   - soft degrade: no embeddings + no fallback ⇒ diarization_status=failed, never panics;
//   - the /diarize fallback is used only when there are zero stored embeddings.

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/voiceprint"
)

// packEmbeddingF64 packs a []float64 into the little-endian float32×192 BLOB form the store
// holds (mirrors worker.packEmbedding so the test exercises the real unpack path).
func packEmbeddingF64(v []float64) []byte {
	out := make([]byte, EmbeddingDim*4)
	for i := 0; i < EmbeddingDim && i < len(v); i++ {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(float32(v[i])))
	}
	return out
}

// loadedEmb builds a store.LoadedEmbedding for a speaker id at a given seq (reusing the
// online_test.go speakerEmbedding helper for well-separated embeddings).
func loadedEmb(segmentID uint64, seq int, speaker int, jitter float64) store.LoadedEmbedding {
	emb := speakerEmbedding(speaker, jitter)
	v := make([]float64, len(emb))
	for i, x := range emb {
		v[i] = float64(x)
	}
	return store.LoadedEmbedding{SegmentID: segmentID, Seq: seq, Embedding: packEmbeddingF64(v)}
}

// ---------------------------------------------------------------------------
// fake OfflineStore
// ---------------------------------------------------------------------------

type fakeOfflineStore struct {
	embeddings []store.LoadedEmbedding
	fallback   []store.FallbackSegment

	loadErr     error
	fallbackErr error
	replaceErr  error
	finalErr    error
	statusErr   error

	// recorded
	statuses     []recordedStatus
	speakers     []model.MeetingSpeaker
	finals       []store.FinalSpeakerAssignment
	replaceCalls int
}

type recordedStatus struct {
	status       string
	speakerCount *int
}

func (f *fakeOfflineStore) LoadSegmentEmbeddings(_ context.Context, _ uint64) ([]store.LoadedEmbedding, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.embeddings, nil
}

func (f *fakeOfflineStore) ListSegmentsForFallback(_ context.Context, _ uint64) ([]store.FallbackSegment, error) {
	if f.fallbackErr != nil {
		return nil, f.fallbackErr
	}
	return f.fallback, nil
}

func (f *fakeOfflineStore) ReplaceMeetingSpeakers(_ context.Context, _ uint64, speakers []model.MeetingSpeaker) error {
	f.replaceCalls++
	if f.replaceErr != nil {
		return f.replaceErr
	}
	f.speakers = speakers
	return nil
}

func (f *fakeOfflineStore) SetSegmentFinalSpeakers(_ context.Context, assignments []store.FinalSpeakerAssignment) error {
	if f.finalErr != nil {
		return f.finalErr
	}
	f.finals = assignments
	return nil
}

func (f *fakeOfflineStore) SetDiarizationStatus(_ context.Context, _ uint64, status string, speakerCount *int) error {
	if f.statusErr != nil {
		return f.statusErr
	}
	f.statuses = append(f.statuses, recordedStatus{status: status, speakerCount: speakerCount})
	return nil
}

func (f *fakeOfflineStore) lastStatus() recordedStatus {
	if len(f.statuses) == 0 {
		return recordedStatus{}
	}
	return f.statuses[len(f.statuses)-1]
}

// ---------------------------------------------------------------------------
// fake DiarizeClient
// ---------------------------------------------------------------------------

type fakeDiarize struct {
	res *voiceprint.DiarizeResult
	err error

	calls int
}

func (f *fakeDiarize) Diarize(_ context.Context, _ string, _ string, _ []voiceprint.Segment) (*voiceprint.DiarizeResult, error) {
	f.calls++
	return f.res, f.err
}

// ---------------------------------------------------------------------------
// AHC + numbering (pure)
// ---------------------------------------------------------------------------

// TestAgglomerative_SeparatesThreeSpeakers feeds nine embeddings (three distinct speakers,
// three segments each, interleaved) and asserts AHC produces exactly three clusters with the
// right membership.
func TestAgglomerative_SeparatesThreeSpeakers(t *testing.T) {
	// interleaved order: s1,s2,s3,s1,s2,s3,s1,s2,s3
	order := []int{1, 2, 3, 1, 2, 3, 1, 2, 3}
	vecs := make([][]float64, len(order))
	for i, sp := range order {
		emb := speakerEmbedding(sp, float64(i)*0.3)
		v := make([]float64, len(emb))
		for j, x := range emb {
			v[j] = float64(x)
		}
		vecs[i] = normalize64(v)
		require.NotNil(t, vecs[i])
	}

	labels := agglomerative(vecs, ahcCosineThreshold)
	require.Len(t, labels, len(order))

	// Build speaker→label groups; each true speaker must map to exactly one cluster label and
	// distinct speakers to distinct labels.
	speakerToLabel := map[int]int{}
	for i, sp := range order {
		if existing, ok := speakerToLabel[sp]; ok {
			assert.Equal(t, existing, labels[i], "segments of speaker %d must share a cluster", sp)
		} else {
			speakerToLabel[sp] = labels[i]
		}
	}
	// three distinct labels
	seen := map[int]bool{}
	for _, l := range speakerToLabel {
		seen[l] = true
	}
	assert.Len(t, seen, 3, "three distinct speakers must yield three clusters")
}

// TestClusterStoredEmbeddings_StableNumbering_Idempotent asserts: (a) appearance-order
// numbering (first speaker to appear is 发言人1), and (b) re-running on the same input yields
// IDENTICAL assignments (重试不漂移 — §8 idempotency).
func TestClusterStoredEmbeddings_StableNumbering_Idempotent(t *testing.T) {
	// Appearance order: speaker 2 appears first (seq 1), then speaker 1 (seq 2), etc. The
	// numbering must follow APPEARANCE (so speaker 2 → cluster 1), not the synthetic speaker id.
	embs := []store.LoadedEmbedding{
		loadedEmb(10, 1, 2, 0.1), // speaker 2 first  → cluster 1
		loadedEmb(11, 2, 1, 0.2), // speaker 1 second → cluster 2
		loadedEmb(12, 3, 3, 0.3), // speaker 3 third  → cluster 3
		loadedEmb(13, 4, 2, 0.4), // speaker 2 again  → cluster 1
		loadedEmb(14, 5, 1, 0.5), // speaker 1 again  → cluster 2
	}
	r := NewRefiner(&fakeOfflineStore{}, nil, "")

	a1, n1 := r.clusterStoredEmbeddings(embs)
	require.Equal(t, 3, n1, "three speakers")
	require.Len(t, a1, 5)

	byID := map[uint64]int{}
	for _, a := range a1 {
		byID[a.SegmentID] = a.ClusterID
	}
	assert.Equal(t, 1, byID[10], "first appearance (speaker 2) → cluster 1")
	assert.Equal(t, 2, byID[11], "second appearance (speaker 1) → cluster 2")
	assert.Equal(t, 3, byID[12], "third appearance (speaker 3) → cluster 3")
	assert.Equal(t, 1, byID[13], "speaker 2 again → cluster 1")
	assert.Equal(t, 2, byID[14], "speaker 1 again → cluster 2")

	// Idempotency: a second run on the same input must produce the exact same mapping + count.
	a2, n2 := r.clusterStoredEmbeddings(embs)
	require.Equal(t, n1, n2)
	require.Len(t, a2, len(a1))
	for i := range a1 {
		assert.Equal(t, a1[i].SegmentID, a2[i].SegmentID, "segment order stable")
		assert.Equal(t, a1[i].ClusterID, a2[i].ClusterID, "cluster numbering must not drift on retry")
	}
}

// ---------------------------------------------------------------------------
// RefineSpeakers orchestration (main path)
// ---------------------------------------------------------------------------

func TestRefineSpeakers_MainPath_WritesSpeakersAndFinals(t *testing.T) {
	store0 := &fakeOfflineStore{
		embeddings: []store.LoadedEmbedding{
			loadedEmb(1, 1, 1, 0.0),
			loadedEmb(2, 2, 2, 0.1),
			loadedEmb(3, 3, 1, 0.2),
			loadedEmb(4, 4, 2, 0.3),
		},
	}
	r := NewRefiner(store0, nil, "")

	err := r.RefineSpeakers(context.Background(), 99)
	require.NoError(t, err)

	// status transitions: refining first, done last with speaker_count.
	require.GreaterOrEqual(t, len(store0.statuses), 2)
	assert.Equal(t, model.MeetingDiarizationStatusRefining, store0.statuses[0].status)
	last := store0.lastStatus()
	assert.Equal(t, model.MeetingDiarizationStatusDone, last.status)
	require.NotNil(t, last.speakerCount)
	assert.Equal(t, 2, *last.speakerCount, "two distinct speakers")

	// meeting_speaker rows: 发言人1 / 发言人2 with round-robin color_index.
	require.Len(t, store0.speakers, 2)
	assert.Equal(t, 1, store0.speakers[0].ClusterID)
	assert.Equal(t, "发言人1", store0.speakers[0].DisplayLabel)
	assert.Equal(t, 0, store0.speakers[0].ColorIndex)
	assert.Equal(t, uint64(99), store0.speakers[0].MeetingID)
	assert.Equal(t, "发言人2", store0.speakers[1].DisplayLabel)
	assert.Equal(t, 1, store0.speakers[1].ColorIndex)

	// final_speaker_id per segment: speaker 1 segments (1,3) → cluster 1; speaker 2 (2,4) → cluster 2.
	finals := map[uint64]int{}
	for _, a := range store0.finals {
		finals[a.SegmentID] = a.ClusterID
		require.NotNil(t, a.Confidence, "confidence is set for the UI")
	}
	require.Len(t, finals, 4)
	assert.Equal(t, finals[1], finals[3], "same speaker → same final cluster")
	assert.Equal(t, finals[2], finals[4], "same speaker → same final cluster")
	assert.NotEqual(t, finals[1], finals[2], "different speakers → different clusters")
}

// TestRefineSpeakers_Idempotent_FullPass: running RefineSpeakers twice yields the same
// persisted speakers + finals (the delete-then-insert ReplaceMeetingSpeakers + deterministic
// numbering means a retry does not drift).
func TestRefineSpeakers_Idempotent_FullPass(t *testing.T) {
	mk := func() *fakeOfflineStore {
		return &fakeOfflineStore{
			embeddings: []store.LoadedEmbedding{
				loadedEmb(1, 1, 3, 0.0),
				loadedEmb(2, 2, 1, 0.1),
				loadedEmb(3, 3, 3, 0.2),
			},
		}
	}
	s1 := mk()
	r1 := NewRefiner(s1, nil, "")
	require.NoError(t, r1.RefineSpeakers(context.Background(), 7))

	// Second run on a fresh store with identical input.
	s2 := mk()
	r2 := NewRefiner(s2, nil, "")
	require.NoError(t, r2.RefineSpeakers(context.Background(), 7))

	require.Equal(t, len(s1.speakers), len(s2.speakers))
	for i := range s1.speakers {
		assert.Equal(t, s1.speakers[i].ClusterID, s2.speakers[i].ClusterID)
		assert.Equal(t, s1.speakers[i].DisplayLabel, s2.speakers[i].DisplayLabel)
	}
	f1 := map[uint64]int{}
	for _, a := range s1.finals {
		f1[a.SegmentID] = a.ClusterID
	}
	for _, a := range s2.finals {
		assert.Equal(t, f1[a.SegmentID], a.ClusterID, "segment %d cluster must not drift across runs", a.SegmentID)
	}
}

// ---------------------------------------------------------------------------
// soft degrade
// ---------------------------------------------------------------------------

// TestRefineSpeakers_NoEmbeddingsNoFallback_MarksFailed: zero stored embeddings and no
// recording/diarize client ⇒ status=failed, no panic, returns an error (caller logs it).
func TestRefineSpeakers_NoEmbeddingsNoFallback_MarksFailed(t *testing.T) {
	store0 := &fakeOfflineStore{embeddings: nil}
	r := NewRefiner(store0, nil, "")

	err := r.RefineSpeakers(context.Background(), 1)
	require.Error(t, err, "no usable data is an error path (caller soft-logs)")
	assert.Equal(t, model.MeetingDiarizationStatusFailed, store0.lastStatus().status)
	assert.Empty(t, store0.speakers, "no speakers written")
	assert.Empty(t, store0.finals, "no finals written")
}

// TestRefineSpeakers_LoadError_MarksFailed: a store load error ⇒ failed + error, no panic.
func TestRefineSpeakers_LoadError_MarksFailed(t *testing.T) {
	store0 := &fakeOfflineStore{loadErr: errors.New("db down")}
	r := NewRefiner(store0, nil, "")

	err := r.RefineSpeakers(context.Background(), 1)
	require.Error(t, err)
	assert.Equal(t, model.MeetingDiarizationStatusFailed, store0.lastStatus().status)
}

// TestRefineSpeakers_PersistError_MarksFailed: a final-speaker persist error after clustering
// ⇒ failed + error.
func TestRefineSpeakers_PersistError_MarksFailed(t *testing.T) {
	store0 := &fakeOfflineStore{
		embeddings: []store.LoadedEmbedding{loadedEmb(1, 1, 1, 0.0), loadedEmb(2, 2, 2, 0.1)},
		finalErr:   errors.New("write fail"),
	}
	r := NewRefiner(store0, nil, "")

	err := r.RefineSpeakers(context.Background(), 1)
	require.Error(t, err)
	assert.Equal(t, model.MeetingDiarizationStatusFailed, store0.lastStatus().status)
}

// ---------------------------------------------------------------------------
// fallback path
// ---------------------------------------------------------------------------

// TestRefineSpeakers_Fallback_UsedWhenNoEmbeddings: zero stored embeddings BUT a recording +
// diarize client ⇒ the /diarize fallback is invoked and its per-segment cluster ids are mapped
// into appearance-ordered numbering.
func TestRefineSpeakers_Fallback_UsedWhenNoEmbeddings(t *testing.T) {
	store0 := &fakeOfflineStore{
		embeddings: nil, // forces fallback
		fallback: []store.FallbackSegment{
			{SegmentID: 100, Seq: 1, StartMs: 0, DurationMs: 1000},
			{SegmentID: 101, Seq: 2, StartMs: 1000, DurationMs: 1000},
			{SegmentID: 102, Seq: 3, StartMs: 2000, DurationMs: 1000},
		},
	}
	// Service returns arbitrary raw cluster ids (7 then 4); appearance order must renumber:
	// seg100(raw7)→1, seg101(raw4)→2, seg102(raw7)→1.
	fd := &fakeDiarize{res: &voiceprint.DiarizeResult{
		SpeakerCount: 2,
		Segments: []voiceprint.SegmentSpeaker{
			{SegmentID: 100, ClusterID: 7, Confidence: 0.9},
			{SegmentID: 101, ClusterID: 4, Confidence: 0.8},
			{SegmentID: 102, ClusterID: 7, Confidence: 0.7},
		},
	}}
	r := NewRefiner(store0, fd, "https://cos.example/full.webm")

	err := r.RefineSpeakers(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, 1, fd.calls, "fallback diarize invoked exactly once")

	last := store0.lastStatus()
	assert.Equal(t, model.MeetingDiarizationStatusDone, last.status)
	require.NotNil(t, last.speakerCount)
	assert.Equal(t, 2, *last.speakerCount)

	finals := map[uint64]int{}
	for _, a := range store0.finals {
		finals[a.SegmentID] = a.ClusterID
	}
	assert.Equal(t, 1, finals[100], "first-appearing raw cluster → 1")
	assert.Equal(t, 2, finals[101], "second distinct raw cluster → 2")
	assert.Equal(t, 1, finals[102], "same raw cluster as seg100 → 1")
}

// TestRefineSpeakers_FallbackError_MarksFailed: when no embeddings and the /diarize call
// errors ⇒ failed + error, no panic.
func TestRefineSpeakers_FallbackError_MarksFailed(t *testing.T) {
	store0 := &fakeOfflineStore{
		embeddings: nil,
		fallback:   []store.FallbackSegment{{SegmentID: 1, Seq: 1}},
	}
	fd := &fakeDiarize{err: errors.New("vp 503")}
	r := NewRefiner(store0, fd, "https://cos.example/full.webm")

	err := r.RefineSpeakers(context.Background(), 1)
	require.Error(t, err)
	assert.Equal(t, model.MeetingDiarizationStatusFailed, store0.lastStatus().status)
}
