package xhsscript

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

func TestNoteDTOResignsMirroredVideoURL(t *testing.T) {
	ctx := context.Background()
	svc := New(store.NewTestStore(newAnalyticsSummaryTestDB(t)))
	signedURL := "https://signed.example/video.mp4?sign=1"

	var capturedObjectKey string
	signerCalls := 0
	prevSigner := signXhsScriptVideoURLFn
	prevHost := xhsScriptCOSBucketHostFn
	xhsScriptCOSBucketHostFn = func() string {
		return "bucket.cos.ap-beijing.myqcloud.com"
	}
	signXhsScriptVideoURLFn = func(_ context.Context, objectKey string) (string, error) {
		signerCalls++
		capturedObjectKey = objectKey
		return signedURL, nil
	}
	t.Cleanup(func() {
		signXhsScriptVideoURLFn = prevSigner
		xhsScriptCOSBucketHostFn = prevHost
	})

	dto, err := svc.noteDTO(ctx, &model.XhsScriptNote{
		ID:       100,
		UserID:   99,
		VideoURL: "https://bucket.cos.ap-beijing.myqcloud.com/xhs-script-media/99/100/video.mp4?stale=1",
	})
	require.NoError(t, err)
	assert.Equal(t, signedURL, dto.VideoURL)
	assert.Equal(t, "xhs-script-media/99/100/video.mp4", capturedObjectKey)
	assert.Equal(t, 1, signerCalls)

	signFailureURL := "https://bucket.cos.ap-beijing.myqcloud.com/xhs-script-media/99/101/video.mp4?stale=1"
	signerCalls = 0
	signXhsScriptVideoURLFn = func(_ context.Context, objectKey string) (string, error) {
		signerCalls++
		capturedObjectKey = objectKey
		return "", errors.New("sign failed")
	}
	dto, err = svc.noteDTO(ctx, &model.XhsScriptNote{ID: 101, UserID: 99, VideoURL: signFailureURL})
	require.NoError(t, err)
	assert.Equal(t, signFailureURL, dto.VideoURL)
	assert.Equal(t, "xhs-script-media/99/101/video.mp4", capturedObjectKey)
	assert.Equal(t, 1, signerCalls)

	evilHostURL := "https://evil.test/xhs-script-media/99/102/video.mp4"
	signXhsScriptVideoURLFn = func(context.Context, string) (string, error) {
		t.Fatal("signer should not be called when mirrored path is on an untrusted host")
		return "", errors.New("unexpected signer call")
	}
	dto, err = svc.noteDTO(ctx, &model.XhsScriptNote{ID: 102, UserID: 99, VideoURL: evilHostURL})
	require.NoError(t, err)
	assert.Equal(t, evilHostURL, dto.VideoURL)

	wrongNoteURL := "https://bucket.cos.ap-beijing.myqcloud.com/xhs-script-media/99/999/video.mp4"
	dto, err = svc.noteDTO(ctx, &model.XhsScriptNote{ID: 103, UserID: 99, VideoURL: wrongNoteURL})
	require.NoError(t, err)
	assert.Equal(t, wrongNoteURL, dto.VideoURL)

	wrongUserURL := "https://bucket.cos.ap-beijing.myqcloud.com/xhs-script-media/98/104/video.mp4"
	dto, err = svc.noteDTO(ctx, &model.XhsScriptNote{ID: 104, UserID: 99, VideoURL: wrongUserURL})
	require.NoError(t, err)
	assert.Equal(t, wrongUserURL, dto.VideoURL)

	signXhsScriptVideoURLFn = func(context.Context, string) (string, error) {
		t.Fatal("signer should not be called for non-mirrored video URL")
		return "", errors.New("unexpected signer call")
	}
	rawURL := "https://sns-video.xhscdn.com/raw.mp4"
	dto, err = svc.noteDTO(ctx, &model.XhsScriptNote{ID: 105, UserID: 99, VideoURL: rawURL})
	require.NoError(t, err)
	assert.Equal(t, rawURL, dto.VideoURL)
}

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

func TestSaveProfileRejectsEmptyProfileText(t *testing.T) {
	db := newAnalyticsSummaryTestDB(t)
	svc := New(store.NewTestStore(db))
	ctx := context.Background()
	userID := uint(44)
	require.NoError(t, db.Create(&model.XhsScriptUserProfile{
		UserID:      userID,
		ProfileText: "已有产品定位",
	}).Error)

	dto, err := svc.SaveProfile(ctx, userID, " \n\t ")

	require.ErrorIs(t, err, errno.ErrXhsScriptProfileRequired)
	assert.Nil(t, dto)

	var profile model.XhsScriptUserProfile
	require.NoError(t, db.Where("user_id = ?", userID).First(&profile).Error)
	assert.Equal(t, "已有产品定位", profile.ProfileText)
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
