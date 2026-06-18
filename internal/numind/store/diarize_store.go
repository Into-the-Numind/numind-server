package store

// diarize_store.go — speaker-diarization-specific store methods (meeting-speaker-diarization
// feature, DIARIZATION_SPEC.md §6/§7 P2-store). Deliberately a DEDICATED file (not
// store/meeting.go) so the online (T7) and offline (T8) diarization store methods live
// apart from the core meeting CRUD and from each other — minimizing merge conflict and
// satisfying §7 P2-store ("store 方法拆到 diarize 专属 store 文件再 ndf-check-disjoint").
//
// THIS FILE owns the ONLINE (T7) store surface only:
//   - UpdateSegmentOnlineSpeaker: targeted column update of online_speaker_id +
//     online_provisional + speaker_confidence on a single meeting_segment row.
//   - UpsertSegmentEmbedding: persist the 192-d embedding to meeting_segment_embedding
//     (离线 AHC 重聚类主路径前提, P0-2 / D5).
//
// T8 (offline) will add its own methods in this same file under a separate section.
//
// TARGETED COLUMN UPDATE (mirrors store/meeting.go UpdateRunningSummary style): the online
// diarization worker runs concurrently with the realtime relay's segment inserts and the
// rolling-summary fold. A full-row Save of meeting_segment would clobber columns it did not
// author; we only ever touch the three diarization columns via a map-form Updates (map form
// guarantees a false/zero value is written, dodging the §database.md default-bool Create
// gotcha — though online_provisional has no default:true so this is belt-and-suspenders).

import (
	"context"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IDiarizeStore is the online (T7) speaker-diarization store surface.
//
// It is intentionally NOT folded into IMeetingStore: it is feature-flag-gated dead weight
// when diarization is off, and keeping it separate keeps store/meeting.go untouched (T8
// disjointness). Callers construct it from a *gorm.DB via NewDiarizeStore (the biz layer
// has db access through store.IStore.DB()).
type IDiarizeStore interface {
	// UpdateSegmentOnlineSpeaker targets online_speaker_id / online_provisional /
	// speaker_confidence on one meeting_segment row. A nil speakerID writes SQL NULL
	// (soft-degrade: voiceprint unavailable ⇒ no attribution). Returns the underlying
	// error; a not-found (RowsAffected==0) is NOT an error — the worker logs and moves on.
	UpdateSegmentOnlineSpeaker(ctx context.Context, segmentID uint64, speakerID *int, provisional bool, confidence *float32) error

	// UpsertSegmentEmbedding stores (or replaces, by unique segment_id) one segment's
	// packed float32×192 embedding for the offline re-clustering main path (P0-2).
	UpsertSegmentEmbedding(ctx context.Context, meetingID, segmentID uint64, embedding []byte) error
}

// diarizeStore is the GORM implementation of IDiarizeStore.
type diarizeStore struct {
	db *gorm.DB
}

var _ IDiarizeStore = (*diarizeStore)(nil)

// NewDiarizeStore builds an IDiarizeStore over the given DB. Exported so the meeting biz
// (T7 worker) can construct it from store.IStore.DB() without widening the IStore interface
// (which would risk a T8 merge conflict on store.go).
func NewDiarizeStore(db *gorm.DB) IDiarizeStore {
	return &diarizeStore{db: db}
}

// UpdateSegmentOnlineSpeaker performs a targeted column update on one meeting_segment row.
//
// Uses map-form Updates so online_provisional=false and a nil confidence are written
// explicitly (map form always includes the key; see .claude/rules/database.md §6b on why
// the struct form would drop a zero-value bool). A nil speakerID writes NULL (gorm maps a
// typed nil pointer in a map value to SQL NULL).
func (s *diarizeStore) UpdateSegmentOnlineSpeaker(ctx context.Context, segmentID uint64, speakerID *int, provisional bool, confidence *float32) error {
	return s.db.WithContext(ctx).
		Model(&model.MeetingSegment{}).
		Where("id = ?", segmentID).
		Updates(map[string]interface{}{
			"online_speaker_id":  speakerID,
			"online_provisional": provisional,
			"speaker_confidence": confidence,
		}).Error
}

// UpsertSegmentEmbedding inserts the embedding row, or on the unique segment_id conflict
// replaces the embedding blob (idempotent re-embed). meeting_segment_embedding has a UNIQUE
// key on segment_id (uk_mse_segment), so OnConflict(DoUpdates) is the natural upsert.
func (s *diarizeStore) UpsertSegmentEmbedding(ctx context.Context, meetingID, segmentID uint64, embedding []byte) error {
	row := &model.MeetingSegmentEmbedding{
		MeetingID: meetingID,
		SegmentID: segmentID,
		Embedding: embedding,
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "segment_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"embedding", "meeting_id"}),
		}).
		Create(row).Error
}

