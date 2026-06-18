package meeting

// offline_trigger_test.go — EndSession → offline speaker-refinement trigger wiring
// (DIARIZATION_SPEC.md §3 step (4), §4 P1-离线触发时序, §7 T8).
//
// These tests live in a DEDICATED file (not meeting_test.go) to keep the T8 trigger wiring
// apart from the foundation's lifecycle tests (disjoint ownership). They drive the REAL
// refineSpeakersAsync path against an in-memory sqlite store (with the diarization tables
// migrated), through the asyncDiarizeSpawn seam (run synchronously), asserting:
//   - the effective flag (meeting_copilot && meeting_diarization) gates the spawn;
//   - when fired, the main path (stored embeddings → AHC) writes meeting_speaker +
//     final_speaker_id + diarization_status=done;
//   - failure / disabled paths never disturb the EndSession sync return.

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/meeting/diarize"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// newDiarizeTestBiz builds a meeting biz over an in-memory sqlite store with ALL meeting +
// diarization tables migrated (the foundation's helper omits the diarization tables).
func newDiarizeTestBiz(t *testing.T) (*meetingBiz, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&model.MeetingSession{},
		&model.MeetingSegment{},
		&model.MeetingFeedback{},
		&model.MeetingPreset{},
		&model.MeetingSpeaker{},
		&model.MeetingSegmentEmbedding{},
	), "AutoMigrate meeting + diarization tables")

	return &meetingBiz{ds: store.NewTestStore(db)}, db
}

// setDiarizationFlags sets both feature flags for the duration of the test.
func setDiarizationFlags(t *testing.T, copilot, diar bool) {
	t.Helper()
	prevC := viper.Get("features.meeting_copilot.enabled")
	prevD := viper.Get("features.meeting_diarization.enabled")
	viper.Set("features.meeting_copilot.enabled", copilot)
	viper.Set("features.meeting_diarization.enabled", diar)
	t.Cleanup(func() {
		viper.Set("features.meeting_copilot.enabled", prevC)
		viper.Set("features.meeting_diarization.enabled", prevD)
	})
}

// seedSessionWithEmbeddings creates an active session with `nSeg` segments and a stored
// embedding per segment (speaker pattern via speakerOf). Returns the session id.
func seedSessionWithEmbeddings(t *testing.T, db *gorm.DB, userID uint, speakers []int) uint64 {
	t.Helper()
	sess := &model.MeetingSession{
		UserID:            userID,
		Title:             "t",
		RolePrompt:        "r",
		Status:            model.MeetingSessionStatusActive,
		SummaryStatus:     model.MeetingSummaryStatusNone,
		DiarizationStatus: model.MeetingDiarizationStatusOnline,
	}
	require.NoError(t, db.Create(sess).Error)

	for i, sp := range speakers {
		seg := &model.MeetingSegment{SessionID: sess.ID, Seq: i + 1, Text: "x"}
		require.NoError(t, db.Create(seg).Error)
		emb := diarizeSpeakerEmbedding(sp, float64(i)*0.2)
		require.NoError(t, db.Create(&model.MeetingSegmentEmbedding{
			MeetingID: sess.ID,
			SegmentID: seg.ID,
			Embedding: emb,
		}).Error)
	}
	return sess.ID
}

// diarizeSpeakerEmbedding builds a packed float32×192 BLOB for a speaker (mirrors the diarize
// package's test embeddings; kept local to avoid cross-package test deps).
func diarizeSpeakerEmbedding(id int, jitter float64) []byte {
	v := make([]float32, diarize.EmbeddingDim)
	third := diarize.EmbeddingDim / 3
	lo := (id - 1) * third
	hi := lo + third
	if id == 3 {
		hi = diarize.EmbeddingDim
	}
	for i := lo; i < hi && i < diarize.EmbeddingDim; i++ {
		v[i] = float32(1.0 + 0.05*math.Sin(float64(i)+jitter))
	}
	for i := 0; i < diarize.EmbeddingDim; i++ {
		v[i] += 0.001
	}
	out := make([]byte, diarize.EmbeddingDim*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(f))
	}
	return out
}

// TestEndSession_DiarizationDisabled_NoSpawn: with the diarization flag off, EndSession must
// NOT spawn the refinement (status stays as seeded).
func TestEndSession_DiarizationDisabled_NoSpawn(t *testing.T) {
	setDiarizationFlags(t, true, false) // copilot on, diarization off ⇒ effective off

	b, db := newDiarizeTestBiz(t)
	id := seedSessionWithEmbeddings(t, db, 42, []int{1, 2, 1})

	spawned := false
	orig := asyncDiarizeSpawn
	t.Cleanup(func() { asyncDiarizeSpawn = orig })
	asyncDiarizeSpawn = func(func()) { spawned = true }

	// generateSummary=false so we don't also spin the summary path.
	_, err := b.EndSession(context.Background(), 42, id, false)
	require.NoError(t, err)
	assert.False(t, spawned, "diarization disabled ⇒ no refinement spawn")
}

