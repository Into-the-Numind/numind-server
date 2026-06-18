package meeting

// diarize_query_test.go — speaker-diarization query/summary enrichment unit tests
// (meeting-speaker-diarization feature, DIARIZATION_SPEC.md §6 / §7 T9).
//
// Reuses newDiarizeTestBiz / setDiarizationFlags from offline_trigger_test.go (same package).
// Covers, per §7 T9 acceptance:
//   (a) GetSession effective label resolution: final → online → empty, + speakers list +
//       diarization_status; FLAG OFF ⇒ none of it appears (behavior identical to current).
//   (b) summary grouping by final_speaker_id ("发言人N：…").
//   - soft-degrade: a segment with no attribution gets an empty label (前端兜底 "发言人?").

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

func intp(v int) *int { return &v }

// seedSessionForQuery creates an ended session with segments carrying the given online/final
// speaker ids (nil = unset) and optionally meeting_speaker rows. Returns the session id.
func seedSessionForQuery(t *testing.T, db *gorm.DB, userID uint, segs []model.MeetingSegment, speakers []model.MeetingSpeaker, status string, speakerCount *int) uint64 {
	t.Helper()
	sess := &model.MeetingSession{
		UserID:            userID,
		Title:             "查询测试会议",
		RolePrompt:        "r",
		Status:            model.MeetingSessionStatusEnded,
		SummaryStatus:     model.MeetingSummaryStatusDone,
		DiarizationStatus: status,
		SpeakerCount:      speakerCount,
	}
	require.NoError(t, db.Create(sess).Error)
	for i := range segs {
		segs[i].SessionID = sess.ID
		if segs[i].Seq == 0 {
			segs[i].Seq = i + 1
		}
		require.NoError(t, db.Create(&segs[i]).Error)
	}
	for i := range speakers {
		speakers[i].MeetingID = sess.ID
		require.NoError(t, db.Create(&speakers[i]).Error)
	}
	return sess.ID
}

// TestGetSession_FlagOff_NoSpeakerFields: flag off ⇒ the detail response carries NO speaker
// data even when the DB rows have final/online ids (behavior identical to pre-diarization).
func TestGetSession_FlagOff_NoSpeakerFields(t *testing.T) {
	setDiarizationFlags(t, true, false) // copilot on, diarization off ⇒ effective off

	b, db := newDiarizeTestBiz(t)
	id := seedSessionForQuery(t, db, 7,
		[]model.MeetingSegment{
			{Text: "大家好", OnlineSpeakerID: intp(1), FinalSpeakerID: intp(1)},
		},
		[]model.MeetingSpeaker{{ClusterID: 1, DisplayLabel: "发言人1", ColorIndex: 0}},
		model.MeetingDiarizationStatusDone, intp(1))

	detail, err := b.GetSession(context.Background(), 7, id)
	require.NoError(t, err)
	require.Len(t, detail.Segments, 1)

	assert.Empty(t, detail.Segments[0].SpeakerLabel, "flag off ⇒ no speaker label")
	assert.Nil(t, detail.Segments[0].SpeakerColorIndex)
	assert.Nil(t, detail.Segments[0].FinalSpeakerID)
	assert.Nil(t, detail.Segments[0].OnlineSpeakerID)
	assert.Nil(t, detail.Speakers, "flag off ⇒ no speakers list")
	assert.Empty(t, detail.Session.DiarizationStatus, "flag off ⇒ diarization_status hidden")
	assert.Nil(t, detail.Session.SpeakerCount)
}

