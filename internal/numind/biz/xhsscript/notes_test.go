package xhsscript

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

func TestIngestNotes_NonVideoRejectionRecordsAnalyticsEvent(t *testing.T) {
	db := newAnalyticsSummaryTestDB(t)
	svc := New(store.NewTestStore(db))
	ctx := context.Background()
	userID := uint(42)

	_, err := svc.IngestNotes(ctx, userID, []CapturePayload{
		{
			SourceNoteID: "normal-note-1",
			NoteType:     model.XhsScriptNoteTypeNormal,
			Title:        "普通图文笔记",
			Content:      "这段内容不应进入 analytics properties",
		},
	})
	require.ErrorIs(t, err, errno.ErrXhsScriptVideoOnly)

	var event model.XhsScriptAnalyticsEvent
	require.NoError(t, db.Where("event_name = ?", "non_video_note_rejected").First(&event).Error)
	require.NotNil(t, event.UserID)
	assert.Equal(t, userID, *event.UserID)

	var props map[string]interface{}
	require.NoError(t, json.Unmarshal(event.Properties, &props))
	assert.Equal(t, "normal-note-1", props["source_note_id"])
	assert.Equal(t, model.XhsScriptNoteTypeNormal, props["note_type"])
	assert.NotContains(t, string(event.Properties), "这段内容不应进入 analytics properties")
}

func TestIngestNotes_DuplicateCaptureRecordsAnalyticsOnce(t *testing.T) {
	db := newAnalyticsSummaryTestDB(t)
	svc := New(store.NewTestStore(db))
	ctx := context.Background()
	userID := uint(43)
	payload := CapturePayload{
		SourceNoteID: "video-note-1",
		NoteType:     model.XhsScriptNoteTypeVideo,
		Title:        "视频笔记",
		VideoURL:     "https://sns-video.xhscdn.com/video-note-1.mp4",
		LikeCount:    100,
		CollectCount: 20,
		CommentCount: 8,
		HotComments:  []Comment{{Content: "有用"}},
	}

	_, err := svc.IngestNotes(ctx, userID, []CapturePayload{payload})
	require.NoError(t, err)
	_, err = svc.IngestNotes(ctx, userID, []CapturePayload{payload})
	require.NoError(t, err)

	var count int64
	require.NoError(t, db.Model(&model.XhsScriptAnalyticsEvent{}).
		Where("event_name = ?", "video_note_captured").
		Count(&count).Error)
	assert.EqualValues(t, 1, count)

	var event model.XhsScriptAnalyticsEvent
	require.NoError(t, db.Where("event_name = ?", "video_note_captured").First(&event).Error)
	assert.Equal(t, capturedNoteEventID(userID, &model.XhsScriptNote{SourceNoteID: payload.SourceNoteID}), event.EventID)
}