// TestEndSession_DiarizationEnabled_RefinesMainPath: both flags on ⇒ EndSession spawns
// refinement; running it synchronously through the seam refines via the stored embeddings.
func TestEndSession_DiarizationEnabled_RefinesMainPath(t *testing.T) {
	setDiarizationFlags(t, true, true)
	// No voiceprint base_url ⇒ no fallback client; main path (stored embeddings) is used.
	prevVP := viper.Get("voiceprint.base_url")
	viper.Set("voiceprint.base_url", "")
	t.Cleanup(func() { viper.Set("voiceprint.base_url", prevVP) })

	b, db := newDiarizeTestBiz(t)
	id := seedSessionWithEmbeddings(t, db, 42, []int{1, 2, 1, 2})

	// Run the spawned refinement synchronously so we can assert its effects.
	orig := asyncDiarizeSpawn
	t.Cleanup(func() { asyncDiarizeSpawn = orig })
	asyncDiarizeSpawn = func(fn func()) { fn() }

	_, err := b.EndSession(context.Background(), 42, id, false)
	require.NoError(t, err)

	// session.diarization_status=done + speaker_count=2.
	var sess model.MeetingSession
	require.NoError(t, db.First(&sess, id).Error)
	assert.Equal(t, model.MeetingDiarizationStatusDone, sess.DiarizationStatus)
	require.NotNil(t, sess.SpeakerCount)
	assert.Equal(t, 2, *sess.SpeakerCount)

	// meeting_speaker rows written (发言人1 / 发言人2).
	var speakers []model.MeetingSpeaker
	require.NoError(t, db.Where("meeting_id = ?", id).Order("cluster_id ASC").Find(&speakers).Error)
	require.Len(t, speakers, 2)
	assert.Equal(t, "发言人1", speakers[0].DisplayLabel)
	assert.Equal(t, "发言人2", speakers[1].DisplayLabel)

	// final_speaker_id written on every segment; same-speaker segments share a cluster.
	var segs []model.MeetingSegment
	require.NoError(t, db.Where("session_id = ?", id).Order("seq ASC").Find(&segs).Error)
	require.Len(t, segs, 4)
	for _, s := range segs {
		require.NotNil(t, s.FinalSpeakerID, "every embedded segment gets a final speaker")
	}
	assert.Equal(t, *segs[0].FinalSpeakerID, *segs[2].FinalSpeakerID, "seq1 & seq3 same speaker")
	assert.Equal(t, *segs[1].FinalSpeakerID, *segs[3].FinalSpeakerID, "seq2 & seq4 same speaker")
	assert.NotEqual(t, *segs[0].FinalSpeakerID, *segs[1].FinalSpeakerID, "different speakers differ")
}

// TestEndSession_DiarizationEnabled_NoEmbeddings_SoftFail: both flags on but no stored
// embeddings and no recording ⇒ refinement marks failed; EndSession itself still succeeds
// (soft degrade — the meeting / its sync return are unaffected).
func TestEndSession_DiarizationEnabled_NoEmbeddings_SoftFail(t *testing.T) {
	setDiarizationFlags(t, true, true)
	prevVP := viper.Get("voiceprint.base_url")
	viper.Set("voiceprint.base_url", "")
	t.Cleanup(func() { viper.Set("voiceprint.base_url", prevVP) })

	b, db := newDiarizeTestBiz(t)
	// session with a segment but NO embeddings, NO recording_url.
	sess := &model.MeetingSession{
		UserID: 42, Title: "t", RolePrompt: "r",
		Status:            model.MeetingSessionStatusActive,
		SummaryStatus:     model.MeetingSummaryStatusNone,
		DiarizationStatus: model.MeetingDiarizationStatusOnline,
	}
	require.NoError(t, db.Create(sess).Error)
	require.NoError(t, db.Create(&model.MeetingSegment{SessionID: sess.ID, Seq: 1, Text: "x"}).Error)

	orig := asyncDiarizeSpawn
	t.Cleanup(func() { asyncDiarizeSpawn = orig })
	asyncDiarizeSpawn = func(fn func()) { fn() }

	dto, err := b.EndSession(context.Background(), 42, sess.ID, false)
	require.NoError(t, err, "EndSession sync return must be unaffected by refinement failure")
	assert.Equal(t, model.MeetingSessionStatusEnded, dto.Status)

	var reloaded model.MeetingSession
	require.NoError(t, db.First(&reloaded, sess.ID).Error)
	assert.Equal(t, model.MeetingDiarizationStatusFailed, reloaded.DiarizationStatus,
		"no usable speaker data ⇒ failed (UI keeps online labels)")
}