// TestGetSession_FinalLabelPrecision: flag on + final_speaker_id set ⇒ effective label is the
// meeting_speaker mapping (发言人N), with its color_index; speakers list + status returned.
func TestGetSession_FinalLabelPrecision(t *testing.T) {
	setDiarizationFlags(t, true, true)

	b, db := newDiarizeTestBiz(t)
	id := seedSessionForQuery(t, db, 7,
		[]model.MeetingSegment{
			{Text: "你好", OnlineSpeakerID: intp(1), FinalSpeakerID: intp(1)},
			{Text: "我也好", OnlineSpeakerID: intp(2), FinalSpeakerID: intp(2)},
		},
		[]model.MeetingSpeaker{
			{ClusterID: 1, DisplayLabel: "发言人1", ColorIndex: 0},
			{ClusterID: 2, DisplayLabel: "发言人2", ColorIndex: 1},
		},
		model.MeetingDiarizationStatusDone, intp(2))

	detail, err := b.GetSession(context.Background(), 7, id)
	require.NoError(t, err)
	require.Len(t, detail.Segments, 2)

	assert.Equal(t, "发言人1", detail.Segments[0].SpeakerLabel)
	require.NotNil(t, detail.Segments[0].SpeakerColorIndex)
	assert.Equal(t, 0, *detail.Segments[0].SpeakerColorIndex)
	assert.Equal(t, "发言人2", detail.Segments[1].SpeakerLabel)
	require.NotNil(t, detail.Segments[1].SpeakerColorIndex)
	assert.Equal(t, 1, *detail.Segments[1].SpeakerColorIndex)

	// speakers legend + session status surfaced.
	require.Len(t, detail.Speakers, 2)
	assert.Equal(t, 1, detail.Speakers[0].ClusterID)
	assert.Equal(t, "发言人1", detail.Speakers[0].Label)
	assert.Equal(t, model.MeetingDiarizationStatusDone, detail.Session.DiarizationStatus)
	require.NotNil(t, detail.Session.SpeakerCount)
	assert.Equal(t, 2, *detail.Session.SpeakerCount)
}

// TestGetSession_OnlineLabelFallback: flag on, NO final yet (refining), online ids present ⇒
// effective label is the letter A/B/C; provisional flag carried through.
func TestGetSession_OnlineLabelFallback(t *testing.T) {
	setDiarizationFlags(t, true, true)

	b, db := newDiarizeTestBiz(t)
	id := seedSessionForQuery(t, db, 7,
		[]model.MeetingSegment{
			{Text: "甲发言", OnlineSpeakerID: intp(1)},
			{Text: "乙发言", OnlineSpeakerID: intp(2), OnlineProvisional: true},
		},
		nil, // no meeting_speaker rows yet
		model.MeetingDiarizationStatusOnline, nil)

	detail, err := b.GetSession(context.Background(), 7, id)
	require.NoError(t, err)
	require.Len(t, detail.Segments, 2)

	assert.Equal(t, "A", detail.Segments[0].SpeakerLabel, "online cluster 1 → A")
	assert.False(t, detail.Segments[0].OnlineProvisional)
	assert.Equal(t, "B", detail.Segments[1].SpeakerLabel, "online cluster 2 → B")
	assert.True(t, detail.Segments[1].OnlineProvisional, "provisional carried through")
	require.NotNil(t, detail.Segments[0].OnlineSpeakerID)
	assert.Equal(t, 1, *detail.Segments[0].OnlineSpeakerID)
	assert.Nil(t, detail.Speakers, "no meeting_speaker rows ⇒ nil speakers list")
	assert.Equal(t, model.MeetingDiarizationStatusOnline, detail.Session.DiarizationStatus)
}

// TestGetSession_NoAttribution_EmptyLabel: flag on but a segment has neither final nor online
// id (soft-degrade / voiceprint unavailable) ⇒ empty label (前端兜底 "发言人?").
func TestGetSession_NoAttribution_EmptyLabel(t *testing.T) {
	setDiarizationFlags(t, true, true)

	b, db := newDiarizeTestBiz(t)
	id := seedSessionForQuery(t, db, 7,
		[]model.MeetingSegment{
			{Text: "无人归属"}, // no online, no final
		},
		nil, model.MeetingDiarizationStatusOnline, nil)

	detail, err := b.GetSession(context.Background(), 7, id)
	require.NoError(t, err)
	require.Len(t, detail.Segments, 1)

	assert.Empty(t, detail.Segments[0].SpeakerLabel, "no attribution ⇒ empty label")
	assert.Nil(t, detail.Segments[0].SpeakerColorIndex)
	assert.Nil(t, detail.Segments[0].FinalSpeakerID)
	assert.Nil(t, detail.Segments[0].OnlineSpeakerID)
}