// ===========================================================================
// OFFLINE (T8) speaker-diarization store surface
// ===========================================================================
//
// The offline refinement worker (biz/meeting/diarize/offline.go) re-clusters a meeting's
// already-stored per-segment embeddings with global AHC, then writes back: the appearance-
// ordered MeetingSpeaker rows, each segment's final_speaker_id, and the session's
// diarization_status + speaker_count. These methods are kept in this dedicated file (NOT
// store/meeting.go) per DIARIZATION_SPEC.md §7 P2-store, apart from both the core meeting CRUD
// and the T7 online surface, to minimize merge surface.
//
// IDiarizeOfflineStore is a SEPARATE interface from IDiarizeStore: the offline path runs
// post-meeting (not in the hot relay loop) and has no overlap with the online write surface,
// so splitting keeps each surface minimal and independently fakeable. Both are constructed
// from a *gorm.DB (NewDiarizeOfflineStore), so the biz layer wires them from store.IStore.DB()
// without widening the IStore interface (T8 avoids touching store.go).

// LoadedEmbedding is one segment's stored embedding, returned in stable appearance order
// (meeting_segment.seq ASC) so the offline clusterer can assign appearance-ordered speaker
// numbers deterministically (重试不漂移 — DIARIZATION_SPEC.md §8 idempotency).
type LoadedEmbedding struct {
	// SegmentID is the meeting_segment PK this embedding belongs to.
	SegmentID uint64
	// Seq is the segment's ordering within the meeting (the basis for appearance-order numbering).
	Seq int
	// Embedding is the float32×192 little-endian packed BLOB (caller unpacks).
	Embedding []byte
}

// FinalSpeakerAssignment is one segment's offline cluster assignment to be persisted to
// meeting_segment.final_speaker_id.
type FinalSpeakerAssignment struct {
	SegmentID uint64
	// ClusterID is the offline (final) cluster id; mapped to a MeetingSpeaker display label.
	ClusterID int
	// Confidence is the offline assignment confidence (nil when unknown); persisted to
	// meeting_segment.speaker_confidence so the UI can weaken low-confidence final labels.
	Confidence *float32
}

// IDiarizeOfflineStore is the offline (T8) speaker-diarization store surface.
type IDiarizeOfflineStore interface {
	// LoadSegmentEmbeddings returns all stored embeddings for a meeting joined to their
	// segment seq, ordered by seq ASC (stable appearance order). Segments with no stored
	// embedding are simply absent (the offline main path only clusters what was embedded
	// online — P0-2). Empty result + nil error is valid (nothing to refine).
	LoadSegmentEmbeddings(ctx context.Context, meetingID uint64) ([]LoadedEmbedding, error)

	// ListSegmentsForFallback returns (segmentID, seq, startMs, durationMs) for every segment
	// of a meeting in seq order, used to build the /diarize fallback request and to map its
	// per-segment output back. Independent of stored embeddings (the fallback exists precisely
	// because embeddings are missing).
	ListSegmentsForFallback(ctx context.Context, meetingID uint64) ([]FallbackSegment, error)

	// ReplaceMeetingSpeakers idempotently rewrites the meeting_speaker rows for a meeting:
	// it deletes any existing rows then inserts the supplied set, all in one transaction.
	// Idempotency (重试不漂移) relies on the caller passing appearance-ordered, deterministic
	// rows; a retry that recomputes the same clustering produces the same rows.
	ReplaceMeetingSpeakers(ctx context.Context, meetingID uint64, speakers []model.MeetingSpeaker) error

	// SetSegmentFinalSpeakers writes final_speaker_id (+ speaker_confidence when provided) for
	// the given segments via targeted column updates (map form, so a nil confidence writes
	// NULL). Runs each update in its own statement; a per-row failure is returned so the
	// caller can decide (the offline worker logs + continues, then marks failed only if the
	// whole pass is unusable).
	SetSegmentFinalSpeakers(ctx context.Context, assignments []FinalSpeakerAssignment) error

	// SetDiarizationStatus targets meeting_session.diarization_status (+ speaker_count when
	// non-nil) via a map-form Updates. A nil speakerCount leaves the column untouched.
	SetDiarizationStatus(ctx context.Context, meetingID uint64, status string, speakerCount *int) error
}

