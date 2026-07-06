package xhsscript

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

func TestListNoteDTOsAndGetQuotaForDedicatedEndpoints(t *testing.T) {
	db := newAnalyticsSummaryTestDB(t)
	svc := New(store.NewTestStore(db))
	ctx := context.Background()
	userID := uint(78)
	otherUserID := uint(79)

	require.NoError(t, db.Create(&model.XhsScriptQuotaAccount{
		UserID:        userID,
		FreeRemaining: 2,
		PaidRemaining: 5,
	}).Error)
	require.NoError(t, db.Create(&model.XhsScriptQuotaLedger{
		UserID:  userID,
		Delta:   -1,
		Bucket:  model.XhsScriptQuotaBucketFree,
		Reason:  model.XhsScriptLedgerReasonGeneration,
		RefType: model.XhsScriptLedgerRefTypeGeneration,
		RefID:   "generation_1",
	}).Error)
	require.NoError(t, db.Create(&model.XhsScriptNote{
		UserID:           userID,
		SourceNoteID:     "own-video",
		NoteType:         model.XhsScriptNoteTypeVideo,
		Title:            "自己的视频",
		VideoURL:         "https://sns-video.xhscdn.com/own.mp4",
		TranscribeStatus: model.XhsScriptTranscribePending,
		GenerateStatus:   model.XhsScriptGenerateNotReady,
	}).Error)
	require.NoError(t, db.Create(&model.XhsScriptNote{
		UserID:           otherUserID,
		SourceNoteID:     "other-video",
		NoteType:         model.XhsScriptNoteTypeVideo,
		Title:            "别人的视频",
		VideoURL:         "https://sns-video.xhscdn.com/other.mp4",
		TranscribeStatus: model.XhsScriptTranscribePending,
		GenerateStatus:   model.XhsScriptGenerateNotReady,
	}).Error)

	notes, err := svc.ListNoteDTOs(ctx, userID, 20, 0)
	require.NoError(t, err)
	require.Len(t, notes, 1)
	assert.Equal(t, "own-video", notes[0].SourceNoteID)
	assert.Equal(t, "waiting_transcript", notes[0].State)

	quota, err := svc.GetQuota(ctx, userID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, quota.FreeRemaining)
	assert.EqualValues(t, 5, quota.PaidRemaining)
	assert.EqualValues(t, 7, quota.Remaining)
	assert.EqualValues(t, 8, quota.Total)
}