// TestGetSession_FinalOverridesOnlineProvisional: a segment with BOTH a final id and an online
// provisional flag must show the final (stable) label and NOT report provisional (final is the
// authoritative, non-provisional label).
func TestGetSession_FinalOverridesOnlineProvisional(t *testing.T) {
	setDiarizationFlags(t, true, true)

	b, db := newDiarizeTestBiz(t)
	id := seedSessionForQuery(t, db, 7,
		[]model.MeetingSegment{
			{Text: "终标覆盖", OnlineSpeakerID: intp(2), OnlineProvisional: true, FinalSpeakerID: intp(1)},
		},
		[]model.MeetingSpeaker{{ClusterID: 1, DisplayLabel: "发言人1", ColorIndex: 3}},
		model.MeetingDiarizationStatusDone, intp(1))

	detail, err := b.GetSession(context.Background(), 7, id)
	require.NoError(t, err)
	require.Len(t, detail.Segments, 1)

	assert.Equal(t, "发言人1", detail.Segments[0].SpeakerLabel)
	require.NotNil(t, detail.Segments[0].SpeakerColorIndex)
	assert.Equal(t, 3, *detail.Segments[0].SpeakerColorIndex)
	assert.False(t, detail.Segments[0].OnlineProvisional, "final label is authoritative, not provisional")
}

// ---------------------------------------------------------------------------
// (b) summary grouping by final_speaker_id
// ---------------------------------------------------------------------------

// TestJoinTranscriptBySpeaker_Grouping: consecutive same-speaker segments group under one
// prefix line; speaker change emits a new "发言人N：" prefix.
func TestJoinTranscriptBySpeaker_Grouping(t *testing.T) {
	speakers := map[int]model.MeetingSpeaker{
		1: {ClusterID: 1, DisplayLabel: "发言人1"},
		2: {ClusterID: 2, DisplayLabel: "发言人2"},
	}
	segs := []model.MeetingSegment{
		{Seq: 1, Text: "开场白", FinalSpeakerID: intp(1)},
		{Seq: 2, Text: "继续说", FinalSpeakerID: intp(1)},
		{Seq: 3, Text: "我来回应", FinalSpeakerID: intp(2)},
		{Seq: 4, Text: "再补一句", FinalSpeakerID: intp(1)},
	}
	out := joinTranscriptBySpeaker(segs, speakers, 12000)

	// 发言人1 prefix appears twice (turn 1 and turn 4), 发言人2 once.
	assert.Equal(t, 2, strings.Count(out, "发言人1："), "speaker1 prefix on each of its turns")
	assert.Equal(t, 1, strings.Count(out, "发言人2："))
	// grouped lines: "开场白" and "继续说" share the same 发言人1 prefix turn (no extra prefix between).
	assert.Contains(t, out, "发言人1：开场白\n继续说")
	assert.Contains(t, out, "发言人2：我来回应")
}

// TestJoinTranscriptBySpeaker_OnlineFallbackLabel: a segment with only an online id (no final,
// not in speaker map) groups under its letter label.
func TestJoinTranscriptBySpeaker_OnlineFallbackLabel(t *testing.T) {
	segs := []model.MeetingSegment{
		{Seq: 1, Text: "甲说话", OnlineSpeakerID: intp(1)},
		{Seq: 2, Text: "乙说话", OnlineSpeakerID: intp(2)},
	}
	out := joinTranscriptBySpeaker(segs, nil, 12000)
	assert.Contains(t, out, "A：甲说话")
	assert.Contains(t, out, "B：乙说话")
}

// TestBuildSummaryTranscript_FlagOffPlainJoin: flag off ⇒ buildSummaryTranscript returns the
// plain joinTranscript (no speaker prefixes), identical to current behavior.
func TestBuildSummaryTranscript_FlagOffPlainJoin(t *testing.T) {
	setDiarizationFlags(t, true, false)

	b, db := newDiarizeTestBiz(t)
	id := seedSessionForQuery(t, db, 7,
		[]model.MeetingSegment{
			{Text: "第一句", FinalSpeakerID: intp(1)},
			{Text: "第二句", FinalSpeakerID: intp(2)},
		},
		[]model.MeetingSpeaker{
			{ClusterID: 1, DisplayLabel: "发言人1"},
			{ClusterID: 2, DisplayLabel: "发言人2"},
		},
		model.MeetingDiarizationStatusDone, intp(2))

	var segs []model.MeetingSegment
	require.NoError(t, db.Where("session_id = ?", id).Order("seq ASC").Find(&segs).Error)

	out := b.buildSummaryTranscript(context.Background(), id, segs, 12000)
	assert.NotContains(t, out, "发言人1：", "flag off ⇒ no speaker grouping")
	assert.Equal(t, "第一句\n第二句", out)
}

