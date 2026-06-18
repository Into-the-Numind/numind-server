package diarize

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/voiceprint"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

// fakeEmbed implements EmbedClient. It returns a scripted EmbedResult per call (round-robin
// over results); a Valid==false result models the soft-degrade path.
type fakeEmbed struct {
	mu      sync.Mutex
	results []*voiceprint.EmbedResult
	errs    []error
	calls   int
}

func (f *fakeEmbed) Embed(_ context.Context, _ []byte, _ string, _ int64) (*voiceprint.EmbedResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	f.calls++
	var res *voiceprint.EmbedResult
	if i < len(f.results) {
		res = f.results[i]
	}
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	return res, err
}

// recordedUpdate captures one UpdateSegmentOnlineSpeaker call.
type recordedUpdate struct {
	segmentID   uint64
	speakerID   *int
	provisional bool
	confidence  *float32
}

// fakeStore implements SpeakerStore and records calls.
type fakeStore struct {
	mu         sync.Mutex
	updates    []recordedUpdate
	embeddings map[uint64][]byte
}

func newFakeStore() *fakeStore { return &fakeStore{embeddings: map[uint64][]byte{}} }

func (s *fakeStore) UpdateSegmentOnlineSpeaker(_ context.Context, segmentID uint64, speakerID *int, provisional bool, confidence *float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, recordedUpdate{segmentID, speakerID, provisional, confidence})
	return nil
}

func (s *fakeStore) UpsertSegmentEmbedding(_ context.Context, _, segmentID uint64, embedding []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.embeddings[segmentID] = embedding
	return nil
}

func (s *fakeStore) updateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.updates)
}

// runWorkerSync feeds the given slices through a one-shot channel and runs the worker to
// completion synchronously (channel closed ⇒ Run returns). Returns the recorded ws pushes.
func runWorkerSync(w *Worker, slices []SegmentSlice, pushes *[]SpeakerUpdate, pushMu *sync.Mutex) {
	ch := make(chan SegmentSlice, len(slices))
	for _, s := range slices {
		ch <- s
	}
	close(ch)
	_ = pushes
	_ = pushMu
	w.Run(ch)
}