// IDiarizeReadStore is the read-only (T9 query path) speaker-diarization store surface.
//
// It is a SEPARATE interface from the online/offline write surfaces: the controller/query
// path (GetSession detail) only needs to LIST a meeting's meeting_speaker rows to map each
// segment's final_speaker_id → display label + color. Keeping it apart from the write
// interfaces preserves the §7 P2-store split (write surfaces untouched) and keeps
// store/meeting.go untouched (T9 disjointness). Constructed from a *gorm.DB
// (NewDiarizeReadStore), wired by biz from store.IStore.DB() without widening IStore.
type IDiarizeReadStore interface {
	// ListMeetingSpeakers returns all meeting_speaker rows for a meeting ordered by cluster_id
	// ASC (== appearance order, since the offline path numbers clusters 1..N in appearance
	// order). Empty result + nil error is valid (not yet refined / soft-degraded — the query
	// path then falls back to online A/B/C labels).
	ListMeetingSpeakers(ctx context.Context, meetingID uint64) ([]model.MeetingSpeaker, error)
}

// diarizeReadStore is the GORM implementation of IDiarizeReadStore.
type diarizeReadStore struct {
	db *gorm.DB
}

var _ IDiarizeReadStore = (*diarizeReadStore)(nil)

// NewDiarizeReadStore builds an IDiarizeReadStore over the given DB. Exported so the meeting
// biz (T9 query path) can construct it from store.IStore.DB() without widening IStore.
func NewDiarizeReadStore(db *gorm.DB) IDiarizeReadStore {
	return &diarizeReadStore{db: db}
}