// TestBuildSummaryTranscript_FlagOnNoFinalPlainJoin: flag on but NO final ids (only online) ⇒
// final summary uses plain join (online-only labels are not stable enough for the final
// summary; grouping triggers only on final ids).
func TestBuildSummaryTranscript_FlagOnNoFinalPlainJoin(t *testing.T) {
	setDiarizationFlags(t, true, true)

	b, db := newDiarizeTestBiz(t)
	id := seedSessionForQuery(t, db, 7,
		[]model.MeetingSegment{
			{Text: "甲", OnlineSpeakerID: intp(1)},
			{Text: "乙", OnlineSpeakerID: intp(2)},
		},
		nil, model.MeetingDiarizationStatusOnline, nil)

	var segs []model.MeetingSegment
	require.NoError(t, db.Where("session_id = ?", id).Order("seq ASC").Find(&segs).Error)

	out := b.buildSummaryTranscript(context.Background(), id, segs, 12000)
	assert.Equal(t, "甲\n乙", out, "no final ids ⇒ plain join (no grouping)")
}

// TestBuildSummaryTranscript_FlagOnWithFinalGroups: flag on + final ids ⇒ speaker-grouped.
func TestBuildSummaryTranscript_FlagOnWithFinalGroups(t *testing.T) {
	setDiarizationFlags(t, true, true)

	b, db := newDiarizeTestBiz(t)
	id := seedSessionForQuery(t, db, 7,
		[]model.MeetingSegment{
			{Text: "甲发言", FinalSpeakerID: intp(1)},
			{Text: "乙发言", FinalSpeakerID: intp(2)},
		},
		[]model.MeetingSpeaker{
			{ClusterID: 1, DisplayLabel: "发言人1"},
			{ClusterID: 2, DisplayLabel: "发言人2"},
		},
		model.MeetingDiarizationStatusDone, intp(2))

	var segs []model.MeetingSegment
	require.NoError(t, db.Where("session_id = ?", id).Order("seq ASC").Find(&segs).Error)

	out := b.buildSummaryTranscript(context.Background(), id, segs, 12000)
	assert.Contains(t, out, "发言人1：甲发言")
	assert.Contains(t, out, "发言人2：乙发言")
}

// TestTailSegments_PreservesOrderAndCovers: tailSegments returns trailing segments in seq
// order covering ~maxRunes of text.
func TestTailSegments_PreservesOrderAndCovers(t *testing.T) {
	segs := []model.MeetingSegment{
		{Seq: 1, Text: "一二三四五"}, // 5 runes
		{Seq: 2, Text: "六七八九十"}, // 5 runes
		{Seq: 3, Text: "末段"},    // 2 runes
	}
	// maxRunes=4 ⇒ only the last segment (2 runes) fits; tail starts at seq 3.
	tail := tailSegments(segs, 4)
	require.Len(t, tail, 1)
	assert.Equal(t, 3, tail[0].Seq)

	// maxRunes=100 ⇒ all three, in order.
	tail = tailSegments(segs, 100)
	require.Len(t, tail, 3)
	assert.Equal(t, 1, tail[0].Seq)
	assert.Equal(t, 3, tail[2].Seq)

	assert.Nil(t, tailSegments(nil, 10))
}

// TestOnlineSpeakerLetter: cluster id → stable letter label.
func TestOnlineSpeakerLetter(t *testing.T) {
	assert.Equal(t, "", onlineSpeakerLetter(0))
	assert.Equal(t, "A", onlineSpeakerLetter(1))
	assert.Equal(t, "B", onlineSpeakerLetter(2))
	assert.Equal(t, "H", onlineSpeakerLetter(8))
	assert.Equal(t, "A2", onlineSpeakerLetter(27), "wraps past 26 defensively")
}
