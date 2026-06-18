package meeting

// diarize_query.go — speaker-diarization query/summary enrichment (meeting-speaker-diarization
// feature, DIARIZATION_SPEC.md §6 展示规则 / §7 T9).
//
// This is the READ side of diarization: GetSession detail returns each segment's effective
// speaker label (final → online → empty) + the meeting_speaker mapping + diarization_status,
// and the summary path groups speakers by final_speaker_id ("发言人N：…").
//
// FLAG GUARD (DIARIZATION_SPEC.md §4 P1-flag): every enrichment here is gated by
// diarizationEnabled() (= meeting_copilot.enabled && meeting_diarization.enabled). When OFF,
// none of the speaker DTO fields are populated and the summary prompt is built exactly as
// before — behavior is byte-identical to current. Kept in a DEDICATED file so the T9 query
// logic stays apart from the foundation's DTO mapping (disjoint ownership).

import (
	"context"
	"fmt"
	"strings"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// onlineLabelColorCount mirrors diarize.SpeakerColorCount for the ONLINE (A/B/C) temporary
// labels' color round-robin. Final labels carry their own color_index from meeting_speaker;
// online labels have no DB color so we derive one from the cluster id deterministically.
// Kept local (not imported from the diarize pkg) so the query path has no build coupling to
// the clustering internals.
const onlineLabelColorCount = 8

// onlineSpeakerLetter maps an online cluster id (1-based) to a stable letter label A/B/C…
// (DIARIZATION_SPEC.md §6: online temporary labels are letters). cluster 1 → "A", 2 → "B".
// Beyond 26 it wraps with a numeric suffix ("A2") — defensive only; MAX_SPEAKERS is 8.
func onlineSpeakerLetter(clusterID int) string {
	if clusterID <= 0 {
		return ""
	}
	idx := clusterID - 1
	letter := string(rune('A' + idx%26))
	if idx < 26 {
		return letter
	}
	return fmt.Sprintf("%s%d", letter, idx/26+1)
}

// speakerView is the resolved, flag-gated diarization view for one segment: the effective
// display label, its color index (nil = no label), and whether it is a provisional/online
// label. Computed once and used both for the segment DTO and the summary grouping.
type speakerView struct {
	label       string // effective label: final "发言人N" → online "A/B/C" → ""
	colorIndex  *int   // palette index for the label; nil when no label
	provisional bool   // true for online provisional (low-confidence) labels
}

// resolveSpeakerView applies the §6 display rule to one segment given the final-cluster→speaker
// map: final_speaker_id (mapped through meeting_speaker) → online_speaker_id (letter) → empty.
func resolveSpeakerView(seg *model.MeetingSegment, speakerByCluster map[int]model.MeetingSpeaker) speakerView {
	// 1) final label (precision path): final_speaker_id mapped through meeting_speaker.
	if seg.FinalSpeakerID != nil {
		if sp, ok := speakerByCluster[*seg.FinalSpeakerID]; ok {
			ci := sp.ColorIndex
			return speakerView{label: sp.DisplayLabel, colorIndex: &ci, provisional: false}
		}
		// final id present but no mapping row (shouldn't happen — defensive): synthesize.
		ci := (*seg.FinalSpeakerID - 1)
		if ci < 0 {
			ci = 0
		}
		ci %= onlineLabelColorCount
		return speakerView{label: fmt.Sprintf("发言人%d", *seg.FinalSpeakerID), colorIndex: &ci, provisional: false}
	}
	// 2) online temporary label (letter A/B/C…).
	if seg.OnlineSpeakerID != nil && *seg.OnlineSpeakerID > 0 {
		ci := (*seg.OnlineSpeakerID - 1) % onlineLabelColorCount
		return speakerView{label: onlineSpeakerLetter(*seg.OnlineSpeakerID), colorIndex: &ci, provisional: seg.OnlineProvisional}
	}
	// 3) no attribution (soft-degrade / not yet clustered): empty (前端兜底 "发言人?").
	return speakerView{}
}

// loadSpeakerMap loads a meeting's meeting_speaker rows into a cluster_id→row map for label
// resolution. Soft path: on store error it logs and returns nil (the caller then falls back to
// online labels — diarization read errors must never fail GetSession).
func (b *meetingBiz) loadSpeakerMap(ctx context.Context, meetingID uint64) (map[int]model.MeetingSpeaker, []model.MeetingSpeaker) {
	readStore := store.NewDiarizeReadStore(b.ds.DB())
	speakers, err := readStore.ListMeetingSpeakers(ctx, meetingID)
	if err != nil {
		log.Warnw("meeting: load speaker map failed (soft-degraded to online labels)",
			"session_id", meetingID, "error", err)
		return nil, nil
	}
	if len(speakers) == 0 {
		return nil, nil
	}
	byCluster := make(map[int]model.MeetingSpeaker, len(speakers))
	for i := range speakers {
		byCluster[speakers[i].ClusterID] = speakers[i]
	}
	return byCluster, speakers
}

// enrichDetailWithSpeakers populates the diarization fields on a SessionDetailDTO in place
// (segment speaker labels + session diarization_status/speaker_count + speakers list), gated by
// the effective flag. Called from GetSession AFTER the base DTO mapping. When the flag is OFF
// this is a no-op and the response is identical to current behavior.
//
// segs is the SAME slice GetSession listed (seq ASC); detail.Segments aligns index-for-index
// with it (toSegmentDTOs preserves order), so we enrich by index.
func (b *meetingBiz) enrichDetailWithSpeakers(ctx context.Context, detail *SessionDetailDTO, s *model.MeetingSession, segs []model.MeetingSegment) {
	if !diarizationEnabled() {
		return
	}

	// Session-level: surface the diarization status + speaker count (flag-gated).
	detail.Session.DiarizationStatus = s.DiarizationStatus
	detail.Session.SpeakerCount = s.SpeakerCount

	speakerByCluster, speakers := b.loadSpeakerMap(ctx, s.ID)

	// Speakers list (label+color) for the legend.
	if len(speakers) > 0 {
		out := make([]SpeakerDTO, 0, len(speakers))
		for i := range speakers {
			out = append(out, SpeakerDTO{
				ClusterID:  speakers[i].ClusterID,
				Label:      speakers[i].DisplayLabel,
				ColorIndex: speakers[i].ColorIndex,
			})
		}
		detail.Speakers = out
	}

	// Per-segment effective label (final → online → empty). detail.Segments[i] ↔ segs[i].
	for i := range segs {
		if i >= len(detail.Segments) {
			break // defensive: mapping must be 1:1, but never index out of range.
		}
		v := resolveSpeakerView(&segs[i], speakerByCluster)
		dto := &detail.Segments[i]
		dto.SpeakerLabel = v.label
		dto.SpeakerColorIndex = v.colorIndex
		dto.OnlineProvisional = v.provisional && segs[i].FinalSpeakerID == nil
		dto.FinalSpeakerID = segs[i].FinalSpeakerID
		dto.OnlineSpeakerID = segs[i].OnlineSpeakerID
		dto.SpeakerConfidence = segs[i].SpeakerConfidence
	}
}

// ---------------------------------------------------------------------------
// Summary grouping by final_speaker_id (DIARIZATION_SPEC.md §7 T9 (b))
// ---------------------------------------------------------------------------

// joinTranscriptBySpeaker builds the summary transcript with each line prefixed by its
// speaker ("发言人N：…"), grouping consecutive same-speaker segments. It uses final_speaker_id
// mapped through speakerByCluster for the precision label, falling back to the online letter
// label, falling back to no prefix. Output is capped at maxRunes via the same head/tail
// truncation as joinTranscript (preserve opening + closing context).
//
// Only called when diarization is enabled AND at least one segment carries a final label (so a
// no-diarization meeting still uses the plain joinTranscript and the prompt is unchanged).
func joinTranscriptBySpeaker(segs []model.MeetingSegment, speakerByCluster map[int]model.MeetingSpeaker, maxRunes int) string {
	var (
		b        strings.Builder
		lastLbl  string
		hasLast  bool
		wroteAny bool
	)
	flushPrefix := func(lbl string) {
		// Group consecutive same-speaker turns under one prefix line; switch prefix on change.
		if !hasLast || lbl != lastLbl {
			if wroteAny {
				b.WriteByte('\n')
			}
			if lbl != "" {
				b.WriteString(lbl)
				b.WriteString("：")
			}
			lastLbl = lbl
			hasLast = true
		} else {
			b.WriteByte('\n')
		}
	}
	for i := range segs {
		t := strings.TrimSpace(segs[i].Text)
		if t == "" {
			continue
		}
		v := resolveSpeakerView(&segs[i], speakerByCluster)
		flushPrefix(v.label)
		b.WriteString(t)
		wroteAny = true
	}
	full := b.String()
	r := []rune(full)
	if len(r) <= maxRunes {
		return full
	}
	head := maxRunes * 6 / 10
	tail := maxRunes - head
	return string(r[:head]) + "\n\n……（转写过长，中间部分已省略）……\n\n" + string(r[len(r)-tail:])
}

// tailSegments returns the trailing segments whose cumulative non-empty text is ~maxRunes,
// preserving time order (seq ASC). Mirrors buildTranscriptWindow's tail selection but returns
// the segment slice (so the caller can speaker-group it) rather than the joined string. Empty
// input → nil.
func tailSegments(segs []model.MeetingSegment, maxRunes int) []model.MeetingSegment {
	if len(segs) == 0 {
		return nil
	}
	total := 0
	start := len(segs) // exclusive lower bound of the picked window (walk backwards)
	for i := len(segs) - 1; i >= 0; i-- {
		t := strings.TrimSpace(segs[i].Text)
		if t == "" {
			// silent segment: include it in the window span but it adds no runes.
			start = i
			continue
		}
		r := len([]rune(t))
		if total+r > maxRunes && start < len(segs) {
			break
		}
		total += r
		start = i
	}
	if start >= len(segs) {
		return nil
	}
	return segs[start:]
}

// hasAnyFinalSpeaker reports whether any segment carries a final_speaker_id (the trigger for
// speaker-grouped summary). Online-only labels don't trigger grouping for the FINAL summary —
// the offline labels are the stable, presentable ones.
func hasAnyFinalSpeaker(segs []model.MeetingSegment) bool {
	for i := range segs {
		if segs[i].FinalSpeakerID != nil {
			return true
		}
	}
	return false
}

// buildSummaryTranscript chooses the transcript representation for summary generation:
// speaker-grouped ("发言人N：…") when diarization is on and final labels exist, else the plain
// joinTranscript (unchanged current behavior). Centralizes the flag/fallback decision so both
// generateSummary and generateFinalSummary stay consistent.
func (b *meetingBiz) buildSummaryTranscript(ctx context.Context, meetingID uint64, segs []model.MeetingSegment, maxRunes int) string {
	if diarizationEnabled() && hasAnyFinalSpeaker(segs) {
		speakerByCluster, _ := b.loadSpeakerMap(ctx, meetingID)
		return joinTranscriptBySpeaker(segs, speakerByCluster, maxRunes)
	}
	return joinTranscript(segs, maxRunes)
}