// ListMeetingSpeakers returns a meeting's speaker rows in cluster_id ASC order.
func (s *diarizeReadStore) ListMeetingSpeakers(ctx context.Context, meetingID uint64) ([]model.MeetingSpeaker, error) {
	var list []model.MeetingSpeaker
	if err := s.db.WithContext(ctx).
		Where("meeting_id = ?", meetingID).
		Order("cluster_id ASC").
		Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// FallbackSegment is one segment's timing metadata for the /diarize fallback path.
type FallbackSegment struct {
	SegmentID  uint64
	Seq        int
	StartMs    int
	DurationMs int64
}

// diarizeOfflineStore is the GORM implementation of IDiarizeOfflineStore.
type diarizeOfflineStore struct {
	db *gorm.DB
}

var _ IDiarizeOfflineStore = (*diarizeOfflineStore)(nil)

// NewDiarizeOfflineStore builds an IDiarizeOfflineStore over the given DB. Exported so the
// meeting biz (T8 offline worker) can construct it from store.IStore.DB() without widening
// IStore (which would risk a merge conflict on store.go).
func NewDiarizeOfflineStore(db *gorm.DB) IDiarizeOfflineStore {
	return &diarizeOfflineStore{db: db}
}

// LoadSegmentEmbeddings joins meeting_segment_embedding to meeting_segment to return each
// stored embedding alongside its segment seq, ordered by seq ASC (stable appearance order).
//
// JOIN (not Preload) because we filter+order on the segment table while selecting the
// embedding blob — a single round-trip. The join key (segment_id) is uniquely indexed on the
// embedding table and is the PK of the segment table. We scope by the embedding's meeting_id
// (indexed idx_mse_meeting); the segment join is a belt-and-suspenders correlation that also
// supplies seq.
func (s *diarizeOfflineStore) LoadSegmentEmbeddings(ctx context.Context, meetingID uint64) ([]LoadedEmbedding, error) {
	var rows []LoadedEmbedding
	err := s.db.WithContext(ctx).
		Table("meeting_segment_embedding AS e").
		Select("e.segment_id AS segment_id, seg.seq AS seq, e.embedding AS embedding").
		Joins("JOIN meeting_segment AS seg ON seg.id = e.segment_id").
		Where("e.meeting_id = ?", meetingID).
		Order("seg.seq ASC, e.segment_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ListSegmentsForFallback returns every segment's timing for the meeting in seq order.
func (s *diarizeOfflineStore) ListSegmentsForFallback(ctx context.Context, meetingID uint64) ([]FallbackSegment, error) {
	var rows []FallbackSegment
	// duration_seconds is a float second count; convert to ms in SQL so the caller gets int64.
	err := s.db.WithContext(ctx).
		Table("meeting_segment").
		Select("id AS segment_id, seq AS seq, start_ms AS start_ms, CAST(duration_seconds * 1000 AS SIGNED) AS duration_ms").
		Where("session_id = ?", meetingID).
		Order("seq ASC, id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ReplaceMeetingSpeakers deletes-then-inserts the meeting_speaker rows in one transaction.
//
// Idempotent on retry: the caller recomputes deterministic appearance-ordered rows, so the
// delete+insert leaves the table in the same state. We do NOT rely on the uk_ms_meeting_cluster
// upsert because a retry that produced a DIFFERENT cluster count must not leave stale rows
// behind — a clean delete+insert is the only way to guarantee no orphan cluster ids.
func (s *diarizeOfflineStore) ReplaceMeetingSpeakers(ctx context.Context, meetingID uint64, speakers []model.MeetingSpeaker) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("meeting_id = ?", meetingID).Delete(&model.MeetingSpeaker{}).Error; err != nil {
			return err
		}
		if len(speakers) == 0 {
			return nil
		}
		// Defensive: stamp meeting_id on every row regardless of caller (single source of truth).
		for i := range speakers {
			speakers[i].MeetingID = meetingID
		}
		return tx.Create(&speakers).Error
	})
}

// SetSegmentFinalSpeakers performs one targeted column update per segment (map form so a nil
// confidence writes SQL NULL and a zero cluster id is still written). A nil assignments slice
// is a no-op. Stops and returns on the first error (the offline worker treats a persist error
// as a failed pass).
func (s *diarizeOfflineStore) SetSegmentFinalSpeakers(ctx context.Context, assignments []FinalSpeakerAssignment) error {
	for _, a := range assignments {
		updates := map[string]interface{}{"final_speaker_id": a.ClusterID}
		if a.Confidence != nil {
			updates["speaker_confidence"] = a.Confidence
		}
		if err := s.db.WithContext(ctx).
			Model(&model.MeetingSegment{}).
			Where("id = ?", a.SegmentID).
			Updates(updates).Error; err != nil {
			return err
		}
	}
	return nil
}

// SetDiarizationStatus targets diarization_status (+ speaker_count when non-nil) on one
// meeting_session row. map-form Updates so the string status is always written; a nil
// speakerCount leaves the column as-is (e.g. a transition to "refining" / "failed" should not
// clobber a previously computed count).
func (s *diarizeOfflineStore) SetDiarizationStatus(ctx context.Context, meetingID uint64, status string, speakerCount *int) error {
	updates := map[string]interface{}{"diarization_status": status}
	if speakerCount != nil {
		updates["speaker_count"] = *speakerCount
	}
	return s.db.WithContext(ctx).
		Model(&model.MeetingSession{}).
		Where("id = ?", meetingID).
		Updates(updates).Error
}
