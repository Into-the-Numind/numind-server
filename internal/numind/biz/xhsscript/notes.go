package xhsscript

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

type CapturePayload struct {
	SourceNoteID string    `json:"source_note_id"`
	XhsNoteID    string    `json:"xhs_note_id"`
	NoteID       string    `json:"note_id"`
	NoteURL      string    `json:"note_url"`
	NoteType     string    `json:"note_type"`
	Title        string    `json:"title"`
	Content      string    `json:"content"`
	Description  string    `json:"description"`
	Tags         []string  `json:"tags"`
	CoverURL     string    `json:"cover_url"`
	VideoURL     string    `json:"video_url"`
	LikeCount    int64     `json:"like_count"`
	CollectCount int64     `json:"collect_count"`
	CommentCount int64     `json:"comment_count"`
	Comments     []Comment `json:"comments"`
	HotComments  []Comment `json:"hot_comments"`
}

func (s *Service) GetHome(ctx context.Context, user *model.User) (*HomeDTO, error) {
	if user == nil {
		return nil, errno.ErrTokenInvalid
	}
	account, err := s.ds.XhsScript().CreateOrGetQuotaAccount(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	quota, err := s.quotaDTO(ctx, account)
	if err != nil {
		return nil, err
	}
	profile, err := s.ds.XhsScript().GetOrCreateProfileByUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	notes, err := s.ds.XhsScript().ListNotes(ctx, user.ID, 40, 0)
	if err != nil {
		return nil, err
	}
	dtos := make([]NoteDTO, 0, len(notes))
	for i := range notes {
		dto, err := s.noteDTO(ctx, &notes[i])
		if err != nil {
			return nil, err
		}
		dtos = append(dtos, dto)
	}
	return &HomeDTO{
		User:    userDTO(user),
		Quota:   quota,
		Profile: ProfileDTO{ProfileText: profile.ProfileText},
		Notes:   dtos,
	}, nil
}

func (s *Service) ListNoteDTOs(ctx context.Context, userID uint, limit, offset int) ([]NoteDTO, error) {
	notes, err := s.ds.XhsScript().ListNotes(ctx, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	dtos := make([]NoteDTO, 0, len(notes))
	for i := range notes {
		dto, err := s.noteDTO(ctx, &notes[i])
		if err != nil {
			return nil, err
		}
		dtos = append(dtos, dto)
	}
	return dtos, nil
}

func (s *Service) GetQuota(ctx context.Context, userID uint) (*QuotaDTO, error) {
	account, err := s.ds.XhsScript().CreateOrGetQuotaAccount(ctx, userID)
	if err != nil {
		return nil, err
	}
	quota, err := s.quotaDTO(ctx, account)
	if err != nil {
		return nil, err
	}
	return &quota, nil
}

func (s *Service) SaveProfile(ctx context.Context, userID uint, profileText string) (*ProfileDTO, error) {
	profileText = strings.TrimSpace(profileText)
	if profileText == "" {
		return nil, errno.ErrXhsScriptProfileRequired
	}
	profile, err := s.ds.XhsScript().GetOrCreateProfileByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	profile.ProfileText = profileText
	if err := s.ds.XhsScript().SaveProfile(ctx, profile); err != nil {
		return nil, err
	}
	return &ProfileDTO{ProfileText: profileText}, nil
}

func (s *Service) IngestNotes(ctx context.Context, userID uint, payloads []CapturePayload) ([]NoteDTO, error) {
	if len(payloads) == 0 {
		return nil, errno.ErrInvalidParameter.SetMessage("没有收到可采集的视频笔记")
	}
	notes := make([]*model.XhsScriptNote, 0, len(payloads))
	for _, payload := range payloads {
		note, err := payload.toModel(userID)
		if err != nil {
			if errors.Is(err, errno.ErrXhsScriptVideoOnly) {
				s.RecordEventBestEffort(ctx, userID, "non_video_note_rejected", nonVideoRejectedProperties(payload))
			}
			return nil, err
		}
		notes = append(notes, note)
	}
	if err := s.ensureQuotaForVideoCapture(ctx, userID, len(notes)); err != nil {
		return nil, err
	}

	dtos := make([]NoteDTO, 0, len(notes))
	for _, note := range notes {
		saved, err := s.ds.XhsScript().CreateOrUpsertCapturedNote(ctx, note)
		if err != nil {
			return nil, err
		}
		s.RecordEventWithIDBestEffort(ctx, userID, capturedNoteEventID(userID, saved), "video_note_captured", capturedNoteProperties(saved))
		if shouldTranscribe(saved) {
			s.enqueueTranscription(saved.UserID, saved.ID)
		}
		dto, err := s.noteDTO(ctx, saved)
		if err != nil {
			return nil, err
		}
		dtos = append(dtos, dto)
	}
	return dtos, nil
}

func (s *Service) ensureQuotaForVideoCapture(ctx context.Context, userID uint, noteCount int) error {
	account, err := s.ds.XhsScript().CreateOrGetQuotaAccount(ctx, userID)
	if err != nil {
		return err
	}
	remaining := account.FreeRemaining + account.PaidRemaining
	if remaining < int64(noteCount) {
		s.RecordEventBestEffort(ctx, userID, "capture_blocked_quota_insufficient", map[string]interface{}{
			"notes_count": noteCount,
			"remaining":   remaining,
		})
		return errno.ErrXhsScriptQuotaInsufficient
	}
	return nil
}

func (s *Service) GetNoteDTO(ctx context.Context, userID uint, id uint64) (*NoteDTO, error) {
	note, err := s.ds.XhsScript().GetNote(ctx, userID, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrXhsScriptNoteNotFound
		}
		return nil, err
	}
	dto, err := s.noteDTO(ctx, note)
	if err != nil {
		return nil, err
	}
	return &dto, nil
}

func (p CapturePayload) toModel(userID uint) (*model.XhsScriptNote, error) {
	sourceID := firstNonEmpty(p.SourceNoteID, p.XhsNoteID, p.NoteID)
	noteURL := strings.TrimSpace(p.NoteURL)
	videoURL := strings.TrimSpace(p.VideoURL)
	if sourceID == "" && noteURL != "" {
		sourceID = "url_" + shortHash(noteURL)
	}
	if sourceID == "" {
		return nil, errno.ErrInvalidParameter.SetMessage("缺少小红书笔记 ID")
	}
	noteType := strings.TrimSpace(p.NoteType)
	if noteType == "" && videoURL != "" {
		noteType = model.XhsScriptNoteTypeVideo
	}
	if noteType != model.XhsScriptNoteTypeVideo || videoURL == "" {
		return nil, errno.ErrXhsScriptVideoOnly
	}

	description := strings.TrimSpace(firstNonEmpty(p.Description, p.Content))
	comments := p.HotComments
	if len(comments) == 0 {
		comments = p.Comments
	}
	return &model.XhsScriptNote{
		UserID:       userID,
		SourceNoteID: strings.TrimSpace(sourceID),
		NoteURL:      noteURL,
		NoteType:     model.XhsScriptNoteTypeVideo,
		Title:        strings.TrimSpace(p.Title),
		Description:  description,
		Tags:         mustJSON(p.Tags),
		LikeCount:    p.LikeCount,
		CollectCount: p.CollectCount,
		CommentCount: p.CommentCount,
		HotComments:  mustJSON(comments),
		CoverURL:     strings.TrimSpace(p.CoverURL),
		VideoURL:     videoURL,
		LastError:    "",
	}, nil
}

func nonVideoRejectedProperties(payload CapturePayload) map[string]interface{} {
	sourceID := firstNonEmpty(payload.SourceNoteID, payload.XhsNoteID, payload.NoteID)
	if sourceID == "" && strings.TrimSpace(payload.NoteURL) != "" {
		sourceID = "url_" + shortHash(payload.NoteURL)
	}
	noteType := strings.TrimSpace(payload.NoteType)
	if noteType == "" {
		noteType = "unknown"
	}
	return map[string]interface{}{
		"source_note_id": sourceID,
		"note_type":      noteType,
		"has_video_url":  strings.TrimSpace(payload.VideoURL) != "",
		"title_length":   textLength(payload.Title),
	}
}

func capturedNoteProperties(note *model.XhsScriptNote) map[string]interface{} {
	props := map[string]interface{}{
		"note_id":             note.ID,
		"source_note_id":      note.SourceNoteID,
		"note_type":           note.NoteType,
		"has_video_url":       strings.TrimSpace(note.VideoURL) != "",
		"title_length":        textLength(note.Title),
		"description_length":  textLength(note.Description),
		"tags_count":          len(tagsFromJSON(note.Tags)),
		"hot_comments_count":  len(commentsFromJSON(note.HotComments)),
		"like_count":          note.LikeCount,
		"collect_count":       note.CollectCount,
		"comment_count":       note.CommentCount,
		"transcribe_status":   note.TranscribeStatus,
		"generation_status":   note.GenerateStatus,
		"video_url_available": strings.TrimSpace(note.VideoURL) != "",
	}
	return props
}

func capturedNoteEventID(userID uint, note *model.XhsScriptNote) string {
	if note == nil {
		return fmt.Sprintf("%s:video_note_captured:%d:unknown", backendEventIDPrefix, userID)
	}
	return fmt.Sprintf("%s:video_note_captured:%d:%s", backendEventIDPrefix, userID, shortHash(note.SourceNoteID))
}

func shouldTranscribe(note *model.XhsScriptNote) bool {
	if note == nil || note.NoteType != model.XhsScriptNoteTypeVideo || strings.TrimSpace(note.VideoURL) == "" {
		return false
	}
	switch note.TranscribeStatus {
	case model.XhsScriptTranscribePending, model.XhsScriptTranscribeFailed, model.XhsScriptTranscribeEmpty:
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func limitForPrompt(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len([]rune(value)) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max]) + "..."
}

func ensureNoteReadyForGeneration(note *model.XhsScriptNote) error {
	if note == nil {
		return errno.ErrXhsScriptNoteNotFound
	}
	if note.TranscribeStatus != model.XhsScriptTranscribeReady || note.VideoTranscript == nil || strings.TrimSpace(*note.VideoTranscript) == "" {
		return fmt.Errorf("note %d transcript not ready: %w", note.ID, errno.ErrXhsScriptTranscriptNotReady)
	}
	return nil
}