func goodEmbed(speaker int) *voiceprint.EmbedResult {
	return &voiceprint.EmbedResult{Valid: true, Embedding: speakerEmbedding(speaker, float64(speaker))}
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

// TestWorker_EmbedSoftDegrade_LeavesSpeakerEmpty: when Embed returns Valid==false, the worker
// must NOT write online_speaker_id (no store update, no embedding persisted, no ws push) —
// the meeting carries on with the segment unattributed. This is the §7 T7 soft-degrade
// assertion ("Embed 软降级时 speaker_id 留空").
func TestWorker_EmbedSoftDegrade_LeavesSpeakerEmpty(t *testing.T) {
	embed := &fakeEmbed{results: []*voiceprint.EmbedResult{
		{Valid: false, Reason: "transport: timeout"}, // soft-degrade
	}}
	store := newFakeStore()

	var pushMu sync.Mutex
	var pushes []SpeakerUpdate
	w := NewWorker(42, "42", embed, store, func(u SpeakerUpdate) {
		pushMu.Lock()
		pushes = append(pushes, u)
		pushMu.Unlock()
	})

	runWorkerSync(w, []SegmentSlice{
		{SegmentID: 100, BeginMs: 0, EndMs: 2000, PCM: loudPCM(2000)},
	}, &pushes, &pushMu)

	assert.Equal(t, 0, store.updateCount(), "soft-degraded embed must not write online_speaker_id")
	assert.Empty(t, store.embeddings, "soft-degraded embed must not persist an embedding")
	assert.Empty(t, pushes, "soft-degraded embed must not push a speaker ws event")
}

// TestWorker_EmbedReturnsErr_LeavesSpeakerEmpty: a (rare) non-nil error from Embed is treated
// identically to Valid==false — no attribution, meeting continues.
func TestWorker_EmbedReturnsErr_LeavesSpeakerEmpty(t *testing.T) {
	embed := &fakeEmbed{
		results: []*voiceprint.EmbedResult{nil},
		errs:    []error{assert.AnError},
	}
	store := newFakeStore()
	w := NewWorker(1, "1", embed, store, nil)

	runWorkerSync(w, []SegmentSlice{{SegmentID: 7, BeginMs: 0, EndMs: 2000, PCM: loudPCM(2000)}}, nil, nil)

	assert.Equal(t, 0, store.updateCount(), "Embed error must not write online_speaker_id")
	assert.Empty(t, store.embeddings)
}

// TestWorker_HappyPath_PersistsAndPushes: a valid embed → store update with a non-nil
// speakerID + embedding persisted + a ws push carrying the assignment.
func TestWorker_HappyPath_PersistsAndPushes(t *testing.T) {
	embed := &fakeEmbed{results: []*voiceprint.EmbedResult{goodEmbed(1)}}
	store := newFakeStore()

	var pushes []SpeakerUpdate
	w := NewWorker(9, "9", embed, store, func(u SpeakerUpdate) { pushes = append(pushes, u) })

	runWorkerSync(w, []SegmentSlice{{SegmentID: 55, BeginMs: 0, EndMs: 2000, PCM: loudPCM(2000)}}, nil, nil)

	require.Equal(t, 1, store.updateCount(), "valid embed must write online_speaker_id once")
	upd := store.updates[0]
	assert.Equal(t, uint64(55), upd.segmentID)
	require.NotNil(t, upd.speakerID, "speakerID must be non-nil for a valid embed")
	assert.GreaterOrEqual(t, *upd.speakerID, 1)
	require.NotNil(t, upd.confidence)

	require.Contains(t, store.embeddings, uint64(55), "embedding must be persisted for offline AHC")
	assert.Len(t, store.embeddings[55], EmbeddingDim*4, "packed float32×192 = 768 bytes")

	require.Len(t, pushes, 1, "valid embed must push exactly one speaker ws event")
	assert.Equal(t, uint64(55), pushes[0].SegmentID)
	assert.Equal(t, *upd.speakerID, pushes[0].SpeakerID)
}

// TestWorker_MixedSequence_OnlyValidGetAttributed: interleave valid and soft-degraded embeds
// over a multi-speaker sequence; only the valid segments get online_speaker_id, and the two
// distinct valid speakers cluster apart.
func TestWorker_MixedSequence_OnlyValidGetAttributed(t *testing.T) {
	embed := &fakeEmbed{results: []*voiceprint.EmbedResult{
		goodEmbed(1), // seg 1 → speaker A
		{Valid: false, Reason: "service invalid"}, // seg 2 → soft skip
		goodEmbed(2), // seg 3 → speaker B
		goodEmbed(1), // seg 4 → speaker A again
	}}
	store := newFakeStore()
	var pushes []SpeakerUpdate
	w := NewWorker(3, "3", embed, store, func(u SpeakerUpdate) { pushes = append(pushes, u) })

	runWorkerSync(w, []SegmentSlice{
		{SegmentID: 1, BeginMs: 0, EndMs: 2000, PCM: loudPCM(2000)},
		{SegmentID: 2, BeginMs: 2000, EndMs: 4000, PCM: loudPCM(2000)},
		{SegmentID: 3, BeginMs: 4000, EndMs: 6000, PCM: loudPCM(2000)},
		{SegmentID: 4, BeginMs: 6000, EndMs: 8000, PCM: loudPCM(2000)},
	}, nil, nil)

	// 3 valid embeds ⇒ 3 updates + 3 pushes; the invalid one is skipped entirely.
	require.Equal(t, 3, store.updateCount())
	require.Len(t, pushes, 3)

	// seg 2 (soft-degraded) never persisted / pushed.
	assert.NotContains(t, store.embeddings, uint64(2))
	for _, u := range pushes {
		assert.NotEqual(t, uint64(2), u.SegmentID)
	}

	// seg 1 and seg 4 are the same speaker; seg 3 is a different speaker.
	byID := map[uint64]int{}
	for _, u := range pushes {
		byID[u.SegmentID] = u.SpeakerID
	}
	assert.Equal(t, byID[1], byID[4], "same speaker across seg1/seg4")
	assert.NotEqual(t, byID[1], byID[3], "seg3 is a different speaker")
}

// loudPCM returns durationMs worth of full-amplitude 16k mono s16le PCM (so rmsDBFS is near
// 0 dBFS, comfortably above MIN_RMS — segment loudness never forces provisional in tests).
func loudPCM(durationMs int) []byte {
	n := durationMs * bytesPerMs // bytes
	if n%2 == 1 {
		n++
	}
	out := make([]byte, n)
	for i := 0; i+1 < len(out); i += 2 {
		// 0x4000 == 16384 ≈ -6 dBFS, plenty loud (well above -45 dBFS floor).
		out[i] = 0x00
		out[i+1] = 0x40
	}
	return out
}
